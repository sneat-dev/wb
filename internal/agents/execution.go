package agents

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/sneat-dev/wb/internal/wbconfig"
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

// Bounded syntax for an immutable image identity. The module has no OCI
// reference parser dependency, so these patterns carry the grammar explicitly:
// a lowercase registry/repository path, an optional Docker tag, and a sha256
// digest or bare sha256 image ID. They are intentionally narrower than every
// string a container runtime would accept, because a mandatory policy must be
// enforceable exactly as written.
var (
	imageDigestHashPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	imageTagPattern          = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	imageRegistryLabel       = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	imageRegistryPort        = regexp.MustCompile(`^[0-9]{1,5}$`)
	imageRepositoryComponent = regexp.MustCompile(`^[a-z0-9]+(?:[._-]+[a-z0-9]+)*$`)
)

// validateExecutionImage accepts only an immutable image identity with a valid
// repository prefix. The syntax is checked here rather than left to the
// runtime, so a leading "-", a control character, a second "@" or a malformed
// path fails at configuration time, before any worktree exists.
func validateExecutionImage(image string) error {
	if image == "" {
		return fmt.Errorf("agents.execution: image is required and must be an immutable sha256 digest reference")
	}
	if strings.ContainsAny(image, " \t\r\n") {
		return fmt.Errorf("agents.execution: image %q must not contain whitespace", image)
	}
	for _, character := range image {
		if character < 0x21 || character > 0x7e {
			return fmt.Errorf("agents.execution: image %q must contain only printable ASCII characters; control characters and non-ASCII bytes are not allowed", image)
		}
	}
	if strings.Count(image, "@") > 1 {
		return fmt.Errorf("agents.execution: image %q must contain at most one @ digest separator", image)
	}
	if index := strings.Index(image, "@"); index >= 0 {
		if index == 0 {
			return fmt.Errorf("agents.execution: image %q has no repository before its digest", image)
		}
		if err := validateImageDigest(image, image[index+1:]); err != nil {
			return err
		}
		return validateImageRepository(image, image[:index])
	}
	if strings.HasPrefix(image, "sha256:") {
		return validateImageDigest(image, image)
	}
	return fmt.Errorf("agents.execution: image %q is a mutable tag; pin an immutable sha256 digest (name@sha256:<64 hex> or sha256:<64 hex>)", image)
}

// validateImageDigest checks the algorithm and the exact 64 lowercase hex
// characters. A short, uppercase or non-hex value is not a digest this policy
// can pin.
func validateImageDigest(image, digest string) error {
	algorithm, hash, found := strings.Cut(digest, ":")
	if !found || algorithm != "sha256" {
		return fmt.Errorf("agents.execution: image %q must be pinned by a sha256 digest", image)
	}
	if !imageDigestHashPattern.MatchString(hash) {
		return fmt.Errorf("agents.execution: image %q has an invalid sha256 digest; expected 64 lowercase hex characters", image)
	}
	return nil
}

// validateImageRepository bounds the repository part: an optional lowercase
// registry host with numeric port, lowercase path components, and an optional
// tag. A component that starts with "-" or contains an empty, "." or ".."
// segment is refused rather than handed to a runtime that might reinterpret it.
func validateImageRepository(image, name string) error {
	repository := name
	tag := ""
	if index := strings.LastIndexByte(repository, ':'); index > strings.LastIndexByte(repository, '/') {
		tag = repository[index+1:]
		repository = repository[:index]
		if !imageTagPattern.MatchString(tag) {
			return fmt.Errorf("agents.execution: image %q has invalid tag %q; expected 1-128 characters from [A-Za-z0-9_.-] starting with an alphanumeric or underscore", image, tag)
		}
	}
	components := strings.Split(repository, "/")
	for _, component := range components {
		if component == "" {
			return fmt.Errorf("agents.execution: image %q contains an empty repository component", image)
		}
	}
	first := 0
	if len(components) > 1 && strings.ContainsAny(components[0], ".:") {
		if err := validateImageRegistry(image, components[0]); err != nil {
			return err
		}
		first = 1
	}
	for _, component := range components[first:] {
		if !imageRepositoryComponent.MatchString(component) {
			return fmt.Errorf("agents.execution: image %q has invalid repository component %q; expected lowercase alphanumeric components separated by [._-]", image, component)
		}
	}
	return nil
}

// validateImageRegistry checks the optional registry prefix. IPv6 literals are
// outside this bounded grammar: the mandatory policy does not need one, and
// accepting a form this checker cannot validate would be a silent bypass.
func validateImageRegistry(image, registry string) error {
	host, port, found := strings.Cut(registry, ":")
	if found && !imageRegistryPort.MatchString(port) {
		return fmt.Errorf("agents.execution: image %q has invalid registry port %q; expected 1-5 digits", image, port)
	}
	if host == "" {
		return fmt.Errorf("agents.execution: image %q has an empty registry host", image)
	}
	for _, label := range strings.Split(host, ".") {
		if !imageRegistryLabel.MatchString(label) {
			return fmt.Errorf("agents.execution: image %q has invalid registry %q; expected a lowercase host with an optional numeric port", image, registry)
		}
	}
	return nil
}

// SnapshotExecution copies a configured policy so a plan or record keeps the
// exact values that were resolved, even if the caller's configuration value is
// later reused or mutated.
func SnapshotExecution(policy *ExecutionPolicy) *ExecutionPolicy {
	if policy == nil {
		return nil
	}
	snapshot := *policy
	return &snapshot
}

// LoadDefaultExecutionPolicy reads the operator's authoritative mandatory
// execution policy from the user's wb.yaml. A missing configuration declares no
// policy and is not an error; a configuration that cannot be read is, because
// the owner must not fall back to "no policy" on a broken file.
func LoadDefaultExecutionPolicy() (*ExecutionPolicy, error) {
	config, err := LoadConfigFile(wbconfig.DefaultPath())
	if err != nil {
		return nil, err
	}
	return SnapshotExecution(config.Execution), nil
}

// RequireRunnableExecution is the single admission authority for the mandatory
// sandbox policy. Every host entry point calls it rather than restating the
// rules: plan admission, the run planner's preflight boundary, runtime failover
// and the detached run owner.
//
// The user's configuration is the authority, so a snapshot that does not carry
// exactly the policy this process loaded is refused rather than trusted: a
// hand-built or stale plan must not become a way to run outside the sandbox.
// Equality is value equality, because the snapshot is what the record claims.
// The subject names the document being admitted so a refusal points at the
// right one.
//
// While container execution is unimplemented, a matching policy also refuses:
// the operator marked the sandbox mandatory, and running the harness on the host
// instead would silently weaken it.
func RequireRunnableExecution(configured, snapshot *ExecutionPolicy, subject, harness string) error {
	switch {
	case configured == nil && snapshot == nil:
		return nil
	case configured != nil && snapshot == nil:
		return fmt.Errorf("agents.execution is configured but this %s carries no mandatory policy snapshot; resolve it again before running", subject)
	case configured == nil:
		return fmt.Errorf("this %s was resolved under a mandatory agents.execution policy that the user configuration no longer declares; resolve it again before running", subject)
	case *configured != *snapshot:
		return fmt.Errorf("agents.execution changed after this %s was resolved; resolve it again before running", subject)
	default:
		return fmt.Errorf("agents.execution mode %q is configured but this WB build cannot execute the %q harness inside that sandbox yet; refusing to run it outside the mandatory sandbox", configured.Mode, harness)
	}
}
