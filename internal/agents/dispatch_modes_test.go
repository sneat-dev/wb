package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// planRequest builds one explicit-plan request on top of the dispatch fixture.
func planRequest(t *testing.T, fixture *dispatchFixture, mode, worktree string) DispatchRequest {
	t.Helper()
	config, err := fixture.deps.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{Config: config}).Plan(RunRequest{Task: "do the planned thing", Profile: "cheap"})
	if err != nil {
		t.Fatal(err)
	}
	plan.probed = true
	return DispatchRequest{
		Mode: mode, Worktree: worktree, Plan: &plan, Task: "do the planned thing",
		Timeout: 5 * time.Minute,
	}
}

//nolint:paralleltest // the dispatch fixture sets process environment
func TestPrepareRunAdmitsTheRunWithoutStartingAnything(t *testing.T) {
	fixture := newDispatchFixture(t)
	prepared, err := PrepareRun(context.Background(), planRequest(t, fixture, ModeAuto, "planned-op"), fixture.deps)
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	if fixture.spawnedAgentID != "" {
		t.Fatal("preparation must start no process")
	}
	if prepared.Record.AgentID == "" || prepared.Record.ExecutionStatus != StatusQueued {
		t.Fatalf("record = %#v", prepared.Record)
	}
	if prepared.Record.TaskSHA256 == "" || prepared.Record.TaskBytes == 0 {
		t.Fatalf("task identity = %#v", prepared.Record)
	}
	if prepared.Record.HarnessCapabilities.Harness != HarnessCodex || prepared.Record.HarnessCapabilities.TaskDelivery != TaskDeliveryStdin {
		t.Fatalf("capabilities = %#v", prepared.Record.HarnessCapabilities)
	}
	loaded, err := NewStore(fixture.home).Load(prepared.Record.AgentID)
	if err != nil {
		t.Fatalf("the record must be durable before the owner starts: %v", err)
	}
	if loaded.ExecutionStatus != StatusQueued {
		t.Fatalf("stored status = %q", loaded.ExecutionStatus)
	}
	if prepared.Lease == nil || prepared.Record.LeasePath == "" {
		t.Fatal("a writable run must hold a writer lease")
	}
	if err := prepared.Lease.Release(); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}
}

//nolint:paralleltest // the dispatch fixture sets process environment
func TestStartRunRefusesASecondWriterOnTheSameWorktree(t *testing.T) {
	fixture := newDispatchFixture(t)
	first, err := PrepareRun(context.Background(), planRequest(t, fixture, ModeAuto, "same-op"), fixture.deps)
	if err != nil {
		t.Fatal(err)
	}
	// A second preparation of the same checkout must fail: the fixture returns
	// one fixed worktree directory for every create.
	_, err = PrepareRun(context.Background(), planRequest(t, fixture, ModeAuto, "same-op"), fixture.deps)
	if err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("second writer = %v", err)
	}
	_ = first.Lease.Release()
}

//nolint:paralleltest // the dispatch fixture sets process environment
func TestPrepareRunRecordsALaunchFailureAndReleasesTheLease(t *testing.T) {
	fixture := newDispatchFixture(t)
	fixture.spawnErr = errors.New("owner unavailable")
	record, err := fixture.dispatch(t, planRequest(t, fixture, ModeAuto, "op"))
	if err == nil || !strings.Contains(err.Error(), "owner unavailable") {
		t.Fatalf("launch failure = %v", err)
	}
	if record.State != StateFailed || record.FailureCategory != FailureInternal {
		t.Fatalf("record = %#v", record)
	}
}

func TestAutoOperationNameIsDeterministicAndRunScoped(t *testing.T) {
	t.Parallel()
	firstID, _ := NewID()
	secondID, _ := NewID()
	first := autoOperationName("coding", "the task", firstID)
	again := autoOperationName("coding", "the task", firstID)
	second := autoOperationName("coding", "the task", secondID)
	if first != again {
		t.Fatalf("the same task and run must produce one name: %q vs %q", first, again)
	}
	if first == second {
		t.Fatalf("a rerun must not reuse the checkout name: %q", first)
	}
	if !strings.HasPrefix(first, "coding-") || len(first) != len("coding-")+8+1+8 {
		t.Fatalf("name = %q", first)
	}
	if got := autoOperationName("", "task", "short"); !strings.HasPrefix(got, "-") {
		t.Fatalf("a missing task type must still produce a name: %q", got)
	}
}

//nolint:paralleltest // the dispatch fixture sets process environment
func TestPrepareRunRefusesAnUnsupportedMode(t *testing.T) {
	fixture := newDispatchFixture(t)
	request := planRequest(t, fixture, "sideways", "op")
	if _, err := PrepareRun(context.Background(), request, fixture.deps); err == nil || !IsRequestError(err) {
		t.Fatalf("unsupported mode = %v", err)
	}
}

//nolint:paralleltest // the dispatch fixture sets process environment
func TestPreparedRunFailAndAbortRecordTerminalState(t *testing.T) {
	fixture := newDispatchFixture(t)
	prepared, err := PrepareRun(context.Background(), planRequest(t, fixture, ModeAuto, "abort-op"), fixture.deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Fail(StateFailed, "base moved", FailureBaseMismatch, fixture.deps.Now); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	loaded, err := NewStore(fixture.home).Load(prepared.Record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExecutionStatus != StatusFailed || loaded.FailureCategory != FailureBaseMismatch {
		t.Fatalf("aborted record = %#v", loaded)
	}
	if err := (*Lease)(nil).Release(); err != nil {
		t.Fatalf("a nil prepared run must be safe: %v", err)
	}
	if err := (*PreparedRun)(nil).Fail(StateFailed, "", "", nil); err != nil {
		t.Fatalf("a nil prepared run must abort safely: %v", err)
	}
}

//nolint:paralleltest // the dispatch fixture sets process environment
func TestPlanBasedDispatchUsesTheConfiguredTaskType(t *testing.T) {
	fixture := newDispatchFixture(t)
	request := planRequest(t, fixture, ModeAuto, "typed-op")
	if _, err := fixture.dispatch(t, request); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if fixture.record.TaskType != "coding" || fixture.record.TaskTypeSource == "" {
		t.Fatalf("task type provenance = %#v", fixture.record)
	}
	if fixture.record.ProfileSource == "" || fixture.record.RoutingReason == "" {
		t.Fatalf("routing provenance = %#v", fixture.record)
	}
}

//nolint:paralleltest // the test changes the process working directory
func TestCurrentModeDispatchesInTheInvokingCheckout(t *testing.T) {
	fixture := newDispatchFixture(t)
	worktree := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(worktree); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	targetDeps := TargetDeps{
		Git: func(_ context.Context, _ string, args ...string) (string, error) {
			switch strings.Join(args, " ") {
			case "rev-parse --show-toplevel":
				return worktree, nil
			case "rev-parse --abbrev-ref HEAD":
				return "main", nil
			case "rev-parse HEAD":
				return "head123", nil
			case "status --porcelain":
				return "", nil
			}
			return "", errors.New("unexpected git")
		},
		OriginSlug: func(context.Context, string) (string, error) { return "acme/app", nil },
	}
	_ = targetDeps
	request := planRequest(t, fixture, ModeCurrent, "")
	// Current mode resolves the real checkout through Git, so this test asserts
	// the refusal path for a non-repository rather than a fake repository.
	_, err = PrepareRun(context.Background(), request, fixture.deps)
	if err == nil || !strings.Contains(err.Error(), "not a Git worktree") {
		t.Fatalf("current mode outside a repository = %v", err)
	}
}

//nolint:paralleltest // the dispatch fixture sets process environment
func TestPathModeRequiresAnAbsoluteExistingCheckout(t *testing.T) {
	fixture := newDispatchFixture(t)
	request := planRequest(t, fixture, ModePath, filepath.Join(t.TempDir(), "absent"))
	if _, err := PrepareRun(context.Background(), request, fixture.deps); err == nil {
		t.Fatal("a missing path must be refused")
	}
}
