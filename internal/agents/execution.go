package agents

import (
	"encoding/hex"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ExecutionModeLinuxContainer is the only mandatory sandbox mode this WB build
// knows. A mode it does not know is refused rather than approximated by a
// weaker one.
const ExecutionModeLinuxContainer = "linux-container"

// supportedExecutionFields is the complete accepted surface of
// agents.execution. It exists to be printed, and to be checked against the
// struct by test, so an arbitrary runtime argument, mount or security override
// has no way to appear in configuration and be silently dropped.
const supportedExecutionFields = "mode, image, memory_bytes, memory_swap_bytes, cpu_count, pids_limit and max_parallel"

// ExecutionPolicy is the operator's mandatory execution sandbox, declared once
// at the top level of the user's wb.yaml under agents.execution.
//
// It is deliberately independent of profiles, task types and command-line
// overrides: nothing below a run request can reach these fields, and there is
// no field here that lowers, replaces or disables another one. Absent means the
// operator declared no mandatory sandbox and WB keeps its existing host
// behaviour; present means every field must be usable exactly as written.
type ExecutionPolicy struct {
	// Mode is the sandbox kind. Only linux-container is accepted.
	Mode string `yaml:"mode" json:"mode"`
	// Image is an immutable container image identity: a reference pinned by a
	// sha256 digest, or a sha256 image ID. A mutable tag such as :latest is
	// refused because it can change between two runs.
	Image string `yaml:"image" json:"image"`
	// MemoryBytes is the hard memory limit the container runtime must apply.
	MemoryBytes int64 `yaml:"memory_bytes" json:"memory_bytes"`
	// MemorySwapBytes is the *total* memory-plus-swap limit. Mandatory policy
	// requires it to equal MemoryBytes, so a run can never borrow swap beyond
	// its memory bound.
	MemorySwapBytes int64 `yaml:"memory_swap_bytes" json:"memory_swap_bytes"`
	// CPUCount is the number of CPUs the sandbox may use.
	CPUCount int `yaml:"cpu_count" json:"cpu_count"`
	// PIDsLimit bounds the processes one sandbox may create.
	PIDsLimit int `yaml:"pids_limit" json:"pids_limit"`
	// MaxParallel is how many container executions may run at once. This build
	// supports exactly one.
	MaxParallel int `yaml:"max_parallel" json:"max_parallel"`
}

// executionPolicyFields is ExecutionPolicy without its YAML method, so the
// strict decoder can decode into it without recursing.
type executionPolicyFields ExecutionPolicy

// UnmarshalYAML refuses an unsupported key instead of ignoring it. Arbitrary
// runtime arguments, mounts and security options are exactly what must never
// reach the container runtime through a policy, and a silently dropped key
// would make a weakened policy look accepted.
func (policy *ExecutionPolicy) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.MappingNode {
		for index := 0; index+1 < len(node.Content); index += 2 {
			field := node.Content[index].Value
			if !supportedExecutionField(field) {
				return fmt.Errorf("agents.execution: field %q is not supported; accepted fields are %s", field, supportedExecutionFields)
			}
		}
	}
	var decoded executionPolicyFields
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*policy = ExecutionPolicy(decoded)
	return nil
}

func supportedExecutionField(field string) bool {
	switch field {
	case "mode", "image", "memory_bytes", "memory_swap_bytes", "cpu_count", "pids_limit", "max_parallel":
		return true
	default:
		return false
	}
}

// validateExecutionPolicy refuses a policy that cannot be enforced exactly as
// written. Every refusal names the field, because a mandatory bound the
// operator believes is active but WB quietly rounded is worse than no bound.
func validateExecutionPolicy(policy ExecutionPolicy) error {
	if policy.Mode == "" {
		return fmt.Errorf("agents.execution: mode is required; the only supported mode is %q", ExecutionModeLinuxContainer)
	}
	if policy.Mode != ExecutionModeLinuxContainer {
		return fmt.Errorf("agents.execution: mode %q is not supported; this WB build supports only %q", policy.Mode, ExecutionModeLinuxContainer)
	}
	if err := validateExecutionImage(policy.Image); err != nil {
		return err
	}
	switch {
	case policy.MemoryBytes <= 0:
		return fmt.Errorf("agents.execution: memory_bytes must be a positive byte count, got %d", policy.MemoryBytes)
	case policy.MemorySwapBytes <= 0:
		return fmt.Errorf("agents.execution: memory_swap_bytes must be a positive byte count, got %d", policy.MemorySwapBytes)
	case policy.MemorySwapBytes != policy.MemoryBytes:
		return fmt.Errorf("agents.execution: memory_swap_bytes %d must equal memory_bytes %d; this policy permits no swap beyond the memory limit", policy.MemorySwapBytes, policy.MemoryBytes)
	case policy.CPUCount <= 0:
		return fmt.Errorf("agents.execution: cpu_count must be a positive CPU count, got %d", policy.CPUCount)
	case policy.PIDsLimit <= 0:
		return fmt.Errorf("agents.execution: pids_limit must be a positive process count, got %d", policy.PIDsLimit)
	case policy.MaxParallel <= 0:
		return fmt.Errorf("agents.execution: max_parallel must be a positive concurrency bound, got %d", policy.MaxParallel)
	case policy.MaxParallel != 1:
		return fmt.Errorf("agents.execution: max_parallel %d is unsupported; this policy runs exactly one container execution at a time", policy.MaxParallel)
	}
	return nil
}

// validateExecutionImage accepts only an immutable image identity. The digest
// is checked here rather than left to the runtime, so a mutable tag fails at
// configuration time, before any worktree exists.
func validateExecutionImage(image string) error {
	if image == "" {
		return fmt.Errorf("agents.execution: image is required and must be an immutable sha256 digest reference")
	}
	if strings.ContainsAny(image, " \t\r\n") {
		return fmt.Errorf("agents.execution: image %q must not contain whitespace", image)
	}
	digest := ""
	if index := strings.LastIndex(image, "@"); index >= 0 {
		if index == 0 {
			return fmt.Errorf("agents.execution: image %q has no repository before its digest", image)
		}
		digest = image[index+1:]
	} else if strings.HasPrefix(image, "sha256:") {
		digest = image
	}
	if digest == "" {
		return fmt.Errorf("agents.execution: image %q is a mutable tag; pin an immutable sha256 digest (name@sha256:<64 hex> or sha256:<64 hex>)", image)
	}
	algorithm, hash, found := strings.Cut(digest, ":")
	if !found || algorithm != "sha256" {
		return fmt.Errorf("agents.execution: image %q must be pinned by a sha256 digest", image)
	}
	if len(hash) != 64 || strings.ToLower(hash) != hash {
		return fmt.Errorf("agents.execution: image %q has an invalid sha256 digest; expected 64 lowercase hex characters", image)
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return fmt.Errorf("agents.execution: image %q has an invalid sha256 digest; expected 64 lowercase hex characters", image)
	}
	return nil
}

// snapshotExecution copies a configured policy so a plan or record keeps the
// exact values that were resolved, even if the caller's configuration value is
// later reused or mutated.
func snapshotExecution(policy *ExecutionPolicy) *ExecutionPolicy {
	if policy == nil {
		return nil
	}
	snapshot := *policy
	return &snapshot
}

// requireRunnableExecution is the admission gate for the mandatory policy. The
// user's configuration is the authority, so a plan that does not carry exactly
// the policy this process loaded is refused rather than trusted: a hand-built or
// stale plan must not become a way to run outside the sandbox. Equality is
// value equality, because the plan's snapshot is what the record will claim.
//
// While container execution is unimplemented, a matching policy also refuses:
// the operator marked the sandbox mandatory, and running the harness on the host
// instead would silently weaken it.
func requireRunnableExecution(configured, planned *ExecutionPolicy, harness string) error {
	switch {
	case configured == nil && planned == nil:
		return nil
	case configured != nil && planned == nil:
		return fmt.Errorf("agents.execution is configured but this plan carries no mandatory policy snapshot; resolve the plan again before running")
	case configured == nil:
		return fmt.Errorf("this plan was resolved under a mandatory agents.execution policy that the user configuration no longer declares; resolve the plan again before running")
	case *configured != *planned:
		return fmt.Errorf("agents.execution changed after this plan was resolved; resolve the plan again before running")
	default:
		return fmt.Errorf("agents.execution mode %q is configured but this WB build cannot execute the %q harness inside that sandbox yet; refusing to run it outside the mandatory sandbox", configured.Mode, harness)
	}
}
