package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLeaseRefusesAnEmptyWorktreePath(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	if _, _, err := store.LeaseHolder(" "); err == nil {
		t.Fatal("LeaseHolder must refuse an empty path")
	}
	if _, err := store.AcquireLease(" ", "agt-one", "", "", time.Unix(1, 0)); err == nil {
		t.Fatal("AcquireLease must refuse an empty path")
	}
}

func TestLeaseReportsAnUnopenableLeaseFile(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	worktree := t.TempDir()
	path, err := store.LeasePath(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireLease(worktree, "agt-one", "", "", time.Unix(1, 0)); err == nil {
		t.Fatal("a directory where the lease file belongs must be reported")
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestLeaseReportsANonBusyLockFailureAndABodyWriteFailure(t *testing.T) {
	//nolint:paralleltest // the test substitutes package seams
	store := newTestStore(t)
	worktree := t.TempDir()
	originalLock := lockFile
	originalWriter := newLeaseWriter
	t.Cleanup(func() {
		lockFile = originalLock
		newLeaseWriter = originalWriter
	})
	lockFile = func(*os.File) error { return errors.New("lock unavailable") }
	if _, err := store.AcquireLease(worktree, "agt-one", "", "", time.Unix(1, 0)); err == nil || !strings.Contains(err.Error(), "lock unavailable") {
		t.Fatalf("lock failure = %v", err)
	}
	lockFile = originalLock
	newLeaseWriter = func(*os.File) leaseWriter { return failingLeaseWriter{failWrite: true} }
	if _, err := store.AcquireLease(worktree, "agt-one", "", "", time.Unix(1, 0)); err == nil {
		t.Fatal("a body write failure must be reported")
	}
	// A second attempt must not be blocked by the abandoned claim.
	newLeaseWriter = originalWriter
	lease, err := store.AcquireLease(worktree, "agt-second", "", "", time.Unix(1, 0))
	if err != nil {
		t.Fatalf("the failed claim must have released the lock: %v", err)
	}
	_ = lease.Release()
}

func TestLeaseHeldErrorSurvivesACorruptBody(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	worktree := t.TempDir()
	first, err := store.AcquireLease(worktree, "agt-first", "", "", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Release() })
	// A corrupt body must not turn "held" into "free".
	if err := os.WriteFile(first.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireLease(worktree, "agt-second", "", "", time.Unix(1, 0)); err == nil {
		t.Fatal("a held lease must stay held")
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestDispatchRunsThePreflightAndRefusesAnUnsupportedHarness(t *testing.T) {
	//nolint:paralleltest // the dispatch fixture sets process environment
	fixture := newDispatchFixture(t)
	fixture.deps.Preflight = func(context.Context, Resolved) error { return errors.New("provider unavailable") }
	request := planRequest(t, fixture, ModeAuto, "op")
	request.Plan.probed = false
	if _, err := PrepareRun(context.Background(), request, fixture.deps); err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("preflight failure = %v", err)
	}
	fixture.deps.Preflight = nil
	request = planRequest(t, fixture, ModeAuto, "op")
	request.Plan.Profile.Harness = "nope"
	if _, err := PrepareRun(context.Background(), request, fixture.deps); err == nil || !IsRequestError(err) {
		t.Fatalf("unsupported harness = %v", err)
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestDispatchDerivesTheAutoOperationName(t *testing.T) {
	//nolint:paralleltest // the dispatch fixture sets process environment
	fixture := newDispatchFixture(t)
	request := planRequest(t, fixture, ModeAuto, "")
	prepared, err := PrepareRun(context.Background(), request, fixture.deps)
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	defer func() { _ = prepared.Lease.Release() }()
	if !strings.HasPrefix(prepared.Record.Worktree, "coding-") {
		t.Fatalf("auto operation = %q", prepared.Record.Worktree)
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestDispatchReleasesTheLeaseWhenTheRecordCannotBePersisted(t *testing.T) {
	//nolint:paralleltest // the dispatch fixture sets process environment
	fixture := newDispatchFixture(t)
	preallocated, _ := NewID()
	// A file where the run directory belongs makes persistence fail after the
	// lease was taken.
	store := NewStore(fixture.home)
	if err := os.MkdirAll(store.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Dir(preallocated), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := planRequest(t, fixture, ModeAuto, "op")
	request.AgentID = preallocated
	if _, err := PrepareRun(context.Background(), request, fixture.deps); err == nil {
		t.Fatal("a run that cannot be persisted must fail")
	}
	// The lease for the fixture's worktree must be free again.
	if _, err := store.AcquireLease("/fleet/.worktrees/task-one", "agt-second", "", "", time.Unix(1, 0)); err != nil {
		t.Fatalf("the failed run must release its lease: %v", err)
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestPreparedRunFailDefaultsTheClockAndReportsSaveFailures(t *testing.T) {
	//nolint:paralleltest // the dispatch fixture sets process environment
	fixture := newDispatchFixture(t)
	prepared, err := PrepareRun(context.Background(), planRequest(t, fixture, ModeAuto, "op"), fixture.deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Fail(StateFailed, "no clock", FailureInternal, nil); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	// A record whose directory vanished cannot be saved.
	broken := &PreparedRun{Record: Record{AgentID: prepared.Record.AgentID}, Store: NewStore(filepath.Join(t.TempDir(), "absent"))}
	if err := broken.Fail(StateFailed, "cannot save", FailureInternal, nil); err == nil {
		t.Fatal("a save failure must be reported")
	}
}

func TestPendingValidationStepsSnapshotsEveryStep(t *testing.T) {
	t.Parallel()
	steps := pendingValidationSteps([]ValidationStep{{Name: "vet", Argv: []string{"go", "vet"}, Timeout: "5m"}})
	if len(steps) != 1 || steps[0].Name != "vet" || steps[0].Timeout != "5m" || steps[0].Status != ValidationPending {
		t.Fatalf("steps = %#v", steps)
	}
	if got := pendingValidationSteps(nil); got != nil {
		t.Fatalf("no steps = %#v", got)
	}
}

func TestRecentActionsThroughTheCodexAdapter(t *testing.T) {
	t.Parallel()
	actions := (codexHarness{}).Actions(strings.NewReader("{\"type\":\"item.completed\",\"item\":{\"type\":\"command_execution\",\"command\":\"ls\"}}\n"), 5)
	if len(actions) != 1 {
		t.Fatalf("actions = %#v", actions)
	}
}

func TestDshAdapterReportsAnUnwritableRunDirectory(t *testing.T) {
	t.Parallel()
	if _, err := (dshHarness{}).Prepare(Launch{
		RunDir: filepath.Join(t.TempDir(), "absent"), WorktreeDir: "/work", Model: "m", Task: "x",
	}); err == nil {
		t.Fatal("an unwritable overlay path must be refused")
	}
}

func TestOpenCodeActionsSkipIncompleteToolEvents(t *testing.T) {
	t.Parallel()
	stream := strings.Join([]string{
		`{"type":"tool_use","part":{"tool":"shell","state":{"status":"completed","input":{"command":"ls"}}}}`,
		`{"type":"tool_use","part":{"tool":"shell","state":{"status":"completed"}}}`,
	}, "\n")
	actions := (openCodeHarness{}).Actions(strings.NewReader(stream), 5)
	if len(actions) != 2 || actions[0] != "ran: ls" {
		t.Fatalf("actions = %#v", actions)
	}
}

func TestExperimentReportsAnUnopenableDirectory(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	experimentID := NewExperimentID()
	dir := store.ExperimentDir(experimentID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := store.CreateExperiment(Experiment{ExperimentID: experimentID}); err == nil {
		t.Fatal("an unopenable experiment directory must be reported")
	}
}

func TestReviewAppendSurfacesACorruptHistory(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ReviewPath(record.AgentID), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendReview(record.AgentID, ReviewEntry{Status: ReviewAccepted, At: time.Unix(1, 0)}); err == nil {
		t.Fatal("a corrupt history must not be overwritten silently")
	}
}

func TestRenderTreatsAnUnreadableSidecarAsPending(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	record.State = StateCompleted
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ReviewPath(record.AgentID), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := store.Render(record); result.Review.Status != ReviewPending {
		t.Fatalf("review = %#v", result.Review)
	}
}

func TestPreflightProbeWrapperNamesTheFailingCommand(t *testing.T) {
	t.Parallel()
	deps := DefaultPreflightDeps()
	_, err := deps.Run(context.Background(), "/bin/sh", "-c", "exit 3")
	if err == nil || !strings.Contains(err.Error(), "sh -c exit 3") {
		t.Fatalf("probe error = %v", err)
	}
}

func TestSpawnOwnerPassesTheLeaseDescriptorAndTheMarker(t *testing.T) {
	t.Parallel()
	leaseFile, err := os.CreateTemp(t.TempDir(), "lease")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = leaseFile.Close() })
	// /bin/true exits immediately; the point is that spawn succeeds with an
	// inherited descriptor and a session of its own.
	pid, err := SpawnOwner(t.TempDir(), func() (string, error) { return "/usr/bin/true", nil }, []*os.File{leaseFile})
	if err != nil {
		t.Fatalf("SpawnOwner: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("pid = %d", pid)
	}
}

func TestStopRunReportsAnUnwritableCancellationSentinel(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	record.WorkerPID = os.Getpid()
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Dir(record.AgentID), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(store.Dir(record.AgentID), 0o700) })
	deps := DefaultOwnerDeps()
	deps.LoadExecutionPolicy = noMandatoryExecutionPolicy
	deps.Now = func() time.Time { return time.Unix(1, 0) }
	if _, err := stopRunWithSignal(store, record.AgentID, deps, func(int, syscall.Signal) error { return nil }); err == nil {
		t.Fatal("an unwritable sentinel must be reported")
	}
}

func TestValidationReportsAStreamThatCannotBeRead(t *testing.T) {
	t.Parallel()
	// A run directory that cannot hold a log file reports the step as failed.
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, validationLogDirName), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(runDir, validationLogDirName), 0o700) })
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: runDir,
		Steps: []ValidationStep{{Name: "one", Argv: []string{"/usr/bin/true"}}},
	})
	if record.Status != ValidationFailed {
		t.Fatalf("validation = %#v", record)
	}
}

func TestLeaseHolderAndPathResolveNestedMissingCheckouts(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	worktree := t.TempDir()
	path, err := store.LeasePath(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	// A directory where the lease body belongs is a real read failure.
	if _, _, err := store.LeaseHolder(worktree); err == nil {
		t.Fatal("reading a directory as a lease body must fail")
	}
	deep := filepath.Join(t.TempDir(), "one", "two", "three")
	if _, err := store.LeasePath(deep); err != nil {
		t.Fatalf("a nested missing checkout must still resolve: %v", err)
	}
}

func TestPlanRecordsAnInheritedEffortTheFallbackCannotApply(t *testing.T) {
	t.Parallel()
	config, err := LoadConfigFile(writeConfig(t, `
agents:
  profiles:
    coder: { harness: codex, provider: deepseek, model: m }
    op: { harness: opencode, model: p/m }
  task_types:
    coding: { profile: coder, effort: high }
  fallback: [op]
`))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{Config: config, Probe: func(resolved Resolved) error {
		if resolved.Profile == "coder" {
			return errors.New("unavailable")
		}
		return nil
	}}).Plan(RunRequest{Task: "x", TaskType: "coding"})
	if err != nil {
		t.Fatalf("an inherited effort never fails a fallback: %v", err)
	}
	if plan.Profile.Profile != "op" || plan.EffortApplied || plan.EffortNote == "" {
		t.Fatalf("fallback effort = %#v", plan)
	}
}

func TestConfigRefusesAnEmptyTaskTypeName(t *testing.T) {
	t.Parallel()
	_, err := LoadConfigFile(writeConfig(t, "agents:\n  task_types:\n    \"\": { profile: p }\n"))
	if err == nil || !strings.Contains(err.Error(), "task type name must not be empty") {
		t.Fatalf("empty task type name = %v", err)
	}
}

func TestReviewAppendReportsAnUnwritableRunDirectory(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Dir(record.AgentID), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(store.Dir(record.AgentID), 0o700) })
	if _, err := store.AppendReview(record.AgentID, ReviewEntry{Status: ReviewAccepted, At: time.Unix(1, 0)}); err == nil {
		t.Fatal("an unwritable directory must be reported")
	}
}

func TestOpenCodeActionsSkipAMalformedEvent(t *testing.T) {
	t.Parallel()
	stream := "{\"type\":\"tool_use\"\n" + `{"type":"tool_use","part":{"tool":"shell","state":{"status":"completed","input":{"command":"ls"}}}}` + "\n"
	actions := (openCodeHarness{}).Actions(strings.NewReader(stream), 5)
	if len(actions) != 1 || actions[0] != "ran: ls" {
		t.Fatalf("actions = %#v", actions)
	}
}

func TestResolveThroughExistingAncestorFallsBackToTheCleanPath(t *testing.T) {
	t.Parallel()
	alwaysFails := func(string) (string, error) { return "", errors.New("unresolvable") }
	if got := resolveThroughExistingAncestor("/nowhere/at/all", alwaysFails); got != "/nowhere/at/all" {
		t.Fatalf("fallback path = %q", got)
	}
	resolvesRoot := func(path string) (string, error) {
		if path == "/" {
			return "/", nil
		}
		return "", errors.New("not yet")
	}
	if got := resolveThroughExistingAncestor("/a/b", resolvesRoot); got != "/a/b" {
		t.Fatalf("root-resolved path = %q", got)
	}
}

func TestReviewReportsAnUnopenableRunDirectory(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	// Execute-only keeps the record readable but refuses to open the directory
	// itself, which is the review writer's first step.
	if err := os.Chmod(store.Dir(record.AgentID), 0o111); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(store.Dir(record.AgentID), 0o700) })
	if _, err := store.AppendReview(record.AgentID, ReviewEntry{Status: ReviewAccepted, At: time.Unix(1, 0)}); err == nil {
		t.Fatal("an unopenable run directory must be reported")
	}
}

func TestResolveTargetRefusesAnEmptyToplevel(t *testing.T) {
	t.Parallel()
	fixture := newTargetFixture(t)
	fixture.git["rev-parse --show-toplevel"] = "   \n"
	if _, err := ResolveTarget(context.Background(), fixture.dir, false, false, fixture.deps()); err == nil || !strings.Contains(err.Error(), "not a Git worktree") {
		t.Fatalf("empty toplevel = %v", err)
	}
}

func TestLeaseLiveDistinguishesAHeldClaimFromARetainedFile(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	worktree := t.TempDir()
	live, err := store.LeaseLive(worktree)
	if err != nil || live {
		t.Fatalf("no lease = %v %v", live, err)
	}
	lease, err := store.AcquireLease(worktree, "agt-one", "", "acme/app", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if live, err := store.LeaseLive(worktree); err != nil || !live {
		t.Fatalf("held lease = %v %v", live, err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	// The retained file proves nothing: only the lock does.
	if live, err := store.LeaseLive(worktree); err != nil || live {
		t.Fatalf("released lease = %v %v", live, err)
	}
	if _, err := store.LeaseLive(" "); err == nil {
		t.Fatal("an empty path must be refused")
	}
	other := t.TempDir()
	path, err := store.LeasePath(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LeaseLive(other); err == nil {
		t.Fatal("a directory where the lease body belongs must be reported")
	}
}

func TestValidationBudgetSumsEveryStepBound(t *testing.T) {
	t.Parallel()
	if got := ValidationBudget(nil); got != 0 {
		t.Fatalf("empty budget = %v", got)
	}
	budget := ValidationBudget([]ValidationStepResult{
		{Timeout: "2m"},
		{Timeout: ""},
		{Timeout: "not-a-duration"},
	})
	if budget != 2*time.Minute+2*defaultValidationStepTimeout {
		t.Fatalf("budget = %v", budget)
	}
}

func TestExecutionStartFallsBackToAdmissionTime(t *testing.T) {
	t.Parallel()
	admitted := time.Unix(100, 0)
	if got := executionStart(Record{StartedAt: admitted}); !got.Equal(admitted) {
		t.Fatalf("fallback start = %v", got)
	}
	execution := time.Unix(200, 0)
	if got := executionStart(Record{StartedAt: admitted, ExecutionStartedAt: execution}); !got.Equal(execution) {
		t.Fatalf("execution start = %v", got)
	}
}

func TestRenderReportsAQueuedAttemptAsAbandonedOnlyWhenItsDispatcherIsGone(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	live := sampleRecord(t)
	live.State = StateRunning
	live.ExecutionStatus = StatusQueued
	live.QueueOwnerPID = os.Getpid()
	live.StartedAt = time.Now().Add(-time.Hour)
	if result := store.Render(live); result.Status != StatusQueued || result.Terminal {
		t.Fatalf("a live queued attempt = %#v", result)
	}
	dead := live
	dead.QueueOwnerPID = 999999
	if result := store.Render(dead); result.Status != StatusAbandoned || !result.Terminal {
		t.Fatalf("an orphaned queued attempt = %#v", result)
	}
}

func TestRenderExposesTheExecutionStartAndQueueDuration(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	execution := time.Unix(500, 0)
	record.ExecutionStartedAt = execution
	record.QueueDurationMS = 250
	result := store.Render(record)
	if result.ExecutionStartedAt == nil || !result.ExecutionStartedAt.Equal(execution) {
		t.Fatalf("execution start = %#v", result.ExecutionStartedAt)
	}
	if result.QueueDurationMS != 250 {
		t.Fatalf("queue duration = %d", result.QueueDurationMS)
	}
}

func TestStopRunCancelsAQueuedAttemptWithASentinel(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	record.ExecutionStatus = StatusQueued
	record.QueueOwnerPID = os.Getpid()
	record.WorkerPID = 0
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if _, err := StopRun(store, record.AgentID, DefaultOwnerDeps()); err != nil {
		t.Fatalf("queued stop: %v", err)
	}
	if !cancelRequested(store, record.AgentID) {
		t.Fatal("a queued stop must leave the sentinel the owner will read")
	}
}

func TestResolveTargetTreatsAnUnanswerableGitAsCanonical(t *testing.T) {
	t.Parallel()
	fixture := newTargetFixture(t)
	fixture.git["rev-parse --show-toplevel"] = fixture.dir
	fixture.git["rev-parse --abbrev-ref HEAD"] = "main"
	fixture.git["rev-parse HEAD"] = "abc123"
	fixture.git["status --porcelain"] = ""
	fixture.gitErr["rev-parse --git-dir"] = errors.New("unavailable")
	if _, err := ResolveTarget(context.Background(), fixture.dir, false, false, fixture.deps()); err == nil {
		t.Fatal("an unanswerable checkout must demand an explicit override")
	}
	delete(fixture.gitErr, "rev-parse --git-dir")
	fixture.git["rev-parse --git-dir"] = "/repo/.git/worktrees/linked"
	fixture.gitErr["rev-parse --git-common-dir"] = errors.New("unavailable")
	if _, err := ResolveTarget(context.Background(), fixture.dir, false, false, fixture.deps()); err == nil {
		t.Fatal("an unanswerable common directory must demand an explicit override")
	}
}

//nolint:paralleltest // the test substitutes the platform lock seam
func TestLeaseLiveTreatsAnUnprovableClaimAsFree(t *testing.T) {
	store := newTestStore(t)
	worktree := t.TempDir()
	lease, err := store.AcquireLease(worktree, "agt-one", "", "acme/app", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	original := lockFile
	lockFile = func(*os.File) error { return errors.New("locking unavailable") }
	t.Cleanup(func() { lockFile = original })
	live, err := store.LeaseLive(worktree)
	if err != nil || live {
		t.Fatalf("an unprovable claim = %v %v", live, err)
	}
}

func TestStopRunReportsAnUnwritableQueuedSentinel(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	record.ExecutionStatus = StatusQueued
	record.QueueOwnerPID = os.Getpid()
	record.WorkerPID = 0
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Dir(record.AgentID), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(store.Dir(record.AgentID), 0o700) })
	if _, err := StopRun(store, record.AgentID, DefaultOwnerDeps()); err == nil {
		t.Fatal("an unwritable sentinel must be reported")
	}
}

//nolint:paralleltest // the dispatch fixture sets process environment
func TestPreparedRunIsNotReportedAbandonedWhileItsDispatcherLives(t *testing.T) {
	fixture := newDispatchFixture(t)
	prepared, err := PrepareRun(context.Background(), planRequest(t, fixture, ModeAuto, "op"), fixture.deps)
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	t.Cleanup(func() { _ = prepared.Lease.Release() })
	// The dispatcher (this process) is alive, so a prepared attempt is pending,
	// not finished — this is the state every compare attempt passes through.
	if prepared.Record.QueueOwnerPID != os.Getpid() {
		t.Fatalf("queue owner = %d", prepared.Record.QueueOwnerPID)
	}
	result := prepared.Store.Render(prepared.Record)
	if result.Terminal || result.Status != StatusQueued {
		t.Fatalf("a prepared attempt must be pending: %#v", result)
	}
	// Once the dispatcher is gone and the admission grace has passed, the same
	// record is honestly abandoned.
	stale := prepared.Record
	stale.QueueOwnerPID = 999999
	stale.StartedAt = time.Now().Add(-2 * admissionGrace)
	if result := prepared.Store.Render(stale); !result.Terminal || result.Status != StatusAbandoned {
		t.Fatalf("an orphaned attempt = %#v", result)
	}
}
