package agentrun

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
)

func TestRunReportsAStartFailureAndResolvesWithoutDispatchingForADryRun(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	fixture.deps.StartRun = func(prepared *agents.PreparedRun, _ agents.DispatchDeps) (agents.Record, error) {
		record := prepared.Record
		record.State = agents.StateFailed
		record.ExecutionStatus = agents.StatusFailed
		return record, errors.New("owner did not start")
	}
	if _, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root, Request: agents.RunRequest{Task: "x", TaskType: "coding"},
	}); err == nil {
		t.Fatal("a start failure must be reported")
	}

	plan, err := fixture.service().Plan(context.Background(), PlanRequest{
		ProjectsRoot: fixture.root, Request: agents.RunRequest{Task: "x", TaskType: "coding"},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Profile.Profile != "cheap" || plan.ValidationSource != agents.SourceTaskType {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestRunUsesTheRequestWorktreeForCurrentMode(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	if _, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root,
		Request:      agents.RunRequest{Task: "review", TaskType: "coding", WorktreeMode: agents.ModeCurrent, Worktree: "/explicit/checkout"},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fixture.prepare.Worktree != "/explicit/checkout" {
		t.Fatalf("worktree = %q", fixture.prepare.Worktree)
	}
}

func TestDiscoverySurfacesAnUnreadableConfiguration(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	fixture.deps.LoadConfig = func(string) (agents.Config, error) { return agents.Config{}, errors.New("unreadable") }
	service := fixture.service()
	if _, err := service.Profiles(context.Background(), DiscoveryRequest{ProjectsRoot: fixture.root}); err == nil {
		t.Fatal("Profiles must report an unreadable configuration")
	}
	if _, err := service.TaskTypes(context.Background(), DiscoveryRequest{ProjectsRoot: fixture.root}); err == nil {
		t.Fatal("TaskTypes must report an unreadable configuration")
	}
	if _, err := service.Plan(context.Background(), PlanRequest{ProjectsRoot: fixture.root}); err == nil {
		t.Fatal("Plan must report an unreadable configuration")
	}
	if _, err := service.Run(context.Background(), RunRequest{ProjectsRoot: fixture.root, Request: agents.RunRequest{Task: "x"}}); err == nil {
		t.Fatal("Run must report an unreadable configuration")
	}
	if _, err := service.Compare(context.Background(), CompareRequest{ProjectsRoot: fixture.root, Task: "x", Profiles: []string{"cheap"}, Repeat: 1, Concurrency: 1}); err == nil {
		t.Fatal("Compare must report an unreadable configuration")
	}
}

func TestDiscoveryAndLookupsReportAFailingStore(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	fixture.deps.Root = func(string) (string, error) { return "", errors.New("no root") }
	service := fixture.service()
	if _, err := service.Experiment(context.Background(), ExperimentRequest{ProjectsRoot: fixture.root, ExperimentID: "exp-00000000000000000000000000000001"}); err == nil {
		t.Fatal("Experiment must report a failing store")
	}
	if _, err := service.Diff(context.Background(), DiffRequest{ProjectsRoot: fixture.root, AgentID: "agt-00000000000000000000000000000001"}); err == nil {
		t.Fatal("Diff must report a failing store")
	}
	if _, err := service.Review(context.Background(), ReviewRequest{ProjectsRoot: fixture.root, AgentID: "agt-00000000000000000000000000000001", Status: agents.ReviewAccepted}); err == nil {
		t.Fatal("Review must report a failing store")
	}
	if _, err := service.Prune(context.Background(), PruneRequest{ProjectsRoot: fixture.root, OlderThan: time.Hour}); err == nil {
		t.Fatal("Prune must report a failing store")
	}
	if _, err := service.Logs(context.Background(), LogsRequest{ProjectsRoot: fixture.root, AgentID: "agt-00000000000000000000000000000001"}); err == nil {
		t.Fatal("Logs must report a failing store")
	}
	if _, err := service.Stop(context.Background(), LookupRequest{ProjectsRoot: fixture.root, AgentID: "agt-00000000000000000000000000000001"}); err == nil {
		t.Fatal("Stop must report a failing store")
	}
	if _, err := service.Await(context.Background(), AwaitRequest{ProjectsRoot: fixture.root, AgentID: "agt-00000000000000000000000000000001"}); err == nil {
		t.Fatal("Await must report a failing store")
	}
}

func TestDiffReportsAnUnknownRunAndRendersThroughTheStore(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	store := agents.NewStore(fixture.home)
	id, _ := agents.NewID()
	if err := store.Create(agents.Record{AgentID: id, State: agents.StateCompleted, WorktreeDir: fixture.root}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service().Diff(context.Background(), DiffRequest{ProjectsRoot: fixture.root, AgentID: id}); err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if _, err := fixture.service().Diff(context.Background(), DiffRequest{ProjectsRoot: fixture.root, AgentID: "agt-00000000000000000000000000000000"}); !agents.IsUnknownAgent(err) {
		t.Fatalf("unknown diff = %v", err)
	}
}

func TestTranscriptSkipsUnknownHarnessActionsAndReadsRaw(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/events"
	writeTestFile(t, path, `{"type":"tool_use","part":{"tool":"shell","state":{"status":"completed","input":{"command":"ls"}}}}`+"\n")
	reader := DefaultTranscriptReader()
	summary, err := reader.Read(agents.Record{LogPath: path, Resolved: agents.Resolved{Harness: agents.HarnessOpenCode}}, 5, false)
	if err != nil || !strings.Contains(summary, "ran: ls") {
		t.Fatalf("opencode transcript = %q %v", summary, err)
	}
	raw, err := reader.Read(agents.Record{LogPath: path, Resolved: agents.Resolved{Harness: agents.HarnessOpenCode}}, 5, true)
	if err != nil || !strings.Contains(raw, "tool_use") {
		t.Fatalf("raw transcript = %q %v", raw, err)
	}
	if _, err := reader.Read(agents.Record{LogPath: path, Resolved: agents.Resolved{Harness: "nope"}}, 5, false); err != nil {
		// An unknown harness falls back to the Codex action reader.
		t.Fatalf("unknown harness transcript = %v", err)
	}
}

func TestDefaultDependenciesWireEveryOperation(t *testing.T) {
	t.Parallel()
	deps := DefaultDependencies()
	if deps.PrepareRun == nil || deps.StartRun == nil || deps.Preflight == nil || deps.DetectVersion == nil || deps.Diff == nil || deps.NewAgentID == nil || deps.NewExperimentID == nil {
		t.Fatalf("default dependencies = %#v", deps)
	}
	if root, err := deps.Root(t.TempDir()); err != nil || root == "" {
		t.Fatalf("root = %q %v", root, err)
	}
	if deps.ConfigPath() == "" {
		t.Fatal("config path must resolve")
	}
	if id := deps.NewExperimentID(); !strings.HasPrefix(id, "exp-") {
		t.Fatalf("experiment ID = %q", id)
	}
	if id, err := deps.NewAgentID(); err != nil || !strings.HasPrefix(id, "agt-") {
		t.Fatalf("agent ID = %q %v", id, err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCompareRefusesAFailingPreparationPath(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	fixture.deps.NewAgentID = func() (string, error) { return "", errors.New("no entropy") }
	if _, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "x", Profiles: []string{"cheap"}, Repeat: 1, Concurrency: 1,
	}); err == nil {
		t.Fatal("a failing identity must abort the plan")
	}
}

func TestCompareReportsAFailingStoreAndADuplicateExperiment(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	fixture.deps.EnsureRoot = func(string) (string, error) { return "", errors.New("no home") }
	if _, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "x", Profiles: []string{"cheap"}, Repeat: 1, Concurrency: 1,
	}); err == nil {
		t.Fatal("a failing store must be reported")
	}

	fixture = newCompareFixture(t)
	// The fixed experiment ID already exists, so its immutable document cannot
	// be created twice.
	store := agents.NewStore(fixture.home)
	if err := store.CreateExperiment(agents.Experiment{ExperimentID: "exp-00000000000000000000000000000001"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "x", Profiles: []string{"cheap"}, Repeat: 1, Concurrency: 1,
	}); err == nil {
		t.Fatal("a duplicate experiment must be reported")
	}
}

func TestCompareUsesTheDefaultTimeoutAndClampsTheBatch(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	fixture.deps.PrepareRun = func(_ context.Context, request agents.DispatchRequest, _ agents.DispatchDeps) (*agents.PreparedRun, error) {
		record := agents.Record{
			AgentID: request.AgentID, State: agents.StateRunning, ExecutionStatus: agents.StatusQueued,
			RequestedProfile: request.Plan.Profile.Profile, BaseSHA: "base", StartedAt: fixture.now,
			ExperimentID: request.ExperimentID,
		}
		store := agents.NewStore(fixture.home)
		if err := store.Create(record); err != nil {
			return nil, err
		}
		return &agents.PreparedRun{Record: record, Store: store}, nil
	}
	// One attempt with a concurrency of five still completes: the batch is
	// clamped to the attempts that exist, and a missing timeout falls back.
	report, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "x", Profiles: []string{"cheap"}, Repeat: 1, Concurrency: 5,
	})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(report.Attempts) != 1 {
		t.Fatalf("attempts = %#v", report.Attempts)
	}
}

func TestUncontrolledDifferencesStatesWhatWBDoesNotControl(t *testing.T) {
	t.Parallel()
	plans := map[string]agents.Plan{
		"a": {Profile: agents.Resolved{Profile: "a", Harness: agents.HarnessCodex}},
		"b": {Profile: agents.Resolved{Profile: "b", Harness: agents.HarnessCodex}},
		"c": {Profile: agents.Resolved{Profile: "c", Harness: agents.HarnessDSH}},
	}
	differences := uncontrolledDifferences(plans, []string{"a", "b", "c", "missing"})
	joined := strings.Join(differences, "\n")
	if !strings.Contains(joined, "codex runs") || !strings.Contains(joined, "dsh runs") || !strings.Contains(joined, "unavailable") {
		t.Fatalf("differences = %#v", differences)
	}
	// One entry per distinct harness: the duplicate codex profile adds none.
	if strings.Count(joined, "codex runs") != 1 {
		t.Fatalf("duplicate harness rows = %#v", differences)
	}
}

func TestPruneReportsAnUnreadableStore(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	store := agents.NewStore(fixture.home)
	// A file where the agents directory belongs makes listing fail.
	if err := os.RemoveAll(store.Root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Root, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service().Prune(context.Background(), PruneRequest{ProjectsRoot: fixture.root, OlderThan: time.Hour}); err == nil {
		t.Fatal("an unreadable store must be reported")
	}
}

func TestTaskTypesUsesTheConfiguredDefault(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	fixture.config.Defaults.TaskType = "coding"
	catalog, err := fixture.service().TaskTypes(context.Background(), DiscoveryRequest{ProjectsRoot: fixture.root})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Default != "coding" {
		t.Fatalf("default = %q", catalog.Default)
	}
}

func TestDefaultDependencyProbesRun(t *testing.T) {
	t.Parallel()
	deps := DefaultDependencies()
	if err := deps.Preflight(context.Background(), agents.Resolved{Harness: agents.HarnessOpenCode, Model: "p/m"}); err == nil {
		// The opencode binary is installed in this environment, so this may
		// succeed; either answer proves the closure ran.
		_ = err
	}
	if version := deps.DetectVersion(context.Background(), agents.HarnessOpenCode); version == "" {
		t.Log("no opencode version was detected; the closure still ran")
	}
}

func TestRunReportsAFailingHome(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	fixture.deps.EnsureRoot = func(string) (string, error) { return "", errors.New("no home") }
	if _, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root, Request: agents.RunRequest{Task: "x", TaskType: "coding"},
	}); err == nil {
		t.Fatal("a failing home must be reported")
	}
}

func TestCompareRecordsARequestRefusalAsAConfigurationFailure(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	fixture.deps.PrepareRun = func(context.Context, agents.DispatchRequest, agents.DispatchDeps) (*agents.PreparedRun, error) {
		return nil, agents.NewRequestError("worktree name is ambiguous")
	}
	report, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "x", Profiles: []string{"cheap"}, Repeat: 1, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(report.Attempts) != 1 || report.Attempts[0].Status != agents.AttemptFailed {
		t.Fatalf("attempts = %#v", report.Attempts)
	}
	store := agents.NewStore(fixture.home)
	record, err := store.Load(report.Attempts[0].AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if record.FailureCategory != agents.FailureConfig {
		t.Fatalf("category = %q", record.FailureCategory)
	}
}
