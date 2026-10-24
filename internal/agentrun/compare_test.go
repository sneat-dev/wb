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
