package agents

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// This file holds pure verifiers for the evidence a container runtime reports
// back: the image identity it resolved and the cgroup files it applied. They do
// not execute Docker, resolve tools, or start anything. Their only authority is
// the mandatory ExecutionPolicy, which must be validated before any observation
// is trusted.

const (
	// maxContainerInspectBytes bounds the docker image inspect document before
	// it is parsed, so a hostile or runaway runtime cannot make verification
	// allocate without limit.
	maxContainerInspectBytes = 1 << 20
	// maxContainerValueBytes bounds one raw cgroup file line.
	maxContainerValueBytes = 64
	// maxContainerLimitValue keeps accepted limits inside a range where every
	// later arithmetic comparison is exact rather than near overflow.
	maxContainerLimitValue int64 = 1 << 62
)

// dockerImageInspect is the bounded subset of `docker image inspect` this
// verifier trusts. Unknown fields are ignored on purpose: verifying identity
// must not depend on whatever else a runtime version decides to report.
type dockerImageInspect struct {
	ID          string   `json:"Id"`
	OS          string   `json:"Os"`
	RepoDigests []string `json:"RepoDigests"`
}

// VerifyContainerImage checks that the inspected image is exactly the immutable
// image the mandatory policy requested. It returns the runtime's config image
// ID only after the OS, ID and repository-digest evidence all agree.
//
// A bare policy image (sha256:<64 hex>) must equal the reported config ID. A
// digest reference (repository@sha256:<64 hex>) must appear verbatim in
// RepoDigests. A manifest digest is not a config image ID, so neither form is
// ever satisfied by the other.
func VerifyContainerImage(policy ExecutionPolicy, dockerInspectJSON []byte) (string, error) {
	if err := validateExecutionPolicy(policy); err != nil {
		return "", err
	}
	if len(dockerInspectJSON) > maxContainerInspectBytes {
		return "", fmt.Errorf("docker image inspect output is %d bytes, above the %d-byte bound", len(dockerInspectJSON), maxContainerInspectBytes)
	}
	var inspected []dockerImageInspect
	if err := json.Unmarshal(dockerInspectJSON, &inspected); err != nil {
		return "", fmt.Errorf("docker image inspect output is not valid JSON: %w", err)
	}
	switch len(inspected) {
	case 0:
		return "", fmt.Errorf("docker image inspect reported no image; exactly one was expected")
	case 1:
	default:
		return "", fmt.Errorf("docker image inspect reported %d images; exactly one was expected", len(inspected))
	}
	image := inspected[0]
	if image.OS != "linux" {
		return "", fmt.Errorf("container image operating system is %q; the mandatory policy requires linux", image.OS)
	}
	if !validContainerImageID(image.ID) {
		return "", fmt.Errorf("container image ID %q is not a full sha256:<64 lowercase hex> config ID", image.ID)
	}
	if strings.HasPrefix(policy.Image, "sha256:") {
		if image.ID != policy.Image {
			return "", fmt.Errorf("container image ID %q does not match requested image ID %q", image.ID, policy.Image)
		}
		return image.ID, nil
	}
	for _, digest := range image.RepoDigests {
		if digest == policy.Image {
			return image.ID, nil
		}
	}
	return "", fmt.Errorf("container image RepoDigests do not contain requested digest reference %q", policy.Image)
}

// validContainerImageID accepts only a complete sha256 config image ID.
func validContainerImageID(imageID string) bool {
	hash, found := strings.CutPrefix(imageID, "sha256:")
	return found && imageDigestHashPattern.MatchString(hash)
}

// ContainerCgroupValues carries the raw, untrimmed contents of the four cgroup
// files that prove a container received the mandatory bounds. They are kept as
// raw text rather than parsed values so the verifier remains the only parser
// and can report a field-specific refusal.
type ContainerCgroupValues struct {
	MemoryMax     string
	MemorySwapMax string
	CPUMax        string
	PIDsMax       string
}

// ContainerVerifiedLimits is the typed set of bounds observed in cgroup files.
// It is returned only when all four limits verified, so a caller can never hold
// a partially verified result. The fields carry no paths, IDs or secrets.
type ContainerVerifiedLimits struct {
	MemoryBytes     int64 `json:"memory_bytes"`
	MemorySwapBytes int64 `json:"memory_swap_bytes"`
	CPUCount        int   `json:"cpu_count"`
	PIDsLimit       int   `json:"pids_limit"`
}

// VerifyContainerCgroup checks that the four raw cgroup files apply exactly the
// mandatory policy: memory.max equals memory_bytes, memory.swap.max is zero,
// pids.max equals pids_limit, and cpu.max is a finite quota/period pair whose
// ratio is exactly cpu_count.
func VerifyContainerCgroup(policy ExecutionPolicy, values ContainerCgroupValues) (ContainerVerifiedLimits, error) {
	if err := validateExecutionPolicy(policy); err != nil {
		return ContainerVerifiedLimits{}, err
	}
	memory, err := parseContainerLimit("memory.max", values.MemoryMax, false)
	if err != nil {
		return ContainerVerifiedLimits{}, err
	}
	if memory != policy.MemoryBytes {
		return ContainerVerifiedLimits{}, fmt.Errorf("memory.max %d does not match agents.execution memory_bytes %d", memory, policy.MemoryBytes)
	}
	swap, err := parseContainerLimit("memory.swap.max", values.MemorySwapMax, true)
	if err != nil {
		return ContainerVerifiedLimits{}, err
	}
	if swap != 0 {
		return ContainerVerifiedLimits{}, fmt.Errorf("memory.swap.max %d does not match the mandatory no-swap limit 0", swap)
	}
	pids, err := parseContainerLimit("pids.max", values.PIDsMax, false)
	if err != nil {
		return ContainerVerifiedLimits{}, err
	}
	if pids != int64(policy.PIDsLimit) {
		return ContainerVerifiedLimits{}, fmt.Errorf("pids.max %d does not match agents.execution pids_limit %d", pids, policy.PIDsLimit)
	}
	quota, period, err := parseContainerCPUFields(values.CPUMax)
	if err != nil {
		return ContainerVerifiedLimits{}, err
	}
	// Compare by division so the exact ratio is checked without multiplying a
	// quota and a period that could overflow int64.
	if quota%period != 0 || quota/period != int64(policy.CPUCount) {
		return ContainerVerifiedLimits{}, fmt.Errorf("cpu.max quota %d and period %d do not express exactly %d CPUs", quota, period, policy.CPUCount)
	}
	return ContainerVerifiedLimits{
		MemoryBytes:     memory,
		MemorySwapBytes: swap,
		CPUCount:        int(quota / period),
		PIDsLimit:       int(pids),
	}, nil
}

// parseContainerCPUFields reads the two-field cpu.max form. "max" is refused
// because an unlimited quota is not a finite bound.
func parseContainerCPUFields(raw string) (int64, int64, error) {
	if len(raw) > maxContainerValueBytes {
		return 0, 0, fmt.Errorf("cpu.max value is %d bytes, above the %d-byte bound", len(raw), maxContainerValueBytes)
	}
	fields := strings.Fields(raw)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("cpu.max %q must contain exactly two fields, quota and period", strings.TrimSpace(raw))
	}
	for _, field := range fields {
		if field == "max" {
			return 0, 0, fmt.Errorf("cpu.max is unlimited (max); the mandatory policy requires a finite CPU quota")
		}
	}
	quota, err := parseContainerLimit("cpu.max quota", fields[0], false)
	if err != nil {
		return 0, 0, err
	}
	period, err := parseContainerLimit("cpu.max period", fields[1], false)
	if err != nil {
		return 0, 0, err
	}
	return quota, period, nil
}

// parseContainerLimit trims one raw cgroup value and parses it strictly as a
// bounded decimal int64. Only digits are accepted, so a sign, exponent or unit
// suffix cannot become a weaker limit through parsing.
func parseContainerLimit(field, raw string, allowZero bool) (int64, error) {
	if len(raw) > maxContainerValueBytes {
		return 0, fmt.Errorf("%s value is %d bytes, above the %d-byte bound", field, len(raw), maxContainerValueBytes)
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("%s is missing", field)
	}
	if value == "max" {
		return 0, fmt.Errorf("%s is unlimited (max); the mandatory policy requires a finite limit", field)
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("%s %q is not a non-negative decimal integer", field, value)
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s %q overflows a signed 64-bit byte count", field, value)
	}
	if parsed > maxContainerLimitValue {
		return 0, fmt.Errorf("%s %d is above the supported limit %d", field, parsed, maxContainerLimitValue)
	}
	if parsed == 0 && !allowZero {
		return 0, fmt.Errorf("%s must be positive, got 0", field)
	}
	return parsed, nil
}
