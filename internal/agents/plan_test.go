package agents

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// plannerConfig is one configuration that exercises every routing source.
func plannerConfig(t *testing.T) Config {
	t.Helper()
	config, err := LoadConfigFile(writeConfig(t, `
agents:
  profiles:
    coder:
      harness: codex
      provider: deepseek
      model: deepseek-v4.1-flash
      effort: medium
      timeout: 45m
    cheap:
      harness: codex
      provider: deepseek
      model: deepseek-v4.1-flash
    op:
      harness: opencode
      model: opencode-go/deepseek-v4.1-flash
      variant: medium
      effort_variants:
        low: cheap
        high: heavy
    ds:
      harness: dsh
      model: deepseek-v4-flash
  task_types:
    coding:
      profile: coder
      complexity:
        high: ds
      worktree: auto
      timeout: 60m
      validate: [go]
    review:
      profile: op
      worktree: current
      acceptance: optional
    broken:
      complexity:
        high: cheap
  validations:
    go:
      - { name: vet, argv: [go, vet, ./...], timeout: 5m }
  defaults:
    task_type: coding
  fallback: [cheap]
`))
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	return config
}

func TestPlanResolvesProfileTaskTypeAndEffortWithProvenance(t *testing.T) {
	t.Parallel()
	config := plannerConfig(t)
	planner := Planner{Config: config}

	plan, err := planner.Plan(RunRequest{Task: "do it", TaskType: "coding"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.TaskType != "coding" || plan.TaskTypeSource != SourceExplicit {
		t.Fatalf("task type = %q/%q", plan.TaskType, plan.TaskTypeSource)
	}
	if plan.Profile.Profile != "coder" || plan.ProfileSource != SourceTaskType {
		t.Fatalf("profile = %q/%q", plan.Profile.Profile, plan.ProfileSource)
	}
	if plan.EffortRequested != "medium" || plan.EffortSource != SourceProfile || !plan.EffortApplied {
		t.Fatalf("effort = %q/%q applied=%v", plan.EffortRequested, plan.EffortSource, plan.EffortApplied)
	}
	if plan.WorktreeMode != ModeAuto || plan.WorktreeSource != SourceTaskType {
		t.Fatalf("worktree = %q/%q", plan.WorktreeMode, plan.WorktreeSource)
	}
	if plan.TimeoutSource != SourceTaskType || time.Duration(plan.TimeoutMS)*time.Millisecond != time.Hour {
		t.Fatalf("timeout = %d/%q", plan.TimeoutMS, plan.TimeoutSource)
	}
	if plan.ValidationSource != SourceTaskType || len(plan.ValidationSteps) != 1 || plan.ValidationSteps[0].Name != "vet" {
		t.Fatalf("validation = %#v/%q", plan.ValidationSteps, plan.ValidationSource)
	}
	if !plan.AcceptanceRequired {
		t.Fatal("coding must require acceptance")
	}
}

func TestPlanHonoursExplicitOverridesAndRecordsComplexityAsOverridden(t *testing.T) {
	t.Parallel()
	config := plannerConfig(t)
	plan, err := (Planner{Config: config}).Plan(RunRequest{
		Task: "do it", TaskType: "coding", Profile: "op", Complexity: "high",
		Effort: "high", WorktreeMode: ModeCurrent, Worktree: "main",
		Timeout: 5 * time.Minute, TimeoutSet: true, NoValidate: true, Acceptance: AcceptanceOptional,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.ProfileSource != SourceExplicit || plan.Profile.Profile != "op" {
		t.Fatalf("profile = %q/%q", plan.Profile.Profile, plan.ProfileSource)
	}
	if plan.Complexity != "high" || !plan.ComplexityOverridden {
		t.Fatalf("complexity = %q overridden=%v", plan.Complexity, plan.ComplexityOverridden)
	}
	if plan.EffortSource != SourceExplicit || plan.EffortEffective != "high" || plan.EffortVariant != "heavy" {
		t.Fatalf("effort = %#v", plan)
	}
	if plan.WorktreeMode != ModeCurrent || plan.WorktreeSource != SourceExplicit {
		t.Fatalf("worktree = %q/%q", plan.WorktreeMode, plan.WorktreeSource)
	}
	if plan.TimeoutSource != SourceExplicit || plan.TimeoutMS != 300000 {
		t.Fatalf("timeout = %d/%q", plan.TimeoutMS, plan.TimeoutSource)
	}
	if len(plan.Validation) != 0 || plan.ValidationSource != SourceOverridden {
		t.Fatalf("validation = %#v/%q", plan.Validation, plan.ValidationSource)
	}
	if plan.AcceptanceRequired {
		t.Fatal("an explicit optional acceptance must win")
	}
}

func TestPlanRoutesByComplexityAndSynthesizesAProfileFromHarnessAndModel(t *testing.T) {
	t.Parallel()
	config := plannerConfig(t)
	plan, err := (Planner{Config: config}).Plan(RunRequest{Task: "do it", TaskType: "coding", Complexity: "high"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.ProfileSource != SourceTaskTypeComplexity || plan.Profile.Profile != "ds" {
		t.Fatalf("profile = %q/%q", plan.Profile.Profile, plan.ProfileSource)
	}

	synthesized, err := (Planner{Config: config}).Plan(RunRequest{
		Task: "do it", TaskType: "coding", Harness: HarnessOpenCode,
		Model: "opencode-go/deepseek-v4.1-flash", Variant: "high",
	})
	if err != nil {
		t.Fatalf("synthesized Plan: %v", err)
	}
	if synthesized.ProfileSource != SourceSynthesized || synthesized.Profile.Harness != HarnessOpenCode {
		t.Fatalf("synthesized profile = %#v", synthesized.Profile)
	}
	if synthesized.Profile.Provider != "opencode-go" || synthesized.Profile.Variant != "high" {
		t.Fatalf("synthesized provider/variant = %#v", synthesized.Profile)
	}
}

func TestPlanFallsBackOnlyForAnInexplicitProfile(t *testing.T) {
	t.Parallel()
	config := plannerConfig(t)
	unavailable := func(resolved Resolved) error {
		if resolved.Profile == "coder" {
			return errors.New("harness missing")
		}
		return nil
	}
	plan, err := (Planner{Config: config, Probe: unavailable}).Plan(RunRequest{Task: "do it", TaskType: "coding"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Profile.Profile != "cheap" || plan.ProfileSource != SourceFallback || plan.FallbackFrom != "coder" {
		t.Fatalf("fallback plan = %#v", plan)
	}

	_, err = (Planner{Config: config, Probe: unavailable}).Plan(RunRequest{Task: "do it", Profile: "coder"})
	if err == nil || !strings.Contains(err.Error(), "harness missing") {
		t.Fatalf("an explicit profile must never be swapped: %v", err)
	}

	broken := plannerConfig(t)
	broken.Fallback = nil
	if _, err := (Planner{Config: broken, Probe: unavailable}).Plan(RunRequest{Task: "do it", TaskType: "coding"}); err == nil {
		t.Fatal("no fallback must surface the probe failure")
	}

	bothFail := func(Resolved) error { return errors.New("all unavailable") }
	if _, err := (Planner{Config: config, Probe: bothFail}).Plan(RunRequest{Task: "do it", TaskType: "coding"}); err == nil || !strings.Contains(err.Error(), "no configured fallback is usable") {
		t.Fatalf("fallback failure = %v", err)
	}
}

func TestPlanRefusesUnsupportedExplicitEffortAndRecordsInheritedOneAsNotApplied(t *testing.T) {
	t.Parallel()
	config := plannerConfig(t)

	// An explicit effort on a harness without an effort seam is a usage error.
	_, err := (Planner{Config: config}).Plan(RunRequest{Task: "x", Profile: "ds", Effort: "high"})
	if err == nil || !IsRequestError(err) || !strings.Contains(err.Error(), "cannot be applied") {
		t.Fatalf("explicit unsupported effort = %v", err)
	}

	// An inherited effort from a task type that routes to dsh is recorded, not fatal.
	config2 := plannerConfig(t)
	taskType := config2.TaskTypes["coding"]
	taskType.Effort = "high"
	config2.TaskTypes["coding"] = taskType
	plan, err := (Planner{Config: config2}).Plan(RunRequest{Task: "x", TaskType: "coding", Complexity: "high"})
	if err != nil {
		t.Fatalf("inherited unsupported effort must not fail: %v", err)
	}
	if plan.EffortApplied || plan.EffortEffective != "" || plan.EffortNote == "" {
		t.Fatalf("inherited effort = %#v", plan)
	}

	// An explicit effort with no variant mapping is refused.
	if _, err := (Planner{Config: config}).Plan(RunRequest{Task: "x", Profile: "op", Effort: "xhigh"}); err == nil || !IsRequestError(err) {
		t.Fatalf("unmapped explicit effort = %v", err)
	}

	// A profile-level effort with no mapping is recorded, not fatal.
	opProfile := config.Profiles["op"]
	opProfile.Effort = "xhigh"
	config.Profiles["op"] = opProfile
	plan, err = (Planner{Config: config}).Plan(RunRequest{Task: "x", Profile: "op"})
	if err != nil {
		t.Fatalf("inherited unmapped effort: %v", err)
	}
	if plan.EffortApplied || plan.EffortNote == "" {
		t.Fatalf("inherited unmapped effort = %#v", plan)
	}
}

func TestPlanRefusesInvalidOrContradictoryRequests(t *testing.T) {
	t.Parallel()
	config := plannerConfig(t)
	cases := []struct {
		name    string
		request RunRequest
		want    string
	}{
		{"profile and harness", RunRequest{Task: "x", Profile: "coder", Harness: HarnessCodex}, "cannot be combined"},
		{"harness without model", RunRequest{Task: "x", Harness: HarnessCodex}, "must be given together"},
		{"unknown task type", RunRequest{Task: "x", TaskType: "nope"}, "unknown task type"},
		{"invalid complexity", RunRequest{Task: "x", TaskType: "coding", Complexity: "huge"}, "is not one of"},
		{"invalid effort", RunRequest{Task: "x", TaskType: "coding", Effort: "max"}, "is not one of"},
		{"invalid worktree", RunRequest{Task: "x", TaskType: "coding", WorktreeMode: "somewhere"}, "must be"},
		{"unknown suite", RunRequest{Task: "x", TaskType: "coding", Validate: []string{"nope"}, ValidateSet: true}, "unknown validation suite"},
		{"validate and no-validate", RunRequest{Task: "x", TaskType: "coding", Validate: []string{"go"}, ValidateSet: true, NoValidate: true}, "mutually exclusive"},
		{"no profile anywhere", RunRequest{Task: "x", TaskType: "broken"}, "names no profile"},
		{"complexity without mapping", RunRequest{Task: "x", TaskType: "broken", Complexity: "low"}, "no profile for complexity"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := (Planner{Config: config}).Plan(testCase.request)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
			if !IsRequestError(err) {
				t.Fatalf("refusal must be a request error: %T", err)
			}
		})
	}
}

func TestPlanUsesDefaultsAndProfileTimeoutWhenNoTaskTypeNamesThem(t *testing.T) {
	t.Parallel()
	config, err := LoadConfigFile(writeConfig(t, `
agents:
  profiles:
    coder:
      harness: codex
      provider: deepseek
      model: deepseek-v4.1-flash
      timeout: 45m
  defaults:
    profile: coder
`))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{Config: config}).Plan(RunRequest{Task: "x"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.TaskType != "coding" || plan.TaskTypeSource != SourceDefault {
		t.Fatalf("task type = %q/%q", plan.TaskType, plan.TaskTypeSource)
	}
	if plan.ProfileSource != SourceDefault {
		t.Fatalf("profile source = %q", plan.ProfileSource)
	}
	if plan.TimeoutSource != SourceProfile || time.Duration(plan.TimeoutMS)*time.Millisecond != 45*time.Minute {
		t.Fatalf("timeout = %d/%q", plan.TimeoutMS, plan.TimeoutSource)
	}
	if plan.ValidationSource != SourceDefault {
		t.Fatalf("validation source = %q", plan.ValidationSource)
	}
}
