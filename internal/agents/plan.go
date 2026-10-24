package agents

import (
	"fmt"
	"strings"
	"time"
)

// Value sources recorded in a plan. They are the audit trail that answers "why
// did this run use this model", which a resolved snapshot alone cannot.
const (
	SourceExplicit           = "explicit"
	SourceTaskType           = "task_type"
	SourceTaskTypeComplexity = "task_type_complexity"
	SourceProfile            = "profile"
	SourceDefault            = "default"
	SourceSynthesized        = "synthesized"
	SourceOverridden         = "overridden"
	SourceFallback           = "fallback"
	SourceNone               = "none"
)

// RunRequest is everything a caller can decide about one execution. Every field
// is optional: the point of a task type is that the caller can name the work and
// nothing else.
type RunRequest struct {
	Task string

	TaskType   string
	Complexity string
	Effort     string

	Profile  string
	Harness  string
	Model    string
	Provider string
	Variant  string

	// WorktreeMode is auto, current, path, new or existing. Worktree is an
	// operation name, an absolute path, or an existing worktree name depending
	// on the mode.
	WorktreeMode   string
	Worktree       string
	AllowCanonical bool
	AllowDirty     bool

	Timeout    time.Duration
	TimeoutSet bool

	Validate    []string
	ValidateSet bool
	NoValidate  bool

	Repository string
	Base       string
	Branch     string

	// Acceptance overrides the task type's acceptance policy when set.
	Acceptance string
}

// Plan is one fully resolved execution. It is the `--dry-run` document and the
// provenance snapshot a run record keeps for each resolved value.
type Plan struct {
	TaskType       string   `json:"task_type"`
	TaskTypeSource string   `json:"task_type_source"`
	Profile        Resolved `json:"profile"`
	ProfileSource  string   `json:"profile_source"`

	Complexity           string `json:"complexity,omitempty"`
	ComplexitySource     string `json:"complexity_source,omitempty"`
	ComplexityOverridden bool   `json:"complexity_overridden,omitempty"`

	EffortRequested string `json:"effort_requested,omitempty"`
	EffortSource    string `json:"effort_source,omitempty"`
	EffortEffective string `json:"effort_effective,omitempty"`
	EffortApplied   bool   `json:"effort_applied"`
	EffortVariant   string `json:"effort_variant,omitempty"`
	EffortNote      string `json:"effort_note,omitempty"`

	WorktreeMode   string `json:"worktree_mode"`
	WorktreeSource string `json:"worktree_source"`

	TimeoutMS     int64  `json:"timeout_ms"`
	TimeoutSource string `json:"timeout_source"`

	Validation       []string         `json:"validation,omitempty"`
	ValidationSource string           `json:"validation_source"`
	ValidationSteps  []ValidationStep `json:"validation_steps,omitempty"`

	AcceptanceRequired bool `json:"acceptance_required"`

	// FallbackFrom names the profile the plan would have used when preflight
	// substituted a configured fallback. Empty means no substitution happened.
	FallbackFrom string `json:"fallback_from,omitempty"`

	// probed records that this plan already passed a preflight, so a caller
	// does not probe the same harness twice. It is never serialized.
	probed bool
}

// Planner resolves requests against one configuration. Probe is the preflight a
// caller can supply — harness present, credential resolvable — and is the only
// reason a plan can fall back to another configured profile.
type Planner struct {
	Config Config
	Probe  func(Resolved) error
}

// Plan resolves one request, failing closed on anything it cannot execute.
func (planner Planner) Plan(request RunRequest) (Plan, error) {
	config := planner.Config
	plan := Plan{}

	taskType, taskTypeName, taskTypeSource, err := config.resolveTaskType(request.TaskType)
	if err != nil {
		return Plan{}, err
	}
	plan.TaskType, plan.TaskTypeSource = taskTypeName, taskTypeSource

	resolved, profileSource, err := config.resolveRunProfile(request, taskType, taskTypeName)
	if err != nil {
		return Plan{}, err
	}
	plan.Profile, plan.ProfileSource = resolved, profileSource

	complexity, complexitySource, err := resolveComplexity(request.Complexity)
	if err != nil {
		return Plan{}, err
	}
	plan.Complexity, plan.ComplexitySource = complexity, complexitySource
	if profileSource == SourceExplicit || profileSource == SourceSynthesized {
		if complexity != "" {
			plan.ComplexityOverridden = true
		}
	}

	if err := planner.resolveEffort(&plan, request, resolved, taskType); err != nil {
		return Plan{}, err
	}

	mode, modeSource, err := resolveWorktreeMode(request.WorktreeMode, taskType)
	if err != nil {
		return Plan{}, err
	}
	plan.WorktreeMode, plan.WorktreeSource = mode, modeSource

	timeout, timeoutSource := resolveTimeout(request, taskType, resolved)
	plan.TimeoutMS, plan.TimeoutSource = timeout.Milliseconds(), timeoutSource

	validation, validationSource, err := config.resolveValidation(request, taskType)
	if err != nil {
		return Plan{}, err
	}
	plan.Validation, plan.ValidationSource = validation, validationSource
	// resolveValidation already validated every suite name, so flattening here
	// cannot fail.
	steps, _ := config.flattenValidation(validation)
	plan.ValidationSteps = steps

	plan.AcceptanceRequired = resolveAcceptance(request.Acceptance, taskType)

	if planner.Probe != nil {
		plan.probed = true
		if probeErr := planner.Probe(plan.Profile); probeErr != nil {
			// An explicitly chosen profile is never swapped for another one: the
			// operator asked for that configuration. Every other route may use
			// the ordered fallback list, which is itself explicit configuration.
			if plan.ProfileSource == SourceExplicit || plan.ProfileSource == SourceSynthesized || len(config.Fallback) == 0 {
				return Plan{}, probeErr
			}
			fallback, fallbackErr := planner.firstUsableFallback()
			if fallbackErr != nil {
				return Plan{}, fmt.Errorf("%v; no configured fallback is usable: %w", probeErr, fallbackErr)
			}
			plan.FallbackFrom = plan.Profile.Profile
			plan.Profile, plan.ProfileSource = fallback, SourceFallback
			// Only an explicit effort can be refused, and an explicit profile is
			// never swapped, so a fallback can only carry an inherited effort.
			_ = planner.resolveEffort(&plan, request, fallback, taskType)
		}
	}
	return plan, nil
}

func (planner Planner) firstUsableFallback() (Resolved, error) {
	var failures []string
	for _, name := range planner.Config.Fallback {
		resolved, err := planner.Config.Resolve(name)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		if err := planner.Probe(resolved); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		return resolved, nil
	}
	return Resolved{}, fmt.Errorf("%s", strings.Join(failures, "; "))
}

func (config Config) resolveTaskType(requested string) (TaskType, string, string, error) {
	name := strings.TrimSpace(requested)
	source := SourceExplicit
	if name == "" {
		name = strings.TrimSpace(config.Defaults.TaskType)
		source = SourceTaskType
	}
	if name == "" {
		name = "coding"
		source = SourceDefault
	}
	taskType, ok := config.TaskTypes[name]
	if !ok {
		// A configuration built without an explicit task_types block still gets
		// WB's policy-only built-ins, so `dispatch --profile` and a minimal test
		// configuration do not have to declare them.
		taskType, ok = builtinTaskTypes()[name]
	}
	if !ok {
		return TaskType{}, "", "", requestErrorf("unknown task type %q; configured task types: %s", name, strings.Join(config.TaskTypesInOrder(), ", "))
	}
	return taskType, name, source, nil
}

func (config Config) resolveRunProfile(request RunRequest, taskType TaskType, taskTypeName string) (Resolved, string, error) {
	explicit := strings.TrimSpace(request.Profile)
	harness := strings.TrimSpace(request.Harness)
	model := strings.TrimSpace(request.Model)
	if explicit != "" && (harness != "" || model != "") {
		return Resolved{}, "", requestErrorf("--profile cannot be combined with --harness or --model; name the configuration one way")
	}
	switch {
	case explicit != "":
		resolved, err := config.Resolve(explicit)
		return resolved, SourceExplicit, err
	case harness != "" || model != "":
		if harness == "" || model == "" {
			return Resolved{}, "", requestErrorf("--harness and --model must be given together")
		}
		profile := Profile{Harness: harness, Provider: request.Provider, Model: model, Variant: request.Variant}
		resolved, err := config.resolveProfile("(command line)", profile)
		return resolved, SourceSynthesized, err
	}
	complexity := strings.TrimSpace(request.Complexity)
	if complexity != "" {
		if profile, ok := taskType.Complexity[complexity]; ok {
			resolved, err := config.Resolve(profile)
			return resolved, SourceTaskTypeComplexity, err
		}
	}
	if taskType.Profile != "" {
		resolved, err := config.Resolve(taskType.Profile)
		return resolved, SourceTaskType, err
	}
	if config.Defaults.Profile != "" {
		resolved, err := config.Resolve(config.Defaults.Profile)
		return resolved, SourceDefault, err
	}
	if complexity != "" {
		return Resolved{}, "", requestErrorf("task type %q has no profile for complexity %q and names no default profile; configure agents.task_types.%s.profile, agents.defaults.profile, or pass --profile", taskTypeName, complexity, taskTypeName)
	}
	return Resolved{}, "", requestErrorf("task type %q names no profile; configure agents.task_types.%s.profile or agents.defaults.profile, or pass --profile (configured profiles: %s)", taskTypeName, taskTypeName, config.profileNames())
}

func resolveComplexity(requested string) (string, string, error) {
	value := strings.TrimSpace(requested)
	if value == "" {
		return "", SourceNone, nil
	}
	if !validComplexity(value) {
		return "", "", requestErrorf("--task-complexity %q is not one of %s", value, complexityLevels())
	}
	return value, SourceExplicit, nil
}

// resolveEffort decides what effort was requested, whether this harness can
// apply it, and what that means for the run. An explicit request that cannot be
// honoured is a usage error; an inherited one is recorded as not applied so a
// task-type default never turns a valid route into a failure.
func (planner Planner) resolveEffort(plan *Plan, request RunRequest, resolved Resolved, taskType TaskType) error {
	plan.EffortRequested, plan.EffortSource = "", SourceNone
	if explicit := strings.TrimSpace(request.Effort); explicit != "" {
		if !validEffort(explicit) {
			return requestErrorf("--effort %q is not one of %s", explicit, effortLevels())
		}
		plan.EffortRequested, plan.EffortSource = explicit, SourceExplicit
	} else if inherited := strings.TrimSpace(taskType.Effort); inherited != "" {
		plan.EffortRequested, plan.EffortSource = inherited, SourceTaskType
	} else if inherited := strings.TrimSpace(resolved.Effort); inherited != "" {
		plan.EffortRequested, plan.EffortSource = inherited, SourceProfile
	}
	plan.EffortEffective, plan.EffortApplied, plan.EffortVariant, plan.EffortNote = "", false, "", ""

	requested := plan.EffortRequested
	if requested == "" {
		return nil
	}
	capability := HarnessCapabilityFor(resolved.Harness)
	switch capability {
	case EffortConfig:
		plan.EffortEffective, plan.EffortApplied = requested, true
	case EffortVariant:
		variant := resolved.EffortVariants[requested]
		if variant == "" {
			if plan.EffortSource == SourceExplicit {
				return requestErrorf("profile %q has no effort_variants entry for %q, so the %q harness cannot apply it; configure agents.profiles.%s.effort_variants.%s or drop --effort", resolved.Profile, requested, resolved.Harness, resolved.Profile, requested)
			}
			plan.EffortNote = fmt.Sprintf("effort %q inherited from %s was not applied: profile %q has no effort_variants for it", requested, plan.EffortSource, resolved.Profile)
			return nil
		}
		plan.EffortEffective, plan.EffortApplied, plan.EffortVariant = requested, true, variant
	case EffortUnsupported:
		if plan.EffortSource == SourceExplicit {
			return requestErrorf("--effort %q cannot be applied by the %q harness; drop --effort or choose a profile whose harness supports it", requested, resolved.Harness)
		}
		plan.EffortNote = fmt.Sprintf("effort %q inherited from %s was not applied: the %q harness has no effort setting", requested, plan.EffortSource, resolved.Harness)
	}
	return nil
}

func resolveWorktreeMode(requested string, taskType TaskType) (string, string, error) {
	mode := strings.TrimSpace(requested)
	if mode == "" {
		if configured := strings.TrimSpace(taskType.Worktree); configured != "" {
			return configured, SourceTaskType, nil
		}
		return ModeAuto, SourceDefault, nil
	}
	switch mode {
	case ModeAuto, ModeCurrent, ModePath, ModeNew, ModeExisting:
		return mode, SourceExplicit, nil
	default:
		return "", "", requestErrorf("--worktree %q must be %q, %q, an absolute path, or use --new-worktree/--use-worktree", mode, ModeAuto, ModeCurrent)
	}
}

func resolveTimeout(request RunRequest, taskType TaskType, resolved Resolved) (time.Duration, string) {
	if request.TimeoutSet && request.Timeout > 0 {
		return request.Timeout, SourceExplicit
	}
	if configured, err := parseStepTimeout(taskType.Timeout); err == nil && configured > 0 {
		return configured, SourceTaskType
	}
	if resolved.TimeoutMS > 0 {
		return time.Duration(resolved.TimeoutMS) * time.Millisecond, SourceProfile
	}
	return DefaultTimeout, SourceDefault
}

func (config Config) resolveValidation(request RunRequest, taskType TaskType) ([]string, string, error) {
	switch {
	case request.NoValidate && request.ValidateSet:
		return nil, "", requestErrorf("--validate and --no-validate are mutually exclusive")
	case request.NoValidate:
		return nil, SourceOverridden, nil
	case request.ValidateSet:
		if _, err := config.flattenValidation(request.Validate); err != nil {
			return nil, "", err
		}
		return append([]string(nil), request.Validate...), SourceExplicit, nil
	case len(taskType.Validate) > 0:
		return append([]string(nil), taskType.Validate...), SourceTaskType, nil
	default:
		return nil, SourceDefault, nil
	}
}

func resolveAcceptance(requested string, taskType TaskType) bool {
	switch strings.TrimSpace(requested) {
	case AcceptanceOptional:
		return false
	case AcceptanceRequired:
		return true
	}
	return strings.TrimSpace(taskType.Acceptance) != AcceptanceOptional
}
