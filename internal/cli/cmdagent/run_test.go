package cmdagent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
)

// runFixture fills the run and discovery operations the new commands need.
func runFixture(t *testing.T) (Dependencies, *[]agentrun.RunRequest, *[]agentrun.CompareRequest) {
	t.Helper()
	deps := fakeDependencies()
	runRequests := &[]agentrun.RunRequest{}
	compareRequests := &[]agentrun.CompareRequest{}
	deps.Run = func(_ context.Context, request agentrun.RunRequest) (agents.Result, error) {
		*runRequests = append(*runRequests, request)
		return agents.Result{
			AgentID: "agt-00000000000000000000000000000001", State: agents.StateRunning,
			Status: agents.StatusQueued, RequestedProfile: "cheap",
			Resolved: agents.Resolved{Profile: "cheap", Harness: agents.HarnessOpenCode, Model: "opencode-go/deepseek-v4.1-flash"},
			Worktree: "coding-1234-abcd", WorktreeMode: agents.ModeAuto, StartedAt: time.Unix(100, 0).UTC(),
		}, nil
	}
	deps.Await = func(_ context.Context, request agentrun.AwaitRequest) (agents.Result, error) {
		return agents.Result{
			AgentID: request.AgentID, State: agents.StateCompleted, Status: agents.StatusCompleted,
			Terminal: true, RequestedProfile: "cheap",
			Resolved:  agents.Resolved{Profile: "cheap", Harness: agents.HarnessOpenCode, Model: "m"},
			StartedAt: time.Unix(100, 0).UTC(),
		}, nil
	}
	deps.Compare = func(_ context.Context, request agentrun.CompareRequest) (agents.ExperimentReport, error) {
		*compareRequests = append(*compareRequests, request)
		return agents.ExperimentReport{
			Experiment: agents.Experiment{
				ExperimentID: "exp-00000000000000000000000000000001", BaseSHA: "base123",
				Profiles: request.Profiles, Repeat: request.Repeat,
			},
			Runs: []agents.Result{{
				AgentID: "agt-00000000000000000000000000000002", RequestedProfile: "cheap",
				Status: agents.StatusCompleted, Terminal: true, DurationMS: 1500,
				Validation: agents.ValidationRecord{Status: agents.ValidationPassed},
			}},
			Summaries: []agents.ProfileSummary{{Profile: "cheap", Attempts: 1, Completed: 1, Validated: 1}},
		}, nil
	}
	return deps, runRequests, compareRequests
}

func TestRunDryRunPrintsTheResolvedPlan(t *testing.T) {
	t.Parallel()
	deps, runRequests, _ := runFixture(t)
	var planned *agentrun.PlanRequest
	deps.Plan = func(_ context.Context, request agentrun.PlanRequest) (agents.Plan, error) {
		planned = &request
		return agents.Plan{
			TaskType: "coding", TaskTypeSource: agents.SourceTaskType,
			Profile:       agents.Resolved{Profile: "op", Harness: agents.HarnessOpenCode, Provider: "opencode-go", Model: "opencode-go/m"},
			ProfileSource: "task_type",
			Complexity:    "high", ComplexitySource: agents.SourceExplicit,
			EffortRequested: "medium", EffortSource: agents.SourceTaskType,
			EffortEffective: "medium", EffortApplied: true,
			WorktreeMode: agents.ModeAuto, WorktreeSource: agents.SourceDefault,
			TimeoutMS: 600000, TimeoutSource: agents.SourceTaskType,
			Validation: []string{"go"}, ValidationSource: agents.SourceTaskType,
			ValidationSteps:    []agents.ValidationStep{{Name: "vet", Argv: []string{"go", "vet"}}},
			AcceptanceRequired: true,
		}, nil
	}
	code, out, errout := runFake(t, deps, "agent", "run", "--task-type", "coding", "--task-complexity", "high", "--dry-run", "--task", "do it")
	if code != exitOK {
		t.Fatalf("code = %d stderr=%s", code, errout)
	}
	if len(*runRequests) != 0 {
		t.Fatal("a dry run must never dispatch")
	}
	if planned == nil || planned.Request.Task != "do it" || planned.Request.TaskType != "coding" {
		t.Fatalf("plan request = %#v", planned)
	}
	for _, want := range []string{"task type:  coding", "profile:    op", "complexity: high", "worktree:   auto", "validation: vet", "acceptance: required", "sha256"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q is missing %q", out, want)
		}
	}
}

func TestRunDispatchesAndOptionallyWaits(t *testing.T) {
	t.Parallel()
	deps, runRequests, _ := runFixture(t)
	code, out, errout := runFake(t, deps, "agent", "run", "--task-type", "coding", "--task", "do it")
	if code != exitOK {
		t.Fatalf("code = %d stderr=%s", code, errout)
	}
	if len(*runRequests) != 1 || (*runRequests)[0].Request.Task != "do it" {
		t.Fatalf("requests = %#v", *runRequests)
	}
	if !strings.Contains(out, "agt-00000000000000000000000000000001") {
		t.Fatalf("output = %q", out)
	}

	code, out, errout = runFake(t, deps, "agent", "run", "--task-type", "coding", "--wait", "--task", "do it", "--format", "json")
	if code != exitOK {
		t.Fatalf("wait code = %d stderr=%s", code, errout)
	}
	if !strings.Contains(out, `"execution_status": "completed"`) {
		t.Fatalf("waited output = %q", out)
	}
}

//nolint:paralleltest // each case owns its own command tree and dependency set
func TestRunResolvesEveryWorktreeSpelling(t *testing.T) {
	absolute := "/tmp/some-worktree"
	cases := []struct {
		name  string
		args  []string
		mode  string
		value string
		code  int
	}{
		{"auto", []string{"--worktree", "auto", "--operation", "op"}, agents.ModeAuto, "op", exitOK},
		{"current", []string{"--worktree", "current"}, agents.ModeCurrent, "", exitOK},
		{"path", []string{"--worktree", absolute}, agents.ModePath, absolute, exitOK},
		{"new", []string{"--new-worktree", "op"}, agents.ModeNew, "op", exitOK},
		{"existing", []string{"--use-worktree", "op"}, agents.ModeExisting, "op", exitOK},
		{"default", nil, "", "", exitOK},
		{"bare name", []string{"--worktree", "op"}, "", "", exitUsage},
		{"two modes", []string{"--worktree", "auto", "--use-worktree", "op"}, "", "", exitUsage},
	}
	for _, testCase := range cases {
		//nolint:paralleltest // the cases share the serial parent's recorder
		t.Run(testCase.name, func(t *testing.T) {
			// Each case builds its own recorder: parallel cases must never share
			// the slice the dependency appends to.
			deps, requests, _ := runFixture(t)
			args := append([]string{"agent", "run", "--task", "do it"}, testCase.args...)
			code, _, errout := runFake(t, deps, args...)
			if code != testCase.code {
				t.Fatalf("code = %d (want %d) stderr=%s", code, testCase.code, errout)
			}
			if testCase.code != exitOK {
				return
			}
			if len(*requests) != 1 {
				t.Fatalf("requests = %#v", *requests)
			}
			last := (*requests)[0]
			if last.Request.WorktreeMode != testCase.mode || last.Request.Worktree != testCase.value {
				t.Fatalf("mode/value = %q/%q, want %q/%q", last.Request.WorktreeMode, last.Request.Worktree, testCase.mode, testCase.value)
			}
		})
	}
}

func TestRunMapsRefusalsToTheUsageExitCode(t *testing.T) {
	t.Parallel()
	deps, _, _ := runFixture(t)
	deps.Run = func(context.Context, agentrun.RunRequest) (agents.Result, error) {
		return agents.Result{}, agents.NewRequestError("--profile and --harness conflict")
	}
	code, _, errout := runFake(t, deps, "agent", "run", "--task", "x")
	if code != exitUsage || !strings.Contains(errout, "conflict") {
		t.Fatalf("code = %d stderr=%s", code, errout)
	}
	deps.Plan = func(context.Context, agentrun.PlanRequest) (agents.Plan, error) {
		return agents.Plan{}, agents.NewRequestError("no profile")
	}
	code, _, errout = runFake(t, deps, "agent", "run", "--task", "x", "--dry-run")
	if code != exitUsage || !strings.Contains(errout, "no profile") {
		t.Fatalf("dry run code = %d stderr=%s", code, errout)
	}
}

func TestRunPassesValidationAndTimeoutProvenance(t *testing.T) {
	t.Parallel()
	deps, requests, _ := runFixture(t)
	code, _, errout := runFake(t, deps, "agent", "run", "--task", "x",
		"--validate", "go", "--timeout", "90m", "--task-complexity", "low", "--effort", "high")
	if code != exitOK {
		t.Fatalf("code = %d stderr=%s", code, errout)
	}
	request := (*requests)[0].Request
	if !request.ValidateSet || len(request.Validate) != 1 || request.Validate[0] != "go" {
		t.Fatalf("validation = %#v", request)
	}
	if !request.TimeoutSet || request.Timeout != 90*time.Minute {
		t.Fatalf("timeout = %v set=%v", request.Timeout, request.TimeoutSet)
	}
	if request.Complexity != "low" || request.Effort != "high" {
		t.Fatalf("complexity/effort = %q/%q", request.Complexity, request.Effort)
	}
	code, _, _ = runFake(t, deps, "agent", "run", "--task", "x", "--no-validate")
	if code != exitOK || !(*requests)[1].Request.NoValidate {
		t.Fatalf("no-validate was not passed through: %#v", (*requests)[1].Request)
	}
}
