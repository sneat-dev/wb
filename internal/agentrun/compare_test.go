package agentrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
)

// compareFixture prepares and starts executions in memory while keeping the
// durable documents real, so ordering, pinning and read-back can all be checked.
type compareFixture struct {
	*serviceFixture
	mutex        sync.Mutex
	order        []string
	baseByCall   map[int]string
	prepareCalls int
	prepareErrAt int
	startCalls   int
	startErrAt   int
	awaitErr     error
}

func newCompareFixture(t *testing.T) *compareFixture {
	t.Helper()
	fixture := &compareFixture{serviceFixture: newServiceFixture(t), baseByCall: map[int]string{}, prepareErrAt: -1, startErrAt: -1}
	fixture.deps.PrepareRun = func(_ context.Context, request agents.DispatchRequest, _ agents.DispatchDeps) (*agents.PreparedRun, error) {
		fixture.mutex.Lock()
		index := fixture.prepareCalls
		fixture.prepareCalls++
		fixture.order = append(fixture.order, "prepare:"+request.AgentID)
		base := "pinned-base"
		if override, ok := fixture.baseByCall[index]; ok {
			base = override
		}
		fixture.mutex.Unlock()
		if index == fixture.prepareErrAt {
			return nil, errors.New("worktree creation failed")
		}
		record := agents.Record{
			AgentID: request.AgentID, State: agents.StateRunning, ExecutionStatus: agents.StatusQueued,
			RequestedProfile: request.Plan.Profile.Profile, Resolved: request.Plan.Profile,
			TaskType: request.Plan.TaskType, Worktree: request.Worktree, WorktreeDir: "/fleet/" + request.Worktree,
			BaseSHA: base, StartedAt: fixture.now, ExperimentID: request.ExperimentID, RepeatIndex: request.RepeatIndex,
			TimeoutMS: request.Timeout.Milliseconds(), AcceptanceRequired: request.Plan.AcceptanceRequired,
			Validation: agents.ValidationRecord{Status: agents.ValidationSkipped},
		}
		store := agents.NewStore(fixture.home)
		if err := store.Create(record); err != nil {
			return nil, err
		}
		return &agents.PreparedRun{Record: record, Store: store}, nil
	}
	fixture.deps.StartRun = func(prepared *agents.PreparedRun, _ agents.DispatchDeps) (agents.Record, error) {
		fixture.mutex.Lock()
		fixture.order = append(fixture.order, "start:"+prepared.Record.AgentID)
		index := fixture.startCalls
		fixture.startCalls++
		fixture.mutex.Unlock()
		if index == fixture.startErrAt {
			record := prepared.Record
			record.State = agents.StateFailed
			record.ExecutionStatus = agents.StatusFailed
			record.Failure = "owner did not start"
			record.FinishedAt = fixture.now
			_ = agents.NewStore(fixture.home).Save(record)
			return record, errors.New("owner did not start")
		}
		record := prepared.Record
		record.State = agents.StateCompleted
		record.ExecutionStatus = agents.StatusCompleted
		record.FinishedAt = fixture.now
		record.DurationMS = 5000
		record.Telemetry = agents.Telemetry{Available: true, Source: agents.UsageFromEvents, Tokens: &agents.Tokens{
			Input: intPointer(10), Output: intPointer(2),
		}}
		record.Validation = agents.ValidationRecord{Status: agents.ValidationPassed}
		if err := agents.NewStore(fixture.home).Save(record); err != nil {
			return record, err
		}
		return record, nil
	}
	fixture.deps.Wait = func(context.Context, time.Duration) error { return fixture.awaitErr }
	return fixture
}

func TestCompareCreatesEveryWorktreeBeforeStartingAnyAttempt(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	report, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "compare this", Profiles: []string{"cheap", "op"},
		Repeat: 2, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(report.Attempts) != 4 || len(report.Runs) != 4 {
		t.Fatalf("attempts = %#v runs = %#v", report.Attempts, report.Runs)
	}
	firstStart := len(fixture.order)
	for index, entry := range fixture.order {
		if strings.HasPrefix(entry, "start:") && index < firstStart {
			firstStart = index
		}
	}
	for index, entry := range fixture.order {
		if index < firstStart && strings.HasPrefix(entry, "start:") {
			t.Fatalf("a worker started before every worktree existed: %#v", fixture.order)
		}
	}
	prepares := 0
	for _, entry := range fixture.order {
		if strings.HasPrefix(entry, "prepare:") {
			prepares++
		}
	}
	if prepares != 4 {
		t.Fatalf("prepares = %d (%#v)", prepares, fixture.order)
	}
	if report.BaseSHA != "pinned-base" {
		t.Fatalf("pinned base = %q", report.BaseSHA)
	}
	if len(report.Summaries) != 2 {
		t.Fatalf("summaries = %#v", report.Summaries)
	}
	if report.TaskSHA256 == "" || report.ExperimentID == "" {
		t.Fatalf("experiment identity = %#v", report.Experiment)
	}
	if len(report.UncontrolledDifferences) == 0 {
		t.Fatal("a comparison must state what it could not control")
	}

	// The experiment is durable and re-readable without running anything again.
	readBack, err := fixture.service().Experiment(context.Background(), ExperimentRequest{
		ProjectsRoot: fixture.root, ExperimentID: report.ExperimentID,
	})
	if err != nil {
		t.Fatalf("Experiment: %v", err)
	}
	if len(readBack.Runs) != 4 || readBack.BaseSHA != "pinned-base" {
		t.Fatalf("read back = %#v", readBack)
	}
}

func TestCompareNeverStartsAnAttemptWhoseBaseMoved(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	fixture.baseByCall[1] = "moved-base"
	report, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "compare this", Profiles: []string{"cheap", "op"}, Repeat: 1, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(report.Attempts) != 2 {
		t.Fatalf("attempts = %#v", report.Attempts)
	}
	mismatched := report.Attempts[1]
	if mismatched.Status != agents.AttemptBaseMismatch || !strings.Contains(mismatched.Failure, "pinned") {
		t.Fatalf("second attempt = %#v", mismatched)
	}
	started := 0
	for _, entry := range fixture.order {
		if strings.HasPrefix(entry, "start:") {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("a base-mismatched attempt must never start: %#v", fixture.order)
	}
	// The mismatched attempt still has its own record, marked failed.
	store := agents.NewStore(fixture.home)
	record, err := store.Load(mismatched.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if record.FailureCategory != agents.FailureBaseMismatch {
		t.Fatalf("record = %#v", record)
	}
}

func TestCompareSurvivesAPartialFailure(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	fixture.prepareErrAt = 1
	report, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "compare this", Profiles: []string{"cheap", "op"}, Repeat: 1, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(report.Runs) != 2 {
		t.Fatalf("every planned attempt must have a record: %#v", report.Attempts)
	}
	failed := report.Attempts[1]
	if failed.Status != agents.AttemptFailed || failed.Failure == "" {
		t.Fatalf("failed attempt = %#v", failed)
	}
	store := agents.NewStore(fixture.home)
	record, err := store.Load(failed.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if record.ExecutionStatus != agents.StatusFailed || record.FailureCategory != agents.FailureWorktree {
		t.Fatalf("failed record = %#v", record)
	}
}

func TestCompareFailedAttemptSnapshotsTheMandatoryPolicyByValue(t *testing.T) {
	t.Parallel()
	policy := agents.ExecutionPolicy{
		Mode: agents.ExecutionModeLinuxContainer, Image: "ghcr.io/acme/agent@sha256:" + strings.Repeat("a", 64),
		MemoryBytes: 8589934592, MemorySwapBytes: 8589934592, CPUCount: 2, PIDsLimit: 128, MaxParallel: 1,
	}
	expected := policy
	plan := agents.Plan{
		TaskType: "coding", Profile: agents.Resolved{Profile: "cheap", Harness: agents.HarnessCodex},
		Execution: &policy,
	}
	now := func() time.Time { return time.Unix(1700000000, 0).UTC() }
	record := failedAttemptRecord("exp-00000000000000000000000000000001", agents.Attempt{
		AgentID: "agt-00000000000000000000000000000001", Operation: "exp-op", RepeatIndex: 0,
	}, "do it", plan, errors.New("worktree creation failed"), now)
	if record.Execution == nil || *record.Execution != expected {
		t.Fatalf("failed attempt policy = %#v, want %#v", record.Execution, expected)
	}
	if record.Execution == plan.Execution {
		t.Fatal("the failed record must snapshot the policy, not alias the plan's pointer")
	}
	plan.Execution.MemoryBytes = 1
	plan.Execution.Image = "ghcr.io/acme/other@sha256:" + strings.Repeat("b", 64)
	if record.Execution.MemoryBytes != expected.MemoryBytes || record.Execution.Image != expected.Image {
		t.Fatalf("mutating the plan rewrote the failed record: %#v", record.Execution)
	}
}

func TestCompareKeepsTheMandatoryExecutionPolicyOnAFailedAttempt(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	policy := &agents.ExecutionPolicy{
		Mode: agents.ExecutionModeLinuxContainer, Image: "ghcr.io/acme/agent@sha256:" + strings.Repeat("a", 64),
		MemoryBytes: 8589934592, MemorySwapBytes: 8589934592, CPUCount: 2, PIDsLimit: 128, MaxParallel: 1,
	}
	fixture.config.Execution = policy
	// Only the contained harness is admitted under a mandatory policy, so this
	// comparison uses dsh and repeats it rather than mixing harnesses.
	fixture.config.Profiles["cheap"] = agents.Profile{Harness: agents.HarnessDSH, Model: "m"}
	fixture.config.Providers[agents.ProviderDshDefault] = agents.Provider{
		BaseURL: "https://api.deepseek.com", CredentialEnv: "WB_TEST_DEEPSEEK_OFFICIAL_KEY", WireAPI: agents.WireAPIChat,
	}
	fixture.prepareErrAt = 1
	report, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "compare this", Profiles: []string{"cheap"}, Repeat: 2, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	failed := report.Attempts[1]
	if failed.Status != agents.AttemptFailed {
		t.Fatalf("failed attempt = %#v", failed)
	}
	record, err := agents.NewStore(fixture.home).Load(failed.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Execution == nil || *record.Execution != *policy {
		t.Fatalf("failed attempt lost the mandatory policy: %#v", record.Execution)
	}
}

func TestCompareRefusesAConcurrentContainedComparisonBeforeCreatingAnything(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	policy := serviceExecutionPolicy()
	fixture.config.Execution = &policy
	fixture.config.Profiles["cheap"] = agents.Profile{Harness: agents.HarnessDSH, Model: "m"}
	fixture.config.Providers[agents.ProviderDshDefault] = agents.Provider{
		BaseURL: "https://api.deepseek.com", CredentialEnv: "WB_TEST_DEEPSEEK_OFFICIAL_KEY", WireAPI: agents.WireAPIChat,
	}
	_, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "compare this", Profiles: []string{"cheap"}, Repeat: 1, Concurrency: 2,
	})
	if err == nil || !strings.Contains(err.Error(), "--concurrency") {
		t.Fatalf("error = %v, want the serial containment refusal", err)
	}
	if fixture.prepareCalls != 0 || fixture.startCalls != 0 {
		t.Fatalf("a refused concurrent comparison prepared %d and started %d attempts", fixture.prepareCalls, fixture.startCalls)
	}
}

func TestCompareChecksEveryPlanInASerialContainedComparison(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	policy := serviceExecutionPolicy()
	fixture.config.Execution = &policy
	fixture.config.Profiles["cheap"] = agents.Profile{Harness: agents.HarnessDSH, Model: "m"}
	fixture.config.Providers[agents.ProviderDshDefault] = agents.Provider{
		BaseURL: "https://api.deepseek.com", CredentialEnv: "WB_TEST_DEEPSEEK_OFFICIAL_KEY", WireAPI: agents.WireAPIChat,
	}
	_, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "compare this", Profiles: []string{"cheap", "op"}, Repeat: 1, Concurrency: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "can execute only the") {
		t.Fatalf("error = %v, want the unsupported-harness refusal for the second plan", err)
	}
	if fixture.prepareCalls != 0 || fixture.startCalls != 0 {
		t.Fatalf("a refused comparison prepared %d and started %d attempts", fixture.prepareCalls, fixture.startCalls)
	}
}

func TestCompareContinuesAfterAStartFailure(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	fixture.startErrAt = 1
	report, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "compare this", Profiles: []string{"cheap", "op"}, Repeat: 1, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(report.Runs) != 2 {
		t.Fatalf("runs = %#v", report.Runs)
	}
	var failed, completed bool
	for _, run := range report.Runs {
		switch run.Status {
		case agents.StatusFailed:
			failed = true
		case agents.StatusCompleted:
			completed = true
		}
	}
	if !failed || !completed {
		t.Fatalf("a partial failure must preserve the other result: %#v", report.Runs)
	}
}

func TestCompareRefusesIncompleteOrContradictoryRequests(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	cases := []struct {
		name    string
		request CompareRequest
		want    string
	}{
		{"no task", CompareRequest{Profiles: []string{"cheap"}}, "task is required"},
		{"no profiles", CompareRequest{Task: "x"}, "at least one profile"},
		{"bad repeat", CompareRequest{Task: "x", Profiles: []string{"cheap"}, Repeat: 0}, "--repeat"},
		{"bad concurrency", CompareRequest{Task: "x", Profiles: []string{"cheap"}, Repeat: 1, Concurrency: 0}, "--concurrency"},
		{"unknown profile", CompareRequest{Task: "x", Profiles: []string{"nope"}, Repeat: 1, Concurrency: 1}, "unknown agent profile"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			testCase.request.ProjectsRoot = fixture.root
			if _, err := fixture.service().Compare(context.Background(), testCase.request); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestCompareRefusesAnUnusableProfileBeforeCreatingAnything(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	configCopy := fixture.config
	configCopy.Profiles["broken"] = agents.Profile{Harness: "nope", Model: "m"}
	fixture.deps.LoadConfig = func(string) (agents.Config, error) { return configCopy, nil }
	_, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "x", Profiles: []string{"cheap", "broken"}, Repeat: 1, Concurrency: 1,
	})
	if err == nil {
		t.Fatal("an unusable profile must fail the whole comparison")
	}
	if len(fixture.order) != 0 {
		t.Fatalf("nothing may be created: %#v", fixture.order)
	}
}

func TestComparePropagatesAnAwaitFailure(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	fixture.awaitErr = errors.New("wait unavailable")
	// A live run that never reaches a terminal state forces the awaiter to wait.
	fixture.deps.StartRun = func(prepared *agents.PreparedRun, _ agents.DispatchDeps) (agents.Record, error) {
		record := prepared.Record
		record.OwnerPID = os.Getpid()
		record.QueueOwnerPID = os.Getpid()
		record.StartedAt = time.Now()
		if err := agents.NewStore(fixture.home).Save(record); err != nil {
			return record, err
		}
		return record, nil
	}
	_, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "x", Profiles: []string{"cheap"}, Repeat: 1, Concurrency: 1,
	})
	if err == nil {
		t.Fatal("an unavailable wait must surface")
	}
}

func TestExperimentOperationNamesAreExperimentScoped(t *testing.T) {
	t.Parallel()
	first := experimentOperation("exp-0123456789abcdef0123456789abcdef", "cheap", 0)
	second := experimentOperation("exp-0123456789abcdef0123456789abcdef", "cheap", 1)
	if first == second || !strings.HasPrefix(first, "exp-01234567-cheap-r1") {
		t.Fatalf("operation names = %q %q", first, second)
	}
	if got := experimentOperation("exp-short", "op", 2); !strings.Contains(got, "op-r3") {
		t.Fatalf("short experiment name = %q", got)
	}
}

func TestCompareDisablesFallbackSoAProfileIsNeverSwapped(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	configCopy := fixture.config
	configCopy.Fallback = []string{"cheap"}
	configCopy.Profiles["broken"] = agents.Profile{Harness: "nope", Model: "m"}
	fixture.deps.LoadConfig = func(string) (agents.Config, error) { return configCopy, nil }
	_, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "x", Profiles: []string{"broken"}, Repeat: 1, Concurrency: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("a broken profile must not be replaced by a fallback: %v", err)
	}
}

func intPointer(value int) *int { return &value }

var _ = fmt.Sprintf

func TestCompareAwaitDeadlineUsesTheCompleteContainedLifetime(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	service := fixture.service()
	policy := serviceExecutionPolicy()
	record := agents.Record{
		AgentID: "id", Execution: &policy, TimeoutMS: time.Hour.Milliseconds(),
		Validation: agents.ValidationRecord{Steps: []agents.ValidationStepResult{{Name: "slow", Timeout: "5m"}}},
	}
	lifetime, err := agents.ContainedRecordLifetime(record)
	if err != nil {
		t.Fatalf("ContainedRecordLifetime: %v", err)
	}
	deadline, err := service.awaitDeadline(&agents.PreparedRun{Record: record})
	if err != nil {
		t.Fatalf("awaitDeadline: %v", err)
	}
	if got, want := deadline.Sub(fixture.now), lifetime+compareSlack; got != want {
		t.Fatalf("contained await deadline = %s, want the complete validated lifetime plus slack %s", got, want)
	}
	// The legacy host formula would have omitted the setup and cleanup budgets.
	legacy := time.Hour + agents.ValidationBudget(record.Validation.Steps) + compareSlack
	if got := deadline.Sub(fixture.now); got <= legacy {
		t.Fatalf("contained await deadline = %s, want it above the legacy host %s", got, legacy)
	}
	// A slow but legitimate setup and a long run must still be awaited for the
	// complete validated total rather than cut off early.
	slow := agents.Record{
		AgentID: "slow", Execution: &policy,
		TimeoutMS:  (3*time.Hour + 30*time.Minute).Milliseconds(),
		Validation: agents.ValidationRecord{Steps: []agents.ValidationStepResult{{Name: "slow", Timeout: "15m"}}},
	}
	slowLifetime, err := agents.ContainedRecordLifetime(slow)
	if err != nil {
		t.Fatalf("ContainedRecordLifetime for a slow run: %v", err)
	}
	slowDeadline, err := service.awaitDeadline(&agents.PreparedRun{Record: slow})
	if err != nil {
		t.Fatalf("awaitDeadline for a slow run: %v", err)
	}
	if got, want := slowDeadline.Sub(fixture.now), slowLifetime+compareSlack; got != want {
		t.Fatalf("slow contained await deadline = %s, want the complete validated total plus slack %s", got, want)
	}
}

func TestCompareAwaitDeadlineKeepsTheLegacyHostFormula(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	service := fixture.service()
	record := agents.Record{
		AgentID: "id", TimeoutMS: time.Hour.Milliseconds(),
		Validation: agents.ValidationRecord{Steps: []agents.ValidationStepResult{{Name: "slow", Timeout: "5m"}}},
	}
	deadline, err := service.awaitDeadline(&agents.PreparedRun{Record: record})
	if err != nil {
		t.Fatalf("awaitDeadline: %v", err)
	}
	want := time.Hour + 5*time.Minute + compareSlack
	if got := deadline.Sub(fixture.now); got != want {
		t.Fatalf("legacy await deadline = %s, want %s", got, want)
	}
}

func TestCompareAwaitDeadlineRefusesAnInvalidContainedLifetime(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	service := fixture.service()
	policy := serviceExecutionPolicy()
	cases := map[string]int64{
		"unbounded":                   0,
		"overflow":                    int64(1<<63 - 1),
		"above the four-hour maximum": int64((4*time.Hour + time.Hour) / time.Millisecond),
	}
	for name, timeoutMS := range cases {
		name, timeoutMS := name, timeoutMS
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			record := agents.Record{AgentID: "id", Execution: &policy, TimeoutMS: timeoutMS}
			if _, err := service.awaitDeadline(&agents.PreparedRun{Record: record}); err == nil {
				t.Fatalf("an invalid contained bound (%d ms) must be refused", timeoutMS)
			}
		})
	}
}

func TestCompareRefusesAnInvalidContainedLifetimeBeforeAnyWorktree(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	policy := serviceExecutionPolicy()
	fixture.config.Execution = &policy
	fixture.config.Profiles["cheap"] = agents.Profile{Harness: agents.HarnessDSH, Model: "m"}
	fixture.config.Providers[agents.ProviderDshDefault] = agents.Provider{
		BaseURL: "https://api.deepseek.com", CredentialEnv: "WB_TEST_DEEPSEEK_OFFICIAL_KEY", WireAPI: agents.WireAPIChat,
	}
	_, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "compare this", Profiles: []string{"cheap"}, Repeat: 1, Concurrency: 1,
		Timeout: 5 * time.Hour, TimeoutSet: true,
	})
	if err == nil || !strings.Contains(err.Error(), "contained comparison plan") {
		t.Fatalf("error = %v, want the contained lifetime refusal", err)
	}
	if fixture.prepareCalls != 0 || fixture.startCalls != 0 {
		t.Fatalf("a refused contained comparison prepared %d and started %d attempts", fixture.prepareCalls, fixture.startCalls)
	}
}

func TestCompareRefusesAnUnusablePreparedRecordBoundBeforeAwaiting(t *testing.T) {
	t.Parallel()
	fixture := newCompareFixture(t)
	policy := serviceExecutionPolicy()
	fixture.config.Execution = &policy
	fixture.config.Profiles["cheap"] = agents.Profile{Harness: agents.HarnessDSH, Model: "m"}
	fixture.config.Providers[agents.ProviderDshDefault] = agents.Provider{
		BaseURL: "https://api.deepseek.com", CredentialEnv: "WB_TEST_DEEPSEEK_OFFICIAL_KEY", WireAPI: agents.WireAPIChat,
	}
	prepare := fixture.deps.PrepareRun
	fixture.deps.PrepareRun = func(ctx context.Context, request agents.DispatchRequest, deps agents.DispatchDeps) (*agents.PreparedRun, error) {
		prepared, err := prepare(ctx, request, deps)
		if err != nil {
			return nil, err
		}
		// A prepared record must still carry a usable finite bound; a corrupted
		// one is refused at the await boundary rather than waited on forever.
		prepared.Record.Execution = &policy
		prepared.Record.TimeoutMS = int64((4*time.Hour + time.Hour) / time.Millisecond)
		return prepared, nil
	}
	_, err := fixture.service().Compare(context.Background(), CompareRequest{
		ProjectsRoot: fixture.root, Task: "compare this", Profiles: []string{"cheap"}, Repeat: 1, Concurrency: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "bound the await for contained attempt") {
		t.Fatalf("error = %v, want the unusable prepared bound refusal", err)
	}
}
