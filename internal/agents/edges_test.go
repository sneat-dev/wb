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

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestPrepareRunRefusesAnInvalidPreallocatedIdentity(t *testing.T) {
	//nolint:paralleltest // the dispatch fixture sets process environment
	fixture := newDispatchFixture(t)
	request := planRequest(t, fixture, ModeAuto, "op")
	request.AgentID = "not-an-agent-id"
	if _, err := PrepareRun(context.Background(), request, fixture.deps); err == nil || !IsRequestError(err) {
		t.Fatalf("invalid pre-allocated ID = %v", err)
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestPrepareRunUsesThePreallocatedIdentity(t *testing.T) {
	//nolint:paralleltest // the dispatch fixture sets process environment
	fixture := newDispatchFixture(t)
	preallocated, _ := NewID()
	request := planRequest(t, fixture, ModeAuto, "op")
	request.AgentID = preallocated
	prepared, err := PrepareRun(context.Background(), request, fixture.deps)
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	if prepared.Record.AgentID != preallocated {
		t.Fatalf("agent ID = %q, want %q", prepared.Record.AgentID, preallocated)
	}
	_ = prepared.Lease.Release()
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestPrepareRunRefusesACodexProfileWithoutACredential(t *testing.T) {
	//nolint:paralleltest // the dispatch fixture sets process environment
	fixture := newDispatchFixture(t)
	t.Setenv("DEEPSEEK_API_KEY", "")
	if _, err := PrepareRun(context.Background(), planRequest(t, fixture, ModeAuto, "op"), fixture.deps); err == nil || !strings.Contains(err.Error(), "DEEPSEEK_API_KEY") {
		t.Fatalf("missing credential = %v", err)
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestPrepareRunRecordsTheDetectedHarnessVersion(t *testing.T) {
	//nolint:paralleltest // the dispatch fixture sets process environment
	fixture := newDispatchFixture(t)
	fixture.deps.DetectVersion = func(context.Context, string) string { return "v9" }
	prepared, err := PrepareRun(context.Background(), planRequest(t, fixture, ModeAuto, "op"), fixture.deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = prepared.Lease.Release() }()
	if prepared.Record.HarnessCapabilities.Version != "v9" {
		t.Fatalf("version = %q", prepared.Record.HarnessCapabilities.Version)
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestPrepareRunRecordsRoutingReasonAndValidationSteps(t *testing.T) {
	//nolint:paralleltest // the dispatch fixture sets process environment
	fixture := newDispatchFixture(t)
	config, err := fixture.deps.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{Config: config}).Plan(RunRequest{
		Task: "x", TaskType: "coding", Profile: "cheap", Complexity: "high", NoValidate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan.probed = true
	plan.FallbackFrom = "coder"
	prepared, err := PrepareRun(context.Background(), DispatchRequest{
		Mode: ModeAuto, Worktree: "op", Plan: &plan, Task: "x", Timeout: 5 * time.Minute,
	}, fixture.deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = prepared.Lease.Release() }()
	if !strings.Contains(prepared.Record.RoutingReason, "complexity high") || !strings.Contains(prepared.Record.RoutingReason, "fallback from coder") {
		t.Fatalf("routing reason = %q", prepared.Record.RoutingReason)
	}
	if prepared.Record.Validation.Status != ValidationPending {
		t.Fatalf("validation = %#v", prepared.Record.Validation)
	}
}

//nolint:paralleltest // the dispatch fixture sets process environment
func TestStartRunRefusesANilPreparationAndAMissingSpawner(t *testing.T) {
	if _, err := StartRun(nil, DispatchDeps{}); err == nil {
		t.Fatal("a nil preparation must be refused")
	}
	fixture := newDispatchFixture(t)
	prepared, err := PrepareRun(context.Background(), planRequest(t, fixture, ModeAuto, "op"), fixture.deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = prepared.Lease.Release() }()
	deps := fixture.deps
	deps.SpawnOwner = nil
	if _, err := StartRun(prepared, deps); err == nil {
		t.Fatal("a run without a spawner must fail")
	}
}

func TestDispatchRequestValidationRefusesIncompleteShapes(t *testing.T) {
	t.Parallel()
	cases := map[string]DispatchRequest{
		"path without worktree": {Mode: ModePath, Task: "x", Timeout: time.Minute},
		"no worktree name":      {Mode: ModeNew, Task: "x", Timeout: time.Minute},
		"blank task":            {Mode: ModeNew, Worktree: "op", Timeout: time.Minute},
		"no timeout":            {Mode: ModeNew, Worktree: "op", Task: "x"},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := validateDispatchRequest(request); err == nil || !IsRequestError(err) {
				t.Fatalf("%s = %v", name, err)
			}
		})
	}
	current := DispatchRequest{Mode: ModeCurrent, Task: "x", Timeout: time.Minute}
	if err := validateDispatchRequest(current); err != nil {
		t.Fatalf("current mode needs no name: %v", err)
	}
}

func TestOwnerHelpersHandleOptionalInput(t *testing.T) {
	t.Parallel()
	record := Record{EffortApplied: true, EffortEffective: "high", Resolved: Resolved{EffortVariants: map[string]string{"high": "heavy"}}}
	if got := effortVariant(record); got != "heavy" {
		t.Fatalf("mapped variant = %q", got)
	}
	record.EffortApplied = false
	record.Resolved.Variant = "static"
	if got := effortVariant(record); got != "static" {
		t.Fatalf("static variant = %q", got)
	}
	if files := leaseFiles(nil, 0); files != nil {
		t.Fatalf("no lease must yield no files: %#v", files)
	}
	file, err := os.CreateTemp(t.TempDir(), "lease")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if files := leaseFiles(file, 2); len(files) != 3 || files[2] != file {
		t.Fatalf("lease files = %#v", files)
	}
	if message := finalMessage(HarnessSummary{}, ""); message != "" {
		t.Fatalf("no message = %q", message)
	}
	if message := failureMessage(HarnessSummary{TurnFailed: true}, errors.New("exit")); message != "harness reported a failed turn" {
		t.Fatalf("failed-turn message = %q", message)
	}
}

func TestStopRunRefusesARunWithoutAWorkerOrWithAGoneWorker(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	record.WorkerPID = 0
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if _, err := StopRun(store, record.AgentID, DefaultOwnerDeps()); err == nil {
		t.Fatal("a run without a worker must be refused")
	}
	record.WorkerPID = 999999
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	if _, err := StopRun(store, record.AgentID, DefaultOwnerDeps()); err == nil || !strings.Contains(err.Error(), "already gone") {
		t.Fatalf("a gone worker = %v", err)
	}
}

func TestExperimentLookupAndPublicationReportFailures(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	experimentID := NewExperimentID()
	if err := store.CreateExperiment(Experiment{ExperimentID: experimentID, CreatedAt: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateExperiment(Experiment{ExperimentID: experimentID}); err == nil {
		t.Fatal("a duplicate write must be refused")
	}
	if _, err := store.LoadExperiment("exp-" + strings.Repeat("z", 32)); err == nil {
		t.Fatal("a non-hex experiment ID must be refused")
	}
	err := (&UnknownExperimentError{ExperimentID: "exp-x", Root: "/root"}).Error()
	if !strings.Contains(err, "exp-x") || !strings.Contains(err, "/root") {
		t.Fatalf("unknown experiment error = %q", err)
	}
}

func TestExperimentCannotBeCreatedUnderAFile(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(store.Root, experimentDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	experimentID := NewExperimentID()
	if err := os.WriteFile(store.ExperimentDir(experimentID), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateExperiment(Experiment{ExperimentID: experimentID}); err == nil {
		t.Fatal("a file where the experiment directory belongs must be reported")
	}
	if _, err := store.LoadExperiment(experimentID); err == nil {
		t.Fatal("reading a directory as an experiment must fail")
	}
}

func TestExperimentReportDerivesAttemptStatuses(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	experimentID := NewExperimentID()
	dispatched, _ := NewID()
	mismatched, _ := NewID()
	experiment := Experiment{
		ExperimentID: experimentID, Profiles: []string{"cheap"}, Repeat: 2,
		Attempts: []Attempt{
			{Profile: "cheap", AgentID: dispatched, Status: AttemptPlanned},
			{Profile: "cheap", AgentID: mismatched, Status: AttemptPlanned},
		},
	}
	if err := store.CreateExperiment(experiment); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(Record{
		AgentID: dispatched, State: StateRunning, ExecutionStatus: StatusQueued,
		RequestedProfile: "cheap", OwnerPID: os.Getpid(), QueueOwnerPID: os.Getpid(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(Record{
		AgentID: mismatched, State: StateFailed, ExecutionStatus: StatusFailed,
		RequestedProfile: "cheap", FailureCategory: FailureBaseMismatch, BaseSHA: "other",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadExperiment(experimentID)
	if err != nil {
		t.Fatal(err)
	}
	report := store.BuildReport(loaded)
	if report.Attempts[0].Status != AttemptDispatched {
		t.Fatalf("a live attempt = %q", report.Attempts[0].Status)
	}
	if report.Attempts[1].Status != AttemptBaseMismatch {
		t.Fatalf("a mismatched attempt = %q", report.Attempts[1].Status)
	}
	if report.BaseSHA != "other" {
		t.Fatalf("the report must recover a base from a member record: %q", report.BaseSHA)
	}
}

func TestProfileSummaryHandlesUnknownProfilesAndUnknownUsage(t *testing.T) {
	t.Parallel()
	experiment := Experiment{Profiles: []string{"a"}}
	runs := []Result{
		{RequestedProfile: "b", Status: StatusFailed, Terminal: true, DurationMS: 10},
		{RequestedProfile: "a", Status: StatusCompleted, Terminal: true, DurationMS: 20,
			Review: agentsReviewAccepted(), Telemetry: Telemetry{Available: false}},
		{RequestedProfile: "a", Status: StatusCompleted, Terminal: true, DurationMS: 30,
			Telemetry: Telemetry{Available: true, Tokens: &Tokens{Input: intPointer(1)}}},
	}
	summaries := summarizeProfiles(experiment, runs)
	if len(summaries) != 2 {
		t.Fatalf("summaries = %#v", summaries)
	}
	if summaries[0].Profile != "a" || summaries[0].Completed != 2 || summaries[0].Accepted != 1 {
		t.Fatalf("profile a = %#v", summaries[0])
	}
	if summaries[0].TotalTokens != nil {
		t.Fatalf("partial usage must stay unknown: %#v", summaries[0].TotalTokens)
	}
	if summaries[1].Profile != "b" || summaries[1].Failed != 1 {
		t.Fatalf("profile b = %#v", summaries[1])
	}
}

func agentsReviewAccepted() ReviewRecord { return ReviewRecord{Status: ReviewAccepted} }

func TestReviewReportsUnreadableAndUnwritableSidecars(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	path := store.ReviewPath(record.AgentID)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadReview(record.AgentID); err == nil {
		t.Fatal("reading a directory as a review must fail")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Dir(record.AgentID), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(store.Dir(record.AgentID), 0o700) })
	if _, err := store.AppendReview(record.AgentID, ReviewEntry{Status: ReviewAccepted, At: time.Unix(1, 0)}); err == nil {
		t.Fatal("an unwritable run directory must be reported")
	}
}

func TestReviewReappliesACorruptHistory(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendReview(record.AgentID, ReviewEntry{Status: ReviewAccepted, Evaluator: "alex", At: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	if !(ReviewRecord{Status: ReviewAccepted}).Reviewed() {
		t.Fatal("an accepted review is reviewed")
	}
	if (ReviewRecord{Status: ReviewNeedsChange}).Reviewed() == false {
		t.Fatal("needs_changes is reviewed")
	}
	if (ReviewRecord{Status: ReviewPending}).Reviewed() {
		t.Fatal("pending is not reviewed")
	}
}

func TestDiffAndTargetResolutionReportFailures(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	// A run directory whose patch path is a directory cannot be written.
	blockedWorktree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(store.Dir(record.AgentID), diffFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteDiff(context.Background(), record.AgentID, blockedWorktree, "base", TargetDeps{
		Git: func(context.Context, string, ...string) (string, error) { return "diff", nil },
	}); err == nil {
		t.Fatal("an unwritable patch path must be reported")
	}
	// A stat document mentions the stored patch path.
	text, err := RenderDiff(context.Background(), Record{
		AgentID: record.AgentID, WorktreeDir: blockedWorktree, DiffPath: "/private/diff.patch",
	}, false, TargetDeps{Git: func(context.Context, string, ...string) (string, error) { return "a.go | 1 +\n", nil }})
	if err != nil || !strings.Contains(text, "/private/diff.patch") {
		t.Fatalf("stat with a patch = %q %v", text, err)
	}
	// A nil git dependency falls back to the real one, which reports the
	// non-repository directory.
	if _, err := RenderDiff(context.Background(), Record{AgentID: record.AgentID, WorktreeDir: blockedWorktree}, false, TargetDeps{}); err == nil {
		t.Fatal("a non-repository directory must fail the diff")
	}
}

func TestPreflightProbeFailureAndLookPathDefault(t *testing.T) {
	t.Parallel()
	deps := DefaultPreflightDeps()
	failing := deps
	failing.Run = func(context.Context, string, ...string) ([]byte, error) { return []byte("out"), errors.New("boom") }
	if _, err := failing.Run(context.Background(), "sh", "-c", "true"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("probe failure = %v", err)
	}
	// DetectVersion without an explicit LookPath uses PATH.
	if got := DetectVersion(context.Background(), "wb-definitely-not-installed", PreflightDeps{
		Run: func(context.Context, string, ...string) ([]byte, error) { return []byte("v"), nil },
	}); got != "" {
		t.Fatalf("a missing binary must yield no version: %q", got)
	}
	// A dsh proof whose overlay cannot be written is reported.
	unwritable := PreflightDeps{
		LookPath: func(string) (string, error) { return "/bin/dsh", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("id: agent-default-model\nmodel: m\n"), nil
		},
		TempDir: func() (string, error) { return t.TempDir(), nil },
	}
	blocked := unwritable
	blocked.TempDir = func() (string, error) { return "/dev/null", nil }
	if err := Preflight(context.Background(), Resolved{Harness: HarnessDSH, Model: "m"}, blocked); err == nil {
		t.Fatal("an unwritable overlay must be reported")
	}
}

func TestHarnessAdaptersExposeTheirNamesAndEdgeActions(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{HarnessCodex: HarnessCodex, HarnessOpenCode: HarnessOpenCode, HarnessDSH: HarnessDSH} {
		adapter, err := HarnessFor(name)
		if err != nil {
			t.Fatal(err)
		}
		if adapter.Name() != want {
			t.Fatalf("adapter %s name = %q", name, adapter.Name())
		}
	}
	if _, err := HarnessFor("nope"); err == nil {
		t.Fatal("an unknown harness must be refused")
	}
	if actions := RecentActions(strings.NewReader("{\"type\":\"item.completed\",\"item\":{\"type\":\"command_execution\",\"command\":\"ls\"}}"), 5); len(actions) != 1 {
		t.Fatalf("codex actions = %#v", actions)
	}
	stream := strings.Join([]string{
		`{"type":"tool_use"}`,
		`{"type":"tool_use","part":{"tool":"shell"}}`,
		`{"type":"tool_use","part":{"tool":"shell","state":{"status":"completed","input":{"command":"a"}}}}`,
		`{"type":"tool_use","part":{"tool":"shell","state":{"status":"completed","input":{"command":"b"}}}}`,
		`{"type":"unknown"}`,
	}, "\n")
	if actions := (openCodeHarness{}).Actions(strings.NewReader(stream), 1); len(actions) != 1 || actions[0] != "ran: b" {
		t.Fatalf("trimmed actions = %#v", actions)
	}
	if _, err := (codexHarness{}).Prepare(Launch{WorktreeDir: "/work", Model: "m"}); err == nil {
		t.Fatal("a codex launch without a provider must fail")
	}
	errReader := errorReader{}
	if summary := (dshHarness{}).Summarize(errReader); summary.TurnCompleted || summary.FinalMessage != "" {
		t.Fatalf("a failing stream must yield an empty summary: %#v", summary)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
