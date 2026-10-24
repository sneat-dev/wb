package agentrun

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
)

// serviceFixture is a fully faked agentrun service: storage is real (so durable
// documents are exercised), while dispatch, spawning and the clock are injected.
type serviceFixture struct {
	root    string
	home    string
	config  agents.Config
	deps    Dependencies
	now     time.Time
	started []string
	prepare agents.DispatchRequest
	err     error
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	root := t.TempDir()
	fixture := &serviceFixture{root: root, home: filepath.Join(root, ".wb"), now: time.Unix(1000, 0).UTC()}
	if err := os.MkdirAll(fixture.home, 0o700); err != nil {
		t.Fatal(err)
	}
	config, err := agents.LoadConfigFile(writeServiceConfig(t, `
agents:
  profiles:
    cheap:
      harness: codex
      provider: deepseek
      model: deepseek-v4.1-flash
    op:
      harness: opencode
      model: opencode-go/deepseek-v4.1-flash
  task_types:
    coding:
      profile: cheap
      validate: [fast]
  validations:
    fast:
      - { name: noop, argv: [/usr/bin/true] }
`))
	if err != nil {
		t.Fatal(err)
	}
	fixture.config = config
	deps := DefaultDependencies()
	deps.Root = func(string) (string, error) { return fixture.home, nil }
	deps.EnsureRoot = func(string) (string, error) { return fixture.home, nil }
	deps.ConfigPath = func() string { return "/private/wb.yaml" }
	deps.LoadConfig = func(string) (agents.Config, error) { return fixture.config, nil }
	deps.Now = func() time.Time { return fixture.now }
	deps.Preflight = func(context.Context, agents.Resolved) error { return nil }
	deps.DetectVersion = func(context.Context, string) string { return "test-version" }
	deps.NewAgentID = func() (string, error) { return agents.NewID() }
	deps.NewExperimentID = func() string { return "exp-00000000000000000000000000000001" }
	deps.Diff = func(context.Context, agents.Record, bool, agents.TargetDeps) (string, error) {
		return "no tracked change", nil
	}
	deps.PrepareRun = func(_ context.Context, request agents.DispatchRequest, dispatchDeps agents.DispatchDeps) (*agents.PreparedRun, error) {
		fixture.prepare = request
		if fixture.err != nil {
			return nil, fixture.err
		}
		agentID := request.AgentID
		if agentID == "" {
			agentID, _ = agents.NewID()
		}
		record := agents.Record{
			AgentID: agentID, State: agents.StateRunning, ExecutionStatus: agents.StatusQueued,
			RequestedProfile: "cheap", Resolved: agents.Resolved{Profile: "cheap", Harness: agents.HarnessCodex, Model: "m"},
			Task: request.Task, TaskType: "coding", Worktree: "op", WorktreeDir: request.Worktree,
			BaseSHA: "base123", StartedAt: fixture.now, ExperimentID: request.ExperimentID,
			RepeatIndex: request.RepeatIndex, TimeoutMS: request.Timeout.Milliseconds(),
		}
		store := agents.NewStore(fixture.home)
		if err := store.Create(record); err != nil {
			return nil, err
		}
		return &agents.PreparedRun{Record: record}, nil
	}
	deps.StartRun = func(prepared *agents.PreparedRun, _ agents.DispatchDeps) (agents.Record, error) {
		fixture.started = append(fixture.started, prepared.Record.AgentID)
		record := prepared.Record
		record.State = agents.StateCompleted
		record.ExecutionStatus = agents.StatusCompleted
		record.FinishedAt = fixture.now
		store := agents.NewStore(fixture.home)
		if err := store.Save(record); err != nil {
			return record, err
		}
		return record, nil
	}
	fixture.deps = deps
	return fixture
}

func writeServiceConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunResolvesAndStartsOneExecution(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	result, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root, Stderr: &bytes.Buffer{},
		Request: agents.RunRequest{Task: "do it", TaskType: "coding", WorktreeMode: agents.ModeAuto, Worktree: "op"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.AgentID == "" || !result.Terminal || result.Status != agents.StatusCompleted {
		t.Fatalf("result = %#v", result)
	}
	if fixture.prepare.Plan == nil || fixture.prepare.Plan.TaskType != "coding" {
		t.Fatalf("plan was not passed to dispatch: %#v", fixture.prepare)
	}
	if len(fixture.prepare.Plan.ValidationSteps) != 1 {
		t.Fatalf("validation steps = %#v", fixture.prepare.Plan.ValidationSteps)
	}
	if fixture.prepare.Timeout != 30*time.Minute {
		t.Fatalf("timeout = %v", fixture.prepare.Timeout)
	}
	if len(fixture.started) != 1 {
		t.Fatalf("started = %#v", fixture.started)
	}
}

//nolint:paralleltest // the test changes the process working directory
func TestRunUsesTheCurrentDirectoryForCurrentMode(t *testing.T) {
	fixture := newServiceFixture(t)
	worktree := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(worktree); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	if _, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root,
		Request:      agents.RunRequest{Task: "review this", TaskType: "coding", WorktreeMode: agents.ModeCurrent},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// An empty current-mode target is the checkout the caller stands in, which
	// the agent layer resolves; the service passes it through unchanged.
	if fixture.prepare.Worktree != "" || fixture.prepare.Mode != agents.ModeCurrent {
		t.Fatalf("current mode request = %#v", fixture.prepare)
	}
}

func TestRunRefusesAnEmptyTaskAndSurfacesPlanAndPrepareFailures(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	if _, err := fixture.service().Run(context.Background(), RunRequest{ProjectsRoot: fixture.root}); err == nil || !agents.IsRequestError(err) {
		t.Fatalf("empty task = %v", err)
	}
	if _, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root, Request: agents.RunRequest{Task: "x", Profile: "missing"},
	}); err == nil {
		t.Fatal("an unknown profile must fail the run")
	}
	fixture.err = errors.New("worktree unavailable")
	if _, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root, Request: agents.RunRequest{Task: "x", TaskType: "coding"},
	}); err == nil {
		t.Fatal("a preparation failure must be reported")
	}
}

func TestPlanNeverProbesAHarness(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	probed := false
	fixture.deps.Preflight = func(context.Context, agents.Resolved) error { probed = true; return nil }
	plan, err := fixture.service().Plan(context.Background(), PlanRequest{
		ProjectsRoot: fixture.root, Request: agents.RunRequest{Task: "x", TaskType: "coding"},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Profile.Profile != "cheap" || plan.TaskType != "coding" {
		t.Fatalf("plan = %#v", plan)
	}
	if probed {
		t.Fatal("a dry run must start no process, including a preflight probe")
	}
}

func TestProfilesCatalogReportsCapabilitiesAndUnusableProfiles(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	fixture.config.Profiles["broken"] = agents.Profile{Harness: "nope", Model: "m"}
	catalog, err := fixture.service().Profiles(context.Background(), DiscoveryRequest{ProjectsRoot: fixture.root})
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(catalog.Profiles) != 3 {
		t.Fatalf("profiles = %#v", catalog.Profiles)
	}
	if len(catalog.Harnesses) != 3 {
		t.Fatalf("harnesses = %#v", catalog.Harnesses)
	}
	var broken *ProfileEntry
	for index := range catalog.Profiles {
		if catalog.Profiles[index].Name == "broken" {
			broken = &catalog.Profiles[index]
		}
	}
	if broken == nil || broken.Usable || broken.Problem == "" {
		t.Fatalf("broken profile = %#v", broken)
	}
	for _, entry := range catalog.Profiles {
		if entry.Name == "op" && entry.Capabilities.Harness != agents.HarnessOpenCode {
			t.Fatalf("op capabilities = %#v", entry.Capabilities)
		}
	}
	if len(catalog.Providers) == 0 {
		t.Fatal("providers must be listed")
	}
}

func TestTaskTypesCatalogMarksBuiltinsAndExpandsValidation(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	catalog, err := fixture.service().TaskTypes(context.Background(), DiscoveryRequest{ProjectsRoot: fixture.root})
	if err != nil {
		t.Fatalf("TaskTypes: %v", err)
	}
	if catalog.Default != "coding" {
		t.Fatalf("default = %q", catalog.Default)
	}
	seenCoding := false
	for _, entry := range catalog.TaskTypes {
		if entry.Name != "coding" {
			continue
		}
		seenCoding = true
		if entry.Builtin {
			t.Fatal("a configured coding task type overrides the built-in policy")
		}
		if len(entry.Validations["fast"]) != 1 {
			t.Fatalf("validations = %#v", entry.Validations)
		}
	}
	if !seenCoding {
		t.Fatalf("coding missing from %#v", catalog.TaskTypes)
	}
	builtin := false
	for _, entry := range catalog.TaskTypes {
		if entry.Name == "debugging" && entry.Builtin {
			builtin = true
		}
	}
	if !builtin {
		t.Fatal("built-in task types must be listed")
	}
}

func TestDiffReviewExperimentAndPruneRouteThroughTheStore(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	service := fixture.service()
	store := agents.NewStore(fixture.home)
	id, _ := agents.NewID()
	record := agents.Record{
		AgentID: id, State: agents.StateCompleted, ExecutionStatus: agents.StatusCompleted,
		RequestedProfile: "cheap", WorktreeDir: fixture.root, BaseSHA: "base",
		StartedAt: fixture.now.Add(-90 * 24 * time.Hour), FinishedAt: fixture.now,
		AcceptanceRequired: true,
		Validation:         agents.ValidationRecord{Status: agents.ValidationPassed},
	}
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}

	diff, err := service.Diff(context.Background(), DiffRequest{ProjectsRoot: fixture.root, AgentID: id, Patch: true})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "no tracked change") {
		t.Fatalf("diff = %q", diff)
	}

	if _, err := service.Review(context.Background(), ReviewRequest{ProjectsRoot: fixture.root, AgentID: id, Status: agents.ReviewAccepted}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	review, err := store.LoadReview(id)
	if err != nil || review.Status != agents.ReviewAccepted || review.Evaluator != "operator" {
		t.Fatalf("review = %#v %v", review, err)
	}
	if _, err := service.Review(context.Background(), ReviewRequest{ProjectsRoot: fixture.root, AgentID: id, Status: "maybe"}); err == nil {
		t.Fatal("an invalid review status must be refused")
	}
	if _, err := service.Review(context.Background(), ReviewRequest{ProjectsRoot: fixture.root, AgentID: "agt-00000000000000000000000000000000", Status: agents.ReviewAccepted}); !agents.IsUnknownAgent(err) {
		t.Fatalf("unknown review target = %v", err)
	}
	if _, err := service.Experiment(context.Background(), ExperimentRequest{ProjectsRoot: fixture.root, ExperimentID: "exp-00000000000000000000000000000000"}); !agents.IsUnknownExperiment(err) {
		t.Fatalf("unknown experiment = %v", err)
	}
}

func TestReviewRefusesARunThatHasNotFinished(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	store := agents.NewStore(fixture.home)
	id, _ := agents.NewID()
	if err := store.Create(agents.Record{AgentID: id, State: agents.StateRunning, OwnerPID: os.Getpid(), StartedAt: fixture.now}); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.service().Review(context.Background(), ReviewRequest{ProjectsRoot: fixture.root, AgentID: id, Status: agents.ReviewAccepted})
	if err == nil || !agents.IsRequestError(err) {
		t.Fatalf("running run review = %v", err)
	}
}

func TestServiceHelpersExposeTheStoreAndAwaiter(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	if _, err := fixture.service().store("/broken"); err != nil {
		t.Fatalf("store: %v", err)
	}
	fixture.deps.Root = func(string) (string, error) { return "", errors.New("root failed") }
	if _, err := New(fixture.deps).List(context.Background(), ListRequest{ProjectsRoot: fixture.root}); err == nil {
		t.Fatal("a failing root must be reported")
	}
}

func (fixture *serviceFixture) service() *Service { return New(fixture.deps) }
