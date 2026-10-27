package cmdagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
)

func TestAgentStatsFiltersBySessionAndRepository(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	cost := 0.4
	deps.List = func(_ context.Context, request agentrun.ListRequest) ([]agents.Result, error) {
		if request.ProjectsRoot != "/fixture" {
			t.Fatalf("wrong projects root: %+v", request)
		}
		return []agents.Result{
			{Caller: agents.CallerSession{ID: "thread-1", WBSessionID: "wbs-1", Name: "builder"}, Repository: "acme/app",
				Status: agents.StatusCompleted, ToolCalls: 4, Cost: agents.CostRecord{USD: &cost}},
			{Caller: agents.CallerSession{ID: "thread-1"}, Repository: "acme/lib", Status: agents.StatusCompleted},
			{Caller: agents.CallerSession{ID: "thread-2"}, Repository: "acme/app", Status: agents.StatusCompleted},
		}, nil
	}
	code, output, stderr := runFake(t, deps, "agent", "stats", "--session", "wbs-1", "--repo", "acme/app", "--format", "json")
	if code != 0 {
		t.Fatal(code, stderr)
	}
	var report agents.StatsReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if report.Total.Runs != 1 || len(report.BySessionRepository) != 1 ||
		report.BySessionRepository[0].SessionID != "thread-1" || report.BySessionRepository[0].Repository != "acme/app" {
		t.Fatalf("filters did not select exact pair: %+v", report)
	}
	code, output, stderr = runFake(t, deps, "agent", "stats")
	if code != 0 || !strings.Contains(output, "sessions:") || !strings.Contains(output, "repositories:") ||
		!strings.Contains(output, "builder") || !strings.Contains(output, "tool_calls=4") ||
		!strings.Contains(output, "estimated_usd=0.4000") {
		t.Fatal(code, output, stderr)
	}
}

type failAtStatsWrite struct {
	writes int
	failAt int
	err    error
}

func (w *failAtStatsWrite) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, w.err
	}
	return len(p), nil
}

func TestAgentStatsPropagatesListFailure(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.List = func(context.Context, agentrun.ListRequest) ([]agents.Result, error) {
		return nil, errors.New("run store unavailable")
	}
	code, _, stderr := runFake(t, deps, "agent", "stats", "--format", "json")
	if code == 0 || !strings.Contains(stderr, "run store unavailable") {
		t.Fatal(code, stderr)
	}
}

func TestAgentStatsPropagatesTextWriterFailure(t *testing.T) {
	t.Parallel()
	report := agents.SummarizeRuns([]agents.Result{{Repository: "acme/app", Status: agents.StatusCompleted}})
	for _, failAt := range []int{1, 2, 3, 4} {
		failure := errors.New("output closed")
		writer := &failAtStatsWrite{failAt: failAt, err: failure}
		if err := printAgentStats(writer, report); !errors.Is(err, failure) {
			t.Fatalf("write %d: got %v, want output error", failAt, err)
		}
	}
	// An empty caller and repository needs an explicit label in text output.
	unattributed := agents.SummarizeRuns([]agents.Result{{Status: agents.StatusRunning}})
	var output strings.Builder
	if err := printAgentStats(&output, unattributed); err != nil || !strings.Contains(output.String(), "(unattributed)") {
		t.Fatalf("empty caller label missing: %v %q", err, output.String())
	}
}

//nolint:paralleltest // Mutates the process-global CODEX_THREAD_ID harness env to prove a remote dispatch carries the source harness session it was given.
func TestRemoteDispatchCarriesTheSourceHarnessSession(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "thread-origin")
	deps := fakeDependencies()
	called := false
	deps.CallRemote = func(_ context.Context, _ agents.RemoteTarget, request agents.RemoteRequest) (agents.RemoteResponse, error) {
		called = true
		if request.Caller.ID != "thread-origin" || request.Caller.Runtime != "codex" ||
			request.Caller.Source != "CODEX_THREAD_ID" {
			t.Fatalf("remote dispatch omitted source session: %+v", request.Caller)
		}
		result := agents.Result{AgentID: "agt-1", State: agents.StateRunning}
		return agents.RemoteResponse{Result: &result}, nil
	}
	code, _, stderr := runFake(t, deps, "agent", "dispatch", "--to=machine",
		"--new-worktree=task", "--profile=cheap", "--task=synthetic")
	if code != 0 || !called {
		t.Fatal(code, called, stderr)
	}
}
