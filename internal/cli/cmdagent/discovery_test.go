package cmdagent

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
)

func discoveryDependencies() Dependencies {
	deps := fakeDependencies()
	deps.Profiles = func(context.Context, agentrun.DiscoveryRequest) (agentrun.ProfilesCatalog, error) {
		return agentrun.ProfilesCatalog{
			Profiles: []agentrun.ProfileEntry{
				{Name: "op", Usable: true, Resolved: agents.Resolved{Profile: "op", Harness: "opencode", Provider: "opencode-go", Model: "opencode-go/m", Variant: "high", Effort: "medium"}},
				{Name: "broken", Problem: "harness nope is not supported"},
			},
			Providers:      []string{"deepseek", "openrouter"},
			Fallback:       []string{"op"},
			DefaultProfile: "op",
			Harnesses:      []agents.HarnessCapabilities{{Harness: "opencode", Effort: "variant", TokenUsage: "events", Cost: "harness-estimated", TaskDelivery: "stdin", Isolation: "user-home"}},
		}, nil
	}
	deps.TaskTypes = func(context.Context, agentrun.DiscoveryRequest) (agentrun.TaskTypesCatalog, error) {
		return agentrun.TaskTypesCatalog{
			Default: "coding",
			TaskTypes: []agentrun.TaskTypeEntry{
				{Name: "coding", Builtin: false, Policy: agents.TaskType{Profile: "op", Worktree: "auto", Timeout: "60m", Validate: []string{"go"}, Acceptance: "required"}},
				{Name: "research", Builtin: true},
			},
		}, nil
	}
	return deps
}

func TestProfilesRendersConfiguredProfilesAndCapabilities(t *testing.T) {
	t.Parallel()
	code, out, errout := runFake(t, discoveryDependencies(), "agent", "profiles")
	if code != exitOK {
		t.Fatalf("code = %d stderr=%s", code, errout)
	}
	for _, want := range []string{"PROFILE", "opencode-go/m#high", "unusable: harness nope", "providers: deepseek, openrouter", "default profile: op", "fallback: op", "harness opencode", "isolation=user-home"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q is missing %q", out, want)
		}
	}
}

func TestProfilesReportAnEmptyConfiguration(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.Profiles = func(context.Context, agentrun.DiscoveryRequest) (agentrun.ProfilesCatalog, error) {
		return agentrun.ProfilesCatalog{Providers: []string{"deepseek"}}, nil
	}
	code, out, _ := runFake(t, deps, "agent", "profiles")
	if code != exitOK || !strings.Contains(out, "no agent profiles configured") {
		t.Fatalf("code = %d out=%q", code, out)
	}
}

func TestProfilesJSONIsTheMachineReadableCatalog(t *testing.T) {
	t.Parallel()
	code, out, _ := runFake(t, discoveryDependencies(), "agent", "profiles", "--json")
	if code != exitOK || !strings.Contains(out, `"providers"`) || !strings.Contains(out, `"harnesses"`) {
		t.Fatalf("code = %d out=%q", code, out)
	}
}

func TestTaskTypesRendersPolicyAndDefault(t *testing.T) {
	t.Parallel()
	code, out, errout := runFake(t, discoveryDependencies(), "agent", "task-types")
	if code != exitOK {
		t.Fatalf("code = %d stderr=%s", code, errout)
	}
	for _, want := range []string{"TASK TYPE", "configured", "coding", "built-in", "research", "default task type: coding"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q is missing %q", out, want)
		}
	}
	code, out, _ = runFake(t, discoveryDependencies(), "agent", "task-types", "--format", "json")
	if code != exitOK || !strings.Contains(out, `"task_types"`) {
		t.Fatalf("json code = %d out=%q", code, out)
	}
}

func TestDiscoverySurfacesServiceFailures(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.Profiles = func(context.Context, agentrun.DiscoveryRequest) (agentrun.ProfilesCatalog, error) {
		return agentrun.ProfilesCatalog{}, errors.New("config unreadable")
	}
	if code, _, _ := runFake(t, deps, "agent", "profiles"); code != exitFindings {
		t.Fatalf("code = %d", code)
	}
	deps.TaskTypes = func(context.Context, agentrun.DiscoveryRequest) (agentrun.TaskTypesCatalog, error) {
		return agentrun.TaskTypesCatalog{}, errors.New("config unreadable")
	}
	if code, _, _ := runFake(t, deps, "agent", "task-types"); code != exitFindings {
		t.Fatalf("code = %d", code)
	}
}

func TestStatusTextReportsTheThreeIndependentAxes(t *testing.T) {
	t.Parallel()
	result := agents.Result{
		AgentID: "agt-00000000000000000000000000000001", State: agents.StateCompleted, Status: agents.StatusCompleted,
		RequestedProfile: "cheap", Resolved: agents.Resolved{Profile: "cheap", Harness: "dsh", Provider: "deepseek-official", Model: "deepseek-v4-flash"},
		TaskType: "coding", Complexity: "high", EffortRequested: "medium",
		Worktree: "op", WorktreeMode: agents.ModeAuto, WorktreeDir: "/fleet/op",
		StartedAt: time.Unix(100, 0).UTC(), SourceDirty: true, ExperimentID: "exp-1", RepeatIndex: 1,
		Validation: agents.ValidationRecord{Status: agents.ValidationFailed, Steps: []agents.ValidationStepResult{
			{Status: agents.ValidationPassed}, {Status: agents.ValidationFailed},
		}},
		Review:    agents.ReviewRecord{Status: agents.ReviewPending},
		Telemetry: agents.Telemetry{Available: true, Tokens: &agents.Tokens{Input: intPointerTest(10), Output: intPointerTest(2)}},
		Cost:      agents.CostRecord{Kind: "api_equivalent", Source: "unavailable", Note: "the dsh harness reports no per-run cost"},
	}
	var out bytes.Buffer
	if err := printAgentStatusText(&out, result); err != nil {
		t.Fatalf("printAgentStatusText: %v", err)
	}
	for _, want := range []string{"(completed)", "task:     coding (complexity high)", "effort:requested=medium (not applied)", "validation: failed (1/2 steps passed)", "review:   pending", "usage:    in=10", "cost:     unavailable", "dirty:", "experiment: exp-1 (attempt 2)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output %q is missing %q", out.String(), want)
		}
	}
}

func intPointerTest(value int) *int { return &value }
