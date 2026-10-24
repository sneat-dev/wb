package agents

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Worktree modes for `run`/`compare`. ModeNew and ModeExisting predate them and
// stay supported by `dispatch`.
const (
	ModeAuto    = "auto"
	ModeCurrent = "current"
	ModePath    = "path"
)

// Acceptance policies a task type can declare.
const (
	AcceptanceRequired = "required"
	AcceptanceOptional = "optional"
)

// ProviderDshDefault is the only inference provider the `dsh` compose tree
// names, so it is the only one this build can prove for that harness.
const ProviderDshDefault = "deepseek-official"

// defaultValidationStepTimeout bounds one validation step when the operator
// names no bound of its own.
const defaultValidationStepTimeout = 10 * time.Minute

// complexityLevels is the closed work-difficulty vocabulary. Complexity chooses
// what runs; it is not a reasoning setting.
func complexityLevels() string { return "trivial, low, medium, high, extreme" }

// effortLevels is the closed reasoning vocabulary WB understands. A harness maps
// it to its own setting, or records that it cannot.
func effortLevels() string { return "low, medium, high, xhigh" }

func validComplexity(value string) bool {
	switch value {
	case "trivial", "low", "medium", "high", "extreme":
		return true
	default:
		return false
	}
}

func validEffort(value string) bool {
	switch value {
	case "low", "medium", "high", "xhigh":
		return true
	default:
		return false
	}
}

func supportedHarness(name string) bool {
	for _, harness := range supportedHarnesses() {
		if harness == name {
			return true
		}
	}
	return false
}

// AllHarnesses is the ordered list of harness names this build can launch.
func AllHarnesses() []string { return supportedHarnesses() }

// BuiltinTaskTypeNames reports which task types come from WB rather than from
// the operator's configuration.
func BuiltinTaskTypeNames() map[string]bool {
	names := make(map[string]bool)
	for name := range builtinTaskTypes() {
		names[name] = true
	}
	return names
}

// NewRequestError marks an error as a refused invocation (exit 2) rather than a
// finding, for callers outside this package.
func NewRequestError(message string) error { return requestErrorf("%s", message) }

// builtinTaskTypes are policy only: they name no profile and no model, so a
// project still decides which configuration executes. They exist so
// `--task-type coding` works without a task_types block.
func builtinTaskTypes() map[string]TaskType {
	return map[string]TaskType{
		"coding":        {Worktree: ModeAuto, Acceptance: AcceptanceRequired},
		"debugging":     {Worktree: ModeAuto, Acceptance: AcceptanceRequired},
		"testing":       {Worktree: ModeAuto, Acceptance: AcceptanceRequired},
		"review":        {Worktree: ModeCurrent, Acceptance: AcceptanceRequired},
		"research":      {Worktree: ModeCurrent, Acceptance: AcceptanceOptional},
		"documentation": {Worktree: ModeAuto, Acceptance: AcceptanceOptional},
	}
}

// TaskTypes returns every task type, built-in or configured, in name order.
func (config Config) TaskTypesInOrder() []string {
	names := make([]string, 0, len(config.TaskTypes))
	for name := range config.TaskTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ProfileNames returns every configured profile in name order.
func (config Config) ProfileNames() []string {
	names := make([]string, 0, len(config.Profiles))
	for name := range config.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ProviderNames returns every provider (built-in or overridden) in name order.
func (config Config) ProviderNames() []string {
	names := make([]string, 0, len(config.Providers))
	for name := range config.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// parseStepTimeout turns a configured duration into a value, treating an absent
// value as "no bound configured" rather than as zero seconds.
func parseStepTimeout(value string) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	parsed, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, fmt.Errorf("duration %q is not a Go duration such as 45m: %w", value, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("duration %q must be positive", value)
	}
	return parsed, nil
}

// flattenValidation turns suite names into the exact ordered steps a run will
// execute. The steps are snapshotted into the run record at dispatch, so a
// worker that edits the configuration mid-run cannot change what was validated.
func (config Config) flattenValidation(suites []string) ([]ValidationStep, error) {
	steps := make([]ValidationStep, 0, len(suites))
	for _, suite := range suites {
		configured, ok := config.Validations[suite]
		if !ok {
			return nil, requestErrorf("unknown validation suite %q; configured suites: %s", suite, config.validationNames())
		}
		steps = append(steps, configured...)
	}
	return steps, nil
}

func (config Config) validationNames() string {
	names := make([]string, 0, len(config.Validations))
	for name := range config.Validations {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "(none configured)"
	}
	return strings.Join(names, ", ")
}
