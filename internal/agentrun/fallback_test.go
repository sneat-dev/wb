package agentrun

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
)

// fallbackFixture scripts the outcome of each attempted execution.
type fallbackFixture struct {
	*serviceFixture
	outcomes []string
	started  []string
	profiles []string
	prepare  int
	stderr   bytes.Buffer
}

// attempt outcomes the fixture can produce.
const (
	outcomeCompleted      = "completed"
	outcomeProviderFailed = "provider"
	outcomeProviderDirty  = "provider-dirty"
	outcomeValidation     = "validation"
)

func newFallbackFixture(t *testing.T, outcomes ...string) *fallbackFixture {
	t.Helper()
	base := newServiceFixture(t)
	fixture := &fallbackFixture{serviceFixture: base, outcomes: outcomes}
	fixture.deps.PrepareRun = func(_ context.Context, request agents.DispatchRequest, _ agents.DispatchDeps) (*agents.PreparedRun, error) {
		if fixture.prepare >= len(fixture.outcomes) {
			return nil, errors.New("unexpected extra attempt")
		}
		fixture.profiles = append(fixture.profiles, request.Plan.Profile.Profile)
		agentID := request.AgentID
		if agentID == "" {
			agentID, _ = agents.NewID()
		}
		record := agents.Record{
			AgentID: agentID, State: agents.StateRunning, ExecutionStatus: agents.StatusQueued,
			RequestedProfile: request.Plan.Profile.Profile, Resolved: request.Plan.Profile,
			Task: request.Task, TaskType: request.Plan.TaskType, Worktree: request.Worktree,
			WorktreeDir: "/fleet/" + request.Worktree, WorktreeMode: request.Mode, BaseSHA: "base",
			StartedAt: fixture.now, QueueOwnerPID: 1, ExperimentID: request.ExperimentID,
			RetryOf: request.RetryOf, FallbackFrom: request.FallbackFrom, FallbackReason: request.FallbackReason,
			TimeoutMS: request.Timeout.Milliseconds(),
		}
		store := agents.NewStore(fixture.home)
		if err := store.Create(record); err != nil {
			return nil, err
		}
		fixture.prepare++
		return &agents.PreparedRun{Record: record, Store: store}, nil
	}
	fixture.deps.StartRun = func(prepared *agents.PreparedRun, _ agents.DispatchDeps) (agents.Record, error) {
		index := len(fixture.started)
		fixture.started = append(fixture.started, prepared.Record.AgentID)
		record := prepared.Record
		record.State = agents.StateCompleted
		record.ExecutionStatus = agents.StatusCompleted
		record.FinishedAt = fixture.now
		switch fixture.outcomes[index] {
		case outcomeProviderFailed:
			record.State = agents.StateFailed
			record.ExecutionStatus = agents.StatusFailed
			record.FailureCategory = agents.FailureProvider
			record.Failure = "provider.no-route: model unavailable"
		case outcomeProviderDirty:
			record.State = agents.StateFailed
			record.ExecutionStatus = agents.StatusFailed
			record.FailureCategory = agents.FailureProvider
			record.Changes = &agents.ChangeSummary{FilesChanged: 1, Files: []string{"a.go"}}
		case outcomeValidation:
			record.Validation = agents.ValidationRecord{Status: agents.ValidationFailed}
		}
		if err := agents.NewStore(fixture.home).Save(record); err != nil {
			return record, err
		}
		return record, nil
	}
	fixture.deps.Wait = func(context.Context, time.Duration) error { return nil }
	return fixture
}

// fallbackConfig is one task type whose provider chain follows the operator's
// preferred order: OpenCode first, then the DeepSeek harness, then Codex.
func fallbackConfig(t *testing.T, fallbackOn string) agents.Config {
	t.Helper()
	body := `
agents:
  profiles:
    oc-go: { harness: opencode, model: opencode-go/deepseek-v4.1-flash }
    deepseek: { harness: dsh, model: deepseek-v4-flash }
    codex: { harness: codex, provider: deepseek, model: deepseek-v4.1-flash }
  task_types:
    coding:
      profile: oc-go
      fallback: [deepseek, codex]
`
	if fallbackOn != "" {
		body += "      fallback_on: [" + fallbackOn + "]\n"
	}
	config, err := agents.LoadConfigFile(writeServiceConfig(t, body))
	if err != nil {
		t.Fatalf("load fallback config: %v", err)
	}
	return config
}

func TestRunFailsOverToTheNextProfileWhenTheProviderIsDown(t *testing.T) {
	t.Parallel()
	fixture := newFallbackFixture(t, outcomeProviderFailed, outcomeCompleted)
	fixture.config = fallbackConfig(t, "preflight, runtime")
	result, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root, Stderr: &fixture.stderr,
		Request: agents.RunRequest{Task: "do it", TaskType: "coding"},
		Wait:    time.Minute,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fixture.started) != 2 {
		t.Fatalf("attempts = %#v", fixture.started)
	}
	if len(fixture.profiles) != 2 || fixture.profiles[0] != "oc-go" || fixture.profiles[1] != "deepseek" {
		t.Fatalf("profiles = %#v", fixture.profiles)
	}
	if result.Status != agents.StatusCompleted || result.RequestedProfile != "deepseek" {
		t.Fatalf("result = %#v", result)
	}
	if result.RetryOf != fixture.started[0] || result.FallbackFrom != "oc-go" || !strings.Contains(result.FallbackReason, "provider.no-route") {
		t.Fatalf("retry provenance = %#v", result)
	}
	if !strings.Contains(fixture.stderr.String(), "falling back to profile deepseek") {
		t.Fatalf("fallback was not logged: %q", fixture.stderr.String())
	}
	// The retry is a new execution in its own worktree.
	store := agents.NewStore(fixture.home)
	second, err := store.Load(result.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if second.WorktreeMode != agents.ModeAuto {
		t.Fatalf("a retry must get a fresh worktree: %#v", second)
	}
}

func TestRunDoesNotFailOverWithoutTheRuntimeTrigger(t *testing.T) {
	t.Parallel()
	fixture := newFallbackFixture(t, outcomeProviderFailed)
	fixture.config = fallbackConfig(t, "")
	result, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root, Stderr: &fixture.stderr,
		Request: agents.RunRequest{Task: "do it", TaskType: "coding"},
		Wait:    time.Minute,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fixture.started) != 1 || result.Status != agents.StatusFailed {
		t.Fatalf("started = %#v result = %#v", fixture.started, result)
	}
	if fixture.stderr.Len() != 0 {
		t.Fatalf("no fallback should be logged: %q", fixture.stderr.String())
	}
}

func TestRunDoesNotFailOverAnExplicitProfileOrASharedCheckout(t *testing.T) {
	t.Parallel()
	explicit := newFallbackFixture(t, outcomeProviderFailed)
	explicit.config = fallbackConfig(t, "preflight, runtime")
	if _, err := explicit.service().Run(context.Background(), RunRequest{
		ProjectsRoot: explicit.root,
		Request:      agents.RunRequest{Task: "do it", TaskType: "coding", Profile: "oc-go"},
		Wait:         time.Minute,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(explicit.started) != 1 {
		t.Fatalf("an explicit profile must never be swapped: %#v", explicit.started)
	}

	shared := newFallbackFixture(t, outcomeProviderFailed)
	shared.config = fallbackConfig(t, "preflight, runtime")
	if _, err := shared.service().Run(context.Background(), RunRequest{
		ProjectsRoot: shared.root,
		Request:      agents.RunRequest{Task: "do it", TaskType: "coding", WorktreeMode: agents.ModeCurrent},
		Wait:         time.Minute,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(shared.started) != 1 {
		t.Fatalf("a shared checkout must not be retried: %#v", shared.started)
	}
}

func TestRunDoesNotFailOverAnAttemptThatProducedWorkOrFailedValidation(t *testing.T) {
	t.Parallel()
	dirty := newFallbackFixture(t, outcomeProviderDirty)
	dirty.config = fallbackConfig(t, "preflight, runtime")
	if _, err := dirty.service().Run(context.Background(), RunRequest{
		ProjectsRoot: dirty.root, Request: agents.RunRequest{Task: "do it", TaskType: "coding"}, Wait: time.Minute,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(dirty.started) != 1 {
		t.Fatalf("work already on disk must not be retried elsewhere: %#v", dirty.started)
	}

	validation := newFallbackFixture(t, outcomeValidation)
	validation.config = fallbackConfig(t, "preflight, runtime")
	result, err := validation.service().Run(context.Background(), RunRequest{
		ProjectsRoot: validation.root, Request: agents.RunRequest{Task: "do it", TaskType: "coding"}, Wait: time.Minute,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(validation.started) != 1 || result.Status != agents.StatusCompleted {
		t.Fatalf("validation failure must not fail over: %#v %#v", validation.started, result)
	}
}

func TestRunWalksTheWholeChainAndStopsWhenItIsExhausted(t *testing.T) {
	t.Parallel()
	fixture := newFallbackFixture(t, outcomeProviderFailed, outcomeProviderFailed, outcomeProviderFailed)
	fixture.config = fallbackConfig(t, "preflight, runtime")
	result, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root, Stderr: &fixture.stderr,
		Request: agents.RunRequest{Task: "do it", TaskType: "coding"},
		Wait:    time.Minute,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fixture.started) != 3 || result.RequestedProfile != "codex" {
		t.Fatalf("started = %#v result = %#v", fixture.started, result)
	}
	if result.RetryOf != fixture.started[1] || result.FallbackFrom != "deepseek" {
		t.Fatalf("last retry = %#v", result)
	}
	if result.Status != agents.StatusFailed {
		t.Fatalf("an exhausted chain must return the last failure: %#v", result)
	}
}

func TestRunSkipsAFallbackWhosePreflightFails(t *testing.T) {
	t.Parallel()
	fixture := newFallbackFixture(t, outcomeProviderFailed, outcomeCompleted)
	fixture.config = fallbackConfig(t, "preflight, runtime")
	fixture.deps.Preflight = func(_ context.Context, resolved agents.Resolved) error {
		if resolved.Profile == "deepseek" {
			return errors.New("dsh is not installed")
		}
		return nil
	}
	result, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root, Stderr: &fixture.stderr,
		Request: agents.RunRequest{Task: "do it", TaskType: "coding"},
		Wait:    time.Minute,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fixture.started) != 2 || result.RequestedProfile != "codex" {
		t.Fatalf("started = %#v result = %#v", fixture.started, result)
	}
	if !strings.Contains(fixture.stderr.String(), "dsh is not installed") {
		t.Fatalf("a skipped fallback must be logged: %q", fixture.stderr.String())
	}
}

func TestRunReportsAStartFailureOfTheFallbackAttempt(t *testing.T) {
	t.Parallel()
	fixture := newFallbackFixture(t, outcomeProviderFailed, outcomeCompleted)
	fixture.config = fallbackConfig(t, "preflight, runtime")
	fixture.deps.StartRun = func(prepared *agents.PreparedRun, _ agents.DispatchDeps) (agents.Record, error) {
		fixture.started = append(fixture.started, prepared.Record.AgentID)
		return prepared.Record, errors.New("owner did not start")
	}
	if _, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root, Stderr: &fixture.stderr,
		Request: agents.RunRequest{Task: "do it", TaskType: "coding"},
		Wait:    time.Minute,
	}); err == nil {
		t.Fatal("a failed fallback launch must be reported")
	}
}

func TestRuntimeRetryableOnlyCoversInfrastructureFailuresWithoutWork(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		result agents.Result
		want   bool
	}{
		"provider":         {agents.Result{Status: agents.StatusFailed, FailureCategory: agents.FailureProvider}, true},
		"credential":       {agents.Result{Status: agents.StatusFailed, FailureCategory: agents.FailureCredential}, true},
		"missing harness":  {agents.Result{Status: agents.StatusFailed, FailureCategory: agents.FailureHarnessMiss}, true},
		"validation":       {agents.Result{Status: agents.StatusCompleted, FailureCategory: agents.FailureValidation}, false},
		"cancelled":        {agents.Result{Status: agents.StatusCancelled, FailureCategory: agents.FailureCancelled}, false},
		"timeout":          {agents.Result{Status: agents.StatusTimedOut, FailureCategory: agents.FailureTimeout}, false},
		"harness failed":   {agents.Result{Status: agents.StatusFailed, FailureCategory: agents.FailureHarness}, false},
		"work on disk":     {agents.Result{Status: agents.StatusFailed, FailureCategory: agents.FailureProvider, Changes: &agents.ChangeSummary{Commits: 1}}, false},
		"not yet terminal": {agents.Result{Status: agents.StatusRunning}, false},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := runtimeRetryable(testCase.result); got != testCase.want {
				t.Fatalf("runtimeRetryable(%s) = %v", name, got)
			}
		})
	}
}

func TestRunReportsAnAwaitFailureWithAndWithoutFailover(t *testing.T) {
	t.Parallel()
	for _, trigger := range []string{"", "preflight, runtime"} {
		fixture := newFallbackFixture(t, outcomeProviderFailed)
		fixture.config = fallbackConfig(t, trigger)
		fixture.deps.StartRun = func(prepared *agents.PreparedRun, _ agents.DispatchDeps) (agents.Record, error) {
			// Save a record that never reaches a terminal state.
			record := prepared.Record
			record.OwnerPID = 1
			record.QueueOwnerPID = 1
			record.StartedAt = time.Now()
			if err := agents.NewStore(fixture.home).Save(record); err != nil {
				return record, err
			}
			return record, nil
		}
		fixture.deps.Wait = func(context.Context, time.Duration) error { return errors.New("wait unavailable") }
		if _, err := fixture.service().Run(context.Background(), RunRequest{
			ProjectsRoot: fixture.root, Stderr: &fixture.stderr,
			Request: agents.RunRequest{Task: "do it", TaskType: "coding"}, Wait: time.Minute,
		}); err == nil {
			t.Fatalf("trigger %q: an unavailable wait must surface", trigger)
		}
	}
}

func TestRunReportsAConfigurationFailureDuringFailover(t *testing.T) {
	t.Parallel()
	fixture := newFallbackFixture(t, outcomeProviderFailed, outcomeCompleted)
	fixture.config = fallbackConfig(t, "preflight, runtime")
	calls := 0
	load := fixture.deps.LoadConfig
	fixture.deps.LoadConfig = func(path string) (agents.Config, error) {
		calls++
		if calls > 1 {
			return agents.Config{}, errors.New("configuration changed")
		}
		return load(path)
	}
	if _, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root, Stderr: &fixture.stderr,
		Request: agents.RunRequest{Task: "do it", TaskType: "coding"}, Wait: time.Minute,
	}); err == nil {
		t.Fatal("a configuration failure during failover must surface")
	}
}

func TestRunReportsAFailedFallbackLaunch(t *testing.T) {
	t.Parallel()
	fixture := newFallbackFixture(t, outcomeProviderFailed, outcomeCompleted)
	fixture.config = fallbackConfig(t, "preflight, runtime")
	start := fixture.deps.StartRun
	calls := 0
	fixture.deps.StartRun = func(prepared *agents.PreparedRun, deps agents.DispatchDeps) (agents.Record, error) {
		calls++
		if calls == 2 {
			return prepared.Record, errors.New("owner did not start")
		}
		return start(prepared, deps)
	}
	if _, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root, Stderr: &fixture.stderr,
		Request: agents.RunRequest{Task: "do it", TaskType: "coding"}, Wait: time.Minute,
	}); err == nil {
		t.Fatal("a failed fallback launch must surface")
	}
}

func TestRunFailsOverWithoutAStderrStream(t *testing.T) {
	t.Parallel()
	fixture := newFallbackFixture(t, outcomeProviderFailed, outcomeCompleted)
	fixture.config = fallbackConfig(t, "preflight, runtime")
	result, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root,
		Request:      agents.RunRequest{Task: "do it", TaskType: "coding"}, Wait: time.Minute,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agents.StatusCompleted || len(fixture.started) != 2 {
		t.Fatalf("result = %#v started = %#v", result, fixture.started)
	}
}

func TestRunReportsAFallbackProfileThatNoLongerResolves(t *testing.T) {
	t.Parallel()
	fixture := newFallbackFixture(t, outcomeProviderFailed, outcomeCompleted)
	fixture.config = fallbackConfig(t, "preflight, runtime")
	calls := 0
	load := fixture.deps.LoadConfig
	fixture.deps.LoadConfig = func(path string) (agents.Config, error) {
		calls++
		if calls == 1 {
			return load(path)
		}
		// The second read no longer knows the configured fallback profile.
		stale, err := load(path)
		if err != nil {
			return agents.Config{}, err
		}
		delete(stale.Profiles, "deepseek")
		return stale, nil
	}
	if _, err := fixture.service().Run(context.Background(), RunRequest{
		ProjectsRoot: fixture.root, Stderr: &fixture.stderr,
		Request: agents.RunRequest{Task: "do it", TaskType: "coding"}, Wait: time.Minute,
	}); err == nil {
		t.Fatal("a fallback profile that no longer resolves must surface")
	}
}
