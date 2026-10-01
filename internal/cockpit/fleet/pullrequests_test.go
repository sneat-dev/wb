package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/prsnapshot"
	"github.com/sneat-dev/wb/internal/prwatch"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// observeStep is one scripted answer of the fake observer.
type observeStep struct {
	outcome prwatch.Outcome
	err     error
	panics  bool
}

// fakeObserver is a PullRequestObserver that never reaches GitHub: it answers
// from a script per pull request number (repeating the last step), records the
// numbers it was asked about, and can hold every observation until released.
type fakeObserver struct {
	mu        sync.Mutex
	script    map[int][]observeStep
	calls     []int
	forgotten []int
	retained  [][]int
	gate      chan struct{}
	entered   chan int
}

func (f *fakeObserver) Evaluate(_ context.Context, binding worktrees.RegisteredPullRequestBinding) (prwatch.Outcome, error) {
	f.mu.Lock()
	f.calls = append(f.calls, binding.PullRequest)
	steps := f.script[binding.PullRequest]
	var step observeStep
	if len(steps) > 0 {
		step = steps[0]
		if len(steps) > 1 {
			f.script[binding.PullRequest] = steps[1:]
		}
	}
	gate, entered := f.gate, f.entered
	f.mu.Unlock()
	if entered != nil {
		entered <- binding.PullRequest
	}
	if gate != nil {
		<-gate
	}
	if step.panics {
		panic("watcher exploded")
	}
	return step.outcome, step.err
}

func (f *fakeObserver) Forget(binding worktrees.RegisteredPullRequestBinding) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgotten = append(f.forgotten, binding.PullRequest)
}

func (f *fakeObserver) Retain(bindings []worktrees.RegisteredPullRequestBinding) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var numbers []int
	for _, binding := range bindings {
		numbers = append(numbers, binding.PullRequest)
	}
	f.retained = append(f.retained, numbers)
}

// take returns the numbers observed since the last take, in the order asked.
func (f *fakeObserver) take() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := f.calls
	f.calls = nil
	return calls
}

func snapshotOutcome(snapshot prsnapshot.Snapshot, kind prwatch.Kind) prwatch.Outcome {
	return prwatch.Outcome{Kind: kind, Snapshot: snapshot}
}

func openChecks(checks map[string]int, failed ...string) observeStep {
	return observeStep{outcome: snapshotOutcome(prsnapshot.Snapshot{State: "open", Mergeable: "blocked", Checks: checks, Failed: failed}, prwatch.KindChecksPending)}
}

func binding(number int) worktrees.RegisteredPullRequestBinding {
	return worktrees.RegisteredPullRequestBinding{Task: "task-a", Repository: "acme/widgets", PullRequest: number, URL: "https://github.com/acme/widgets/pull/" + string(rune('0'+number%10))}
}

func pullObserved(observer PullRequestObserver, bindings ...worktrees.RegisteredPullRequestBinding) (*Snapshotter, *manualClock, *fakeSources) {
	sources := oneRepoSources("/repo/widgets")
	sources.bindings, sources.remote = bindings, nil
	snapshotter, clock := newSnapshotter(sources.collectors(), func(options *Options) {
		options.PullRequests, options.PullRequestLimit = observer, 3
	})
	return snapshotter, clock, sources
}

func pullEntry(t *testing.T, snapshotter *Snapshotter, number int) PullRequest {
	t.Helper()
	for _, pull := range snapshotter.Document().PullRequests {
		if pull.Number == number && pull.Route == "local" {
			return pull
		}
	}
	t.Fatalf("no local pull request %d in %+v", number, snapshotter.Document().PullRequests)
	return PullRequest{}
}

// TestPullRequestEntriesCarryStateAndChecks is
// cockpit-views#ac:pull-request-entries-carry-state-and-checks.
func TestPullRequestEntriesCarryStateAndChecks(t *testing.T) {
	t.Parallel()
	observer := &fakeObserver{script: map[int][]observeStep{
		12: {openChecks(map[string]int{"pass": 3, "skipping": 1, "fail": 1, "pending": 1}, "go-ci / test")},
		13: {openChecks(map[string]int{"pass": 2, "pending": 1}), {err: errors.New("github is down")}},
		14: {{outcome: snapshotOutcome(prsnapshot.Snapshot{State: "closed", Merged: true}, prwatch.KindMerged)}},
		15: {openChecks(nil)},
		16: {openChecks(nil)},
	}}
	// 13 is bound before 12 so that, with equal ages, it is the one the second
	// tick re-observes after the two pull requests never observed.
	snapshotter, clock, sources := pullObserved(observer, binding(13), binding(12), binding(14), binding(15), binding(16))
	server := newCockpitServer(t, snapshotter)

	refreshAndSettle(t, snapshotter)
	if got := observer.take(); len(got) != 3 || slices.Contains(got, 15) || slices.Contains(got, 16) {
		t.Fatalf("first tick observed %v, want the first 3 bound pull requests", got)
	}
	first := pullEntry(t, snapshotter, 13)
	if first.State != "open" || first.CheckedAt.IsZero() {
		t.Fatalf("13 after the first read = %+v", first)
	}
	clock.advance(2 * time.Minute)
	refreshAndSettle(t, snapshotter)
	second := observer.take()
	if len(second) != 3 || slices.Contains(second, 14) || !slices.Contains(second, 13) || !slices.Contains(second, 15) || !slices.Contains(second, 16) {
		t.Fatalf("second tick observed %v, want 15, 16 (never observed) and then 13 (due first of the pending), not the merged 14", second)
	}
	clock.advance(2 * time.Minute)
	refreshAndSettle(t, snapshotter)
	if third := observer.take(); len(third) != 3 || slices.Contains(third, 14) {
		t.Fatalf("third tick observed %v", third)
	}

	twelve := pullEntry(t, snapshotter, 12)
	if twelve.State != "open" || twelve.Mergeable != "blocked" || *twelve.ChecksTotal != 6 || *twelve.ChecksPassed != 3 || *twelve.ChecksSkipped != 1 ||
		*twelve.ChecksFailed != 1 || *twelve.ChecksPending != 1 || twelve.FailedCheck != "go-ci / test" || *twelve.ChecksGreen || twelve.CheckedAt.IsZero() {
		t.Errorf("12 = %+v", twelve)
	}
	thirteen := pullEntry(t, snapshotter, 13)
	if thirteen.State != "open" || *thirteen.ChecksPassed != 2 || !thirteen.CheckedAt.Equal(first.CheckedAt) {
		t.Errorf("13 after a failed read = %+v, want its first values and checked_at %v", thirteen, first.CheckedAt)
	}
	if pullEntry(t, snapshotter, 14).State != "merged" {
		t.Errorf("14 = %+v", pullEntry(t, snapshotter, 14))
	}
	observer.mu.Lock()
	forgotten := slices.Clone(observer.forgotten)
	observer.mu.Unlock()
	if !slices.Contains(forgotten, 14) {
		t.Errorf("the merged pull request was not forgotten by the watcher: %v", forgotten)
	}

	before := sources.calls.Load()
	for range 3 {
		server.get("/api/v1/cockpit/fleet", nil)
	}
	if got := observer.take(); len(got) != 0 || sources.calls.Load() != before {
		t.Errorf("a request read GitHub or a source: observed %v", got)
	}
}

// TestPullRequestStateIsAbsentUntilObserved is
// cockpit-views#ac:pull-request-state-absent-until-observed: a pull request
// whose observation never succeeded has only its number, repository and url.
func TestPullRequestStateIsAbsentUntilObserved(t *testing.T) {
	t.Parallel()
	observer := &fakeObserver{script: map[int][]observeStep{
		1: {{err: errors.New("denied")}},
		2: {{panics: true}},
		3: {{outcome: prwatch.Outcome{Kind: prwatch.KindUnavailable, Snapshot: prsnapshot.Snapshot{Err: errors.New("down")}}}},
	}}
	snapshotter, _, _ := pullObserved(observer, binding(1), binding(2), binding(3))
	server := newCockpitServer(t, snapshotter)
	refreshAndSettle(t, snapshotter)
	var document struct {
		PullRequests []map[string]any `json:"pull_requests"`
	}
	if err := json.Unmarshal(server.get("/api/v1/cockpit/fleet", nil).Body.Bytes(), &document); err != nil || len(document.PullRequests) != 3 {
		t.Fatalf("document: %v %+v", err, document)
	}
	for _, pull := range document.PullRequests {
		for _, field := range []string{"state", "mergeable", "checks_total", "checks_passed", "checks_failed", "checks_skipped", "checks_pending", "checks_green", "failed_check", "checked_at"} {
			if _, present := pull[field]; present {
				t.Errorf("a pull request never observed carries %q: %v", field, pull)
			}
		}
		if pull["number"] == nil || pull["repository"] == nil || pull["url"] == nil {
			t.Errorf("entry lost its identity: %v", pull)
		}
	}
}

// TestPullRequestStringsAreHostileSafe is
// cockpit-views#ac:pull-request-strings-are-hostile-safe for the daemon half:
// the failing check's name, the mergeable value, the addresses and a cached
// state.
func TestPullRequestStringsAreHostileSafe(t *testing.T) {
	t.Parallel()
	hostile := strings.Repeat("a", 90) + "\x00\x1b[31m\u202e" + strings.Repeat("b", 300)
	step := openChecks(map[string]int{"fail": 1}, hostile)
	step.outcome.Snapshot.Mergeable = "<img onerror=1>"
	observer := &fakeObserver{script: map[int][]observeStep{5: {step}}}
	urls := []string{"javascript:alert(1)", "http://example.com/x", "https://user@example.com/x", "https://example.com:8443/x", "https://github.com/o/r/pull/1"}
	var bindings []worktrees.RegisteredPullRequestBinding
	for index, url := range urls {
		bound := binding(5 + index)
		bound.URL = url
		bindings = append(bindings, bound)
	}
	sources := sentinelSources()
	sources.remote[0].Snapshot.Worktrees[0].PullRequest.State = "bogus"
	sources.bindings = bindings
	snapshotter, _ := newSnapshotter(sources.collectors(), func(options *Options) {
		options.PullRequests, options.PullRequestLimit = observer, 10
	})
	refreshAndSettle(t, snapshotter)
	if got := pullEntry(t, snapshotter, 5); got.Mergeable != "" || len([]rune(got.FailedCheck)) != maxFailedCheckText ||
		strings.ContainsAny(got.FailedCheck, "\x00\x1b\u202e") || !strings.HasPrefix(got.FailedCheck, strings.Repeat("a", 90)) {
		t.Errorf("hostile observation = mergeable %q failed_check %q", got.Mergeable, got.FailedCheck)
	}
	var withURL []string
	for _, pull := range snapshotter.Document().PullRequests {
		if pull.Route == "local" && pull.URL != "" {
			withURL = append(withURL, pull.URL)
		}
		if pull.Route == "cached" && pull.State != "" {
			t.Errorf("a cached pull request with state %q kept it", pull.State)
		}
	}
	if !slices.Equal(withURL, []string{"https://github.com/o/r/pull/1"}) {
		t.Errorf("addresses emitted = %v", withURL)
	}
}

// TestObservationOfMapsEveryState pins the mapping of a snapshot to the entry.
func TestObservationOfMapsEveryState(t *testing.T) {
	t.Parallel()
	at := newClock().Now()
	for name, test := range map[string]struct {
		snapshot prsnapshot.Snapshot
		state    string
	}{
		"open":   {prsnapshot.Snapshot{State: "open"}, "open"},
		"draft":  {prsnapshot.Snapshot{State: "open", Draft: true}, "draft"},
		"closed": {prsnapshot.Snapshot{State: "closed"}, "closed"},
		"merged": {prsnapshot.Snapshot{State: "closed", Merged: true, Draft: true}, "merged"},
	} {
		got, ok := observationOf(snapshotOutcome(test.snapshot, prwatch.KindChecksPending), at)
		if !ok || got.state != test.state || !got.checkedAt.Equal(at) {
			t.Errorf("%s: %+v %v", name, got, ok)
		}
	}
	got, _ := observationOf(snapshotOutcome(prsnapshot.Snapshot{
		State: "open", Mergeable: "dirty", Green: true, Failed: []string{"x", "y"},
		Checks: map[string]int{"pass": 1, "fail": 2, "cancel": 1, "skipping": 3, "pending": 4, "neutral": 5},
	}, prwatch.KindChecksPassed), at)
	if got.total != 16 || got.passed != 1 || got.failed != 3 || got.skipped != 3 || got.pending != 4 || !got.green || got.mergeable != "dirty" || got.failedCheck != "x" || got.done {
		t.Errorf("counts = %+v", got)
	}
	if _, ok := observationOf(prwatch.Outcome{Kind: prwatch.KindUnavailable}, at); ok {
		t.Error("an unavailable outcome was taken as an observation")
	}
	if _, ok := observationOf(prwatch.Outcome{Kind: prwatch.KindChecksPending, Snapshot: prsnapshot.Snapshot{Err: errors.New("x")}}, at); ok {
		t.Error("a snapshot with an error was taken as an observation")
	}
}

// TestPullRequestObservationNeverDelaysThePublishedSnapshot holds every
// observation open: the refresh still returns and publishes the local
// document, without state, and the state arrives when the observation does.
func TestPullRequestObservationNeverDelaysThePublishedSnapshot(t *testing.T) {
	t.Parallel()
	observer := &fakeObserver{script: map[int][]observeStep{1: {openChecks(map[string]int{"pass": 1})}}, gate: make(chan struct{}), entered: make(chan int, 10)}
	snapshotter, clock, sources := pullObserved(observer, binding(1), binding(2))
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if pull := pullEntry(t, snapshotter, 1); pull.State != "" || snapshotter.Document().WarmingUp {
		t.Fatalf("published document = %+v", snapshotter.Document())
	}
	<-observer.entered
	// A refresh while the pass runs starts no second one, and the pull request
	// that is gone from the records is dropped when its observation lands.
	clock.advance(5 * time.Minute)
	sources.mu.Lock()
	sources.bindings = sources.bindings[1:]
	sources.mu.Unlock()
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	close(observer.gate)
	snapshotter.side.Wait()
	if got := observer.take(); len(got) != 2 {
		t.Fatalf("observed %v, want only the first pass's two", got)
	}
	if _, dropped := snapshotter.observed[pullKey("acme/widgets", 1)]; dropped {
		t.Error("an observation of a pull request no longer recorded was kept")
	}
	if len(snapshotter.Document().PullRequests) != 1 {
		t.Errorf("document = %+v", snapshotter.Document().PullRequests)
	}
}

// TestPullRequestsAreObservedWhenDueAndNeverOnceDone follows one settled pull
// request, one merged and one closed through the cadence: a settled head waits
// ten minutes, merged and closed are observed once.
func TestPullRequestsAreObservedWhenDueAndNeverOnceDone(t *testing.T) {
	t.Parallel()
	observer := &fakeObserver{script: map[int][]observeStep{
		1: {{outcome: snapshotOutcome(prsnapshot.Snapshot{State: "closed", Merged: true}, prwatch.KindMerged)}},
		2: {{outcome: snapshotOutcome(prsnapshot.Snapshot{State: "closed"}, prwatch.KindClosed)}},
		3: {openChecks(map[string]int{"pass": 1})},
	}}
	snapshotter, clock, _ := pullObserved(observer, binding(1), binding(2), binding(3))
	refreshAndSettle(t, snapshotter)
	if got := observer.take(); len(got) != 3 {
		t.Fatalf("first refresh observed %v", got)
	}
	clock.advance(5 * time.Minute)
	refreshAndSettle(t, snapshotter)
	if got := observer.take(); len(got) != 0 {
		t.Fatalf("a settled head was observed again after 5 minutes: %v", got)
	}
	clock.advance(6 * time.Minute)
	refreshAndSettle(t, snapshotter)
	if got := observer.take(); !slices.Equal(got, []int{3}) {
		t.Fatalf("after 11 minutes observed %v, want only the settled pull request 3", got)
	}
	if pullEntry(t, snapshotter, 2).State != "closed" || pullEntry(t, snapshotter, 1).State != "merged" {
		t.Errorf("a done pull request lost its state: %+v", snapshotter.Document().PullRequests)
	}
	observer.mu.Lock()
	forgotten := slices.Clone(observer.forgotten)
	observer.mu.Unlock()
	slices.Sort(forgotten)
	if !slices.Equal(forgotten, []int{1, 2}) || len(observer.retained) != 3 {
		t.Errorf("forgotten %v, retained %d times", forgotten, len(observer.retained))
	}
}

// TestObservationIsThrottledByTheHourlyBudget spends a budget of 2, shows the
// document says so with the old values kept, and observes again when the window
// has passed.
func TestObservationIsThrottledByTheHourlyBudget(t *testing.T) {
	t.Parallel()
	pending := openChecks(map[string]int{"pending": 1})
	observer := &fakeObserver{script: map[int][]observeStep{1: {pending}, 2: {pending}, 3: {pending}}}
	snapshotter, clock, _ := pullObserved(observer, binding(1), binding(2), binding(3))
	snapshotter.pullBudget = 2
	refreshAndSettle(t, snapshotter)
	if got := observer.take(); len(got) != 2 || !snapshotter.Document().PullRequestsThrottled {
		t.Fatalf("observed %v throttled %v, want 2 and true", got, snapshotter.Document().PullRequestsThrottled)
	}
	first := snapshotter.Document()
	clock.advance(30 * time.Minute)
	refreshAndSettle(t, snapshotter)
	if got := observer.take(); len(got) != 0 || !snapshotter.Document().PullRequestsThrottled {
		t.Fatalf("observed %v with the budget spent, throttled %v", got, snapshotter.Document().PullRequestsThrottled)
	}
	for _, pull := range snapshotter.Document().PullRequests {
		for _, old := range first.PullRequests {
			if pull.ID == old.ID && !pull.CheckedAt.Equal(old.CheckedAt) {
				t.Errorf("a throttled pass changed checked_at of %d", pull.Number)
			}
		}
	}
	clock.advance(31 * time.Minute)
	refreshAndSettle(t, snapshotter)
	if got := observer.take(); len(got) != 2 {
		t.Fatalf("after the window observed %v", got)
	}
	clock.advance(2 * time.Minute)
	snapshotter.pullBudget = 10
	refreshAndSettle(t, snapshotter)
	observer.take()
	if snapshotter.Document().PullRequestsThrottled {
		t.Error("throttled stayed set once the budget allowed every due pull request")
	}
}

// TestPullDueFollowsTheObservationHistory pins the cadence as a pure function.
func TestPullDueFollowsTheObservationHistory(t *testing.T) {
	t.Parallel()
	at := newClock().Now()
	for name, test := range map[string]struct {
		observation pullObservation
		observed    bool
		attempt     pullAttempt
		due         time.Time
		watched     bool
	}{
		"never asked":         {attempt: pullAttempt{}, due: time.Time{}, watched: true},
		"first failure":       {attempt: pullAttempt{at: at, failures: 1}, due: at.Add(2 * time.Minute), watched: true},
		"third failure":       {attempt: pullAttempt{at: at, failures: 3}, due: at.Add(8 * time.Minute), watched: true},
		"failures are capped": {attempt: pullAttempt{at: at, failures: 40}, due: at.Add(30 * time.Minute), watched: true},
		"waiting":             {observation: pullObservation{waiting: true}, observed: true, attempt: pullAttempt{at: at}, due: at.Add(90 * time.Second), watched: true},
		"settled":             {observation: pullObservation{green: true}, observed: true, attempt: pullAttempt{at: at}, due: at.Add(10 * time.Minute), watched: true},
		"asked, never seen":   {attempt: pullAttempt{at: at}, due: at.Add(90 * time.Second), watched: true},
		"done":                {observation: pullObservation{done: true}, observed: true, attempt: pullAttempt{at: at}, watched: false},
		"failed after done":   {observation: pullObservation{done: true}, observed: true, attempt: pullAttempt{at: at, failures: 2}, watched: false},
	} {
		due, watched := pullDue(test.observation, test.observed, test.attempt)
		if !due.Equal(test.due) || watched != test.watched {
			t.Errorf("%s: due %v watched %v, want %v %v", name, due, watched, test.due, test.watched)
		}
	}
}

// TestObservationOfWaitsOnlyForChecksThatCanStillArrive pins which snapshots
// are awaiting a verdict.
func TestObservationOfWaitsOnlyForChecksThatCanStillArrive(t *testing.T) {
	t.Parallel()
	at := newClock().Now()
	for name, test := range map[string]struct {
		snapshot prsnapshot.Snapshot
		waiting  bool
	}{
		"running":            {prsnapshot.Snapshot{State: "open", Checks: map[string]int{"pending": 1}}, true},
		"mergeability":       {prsnapshot.Snapshot{State: "open", Mergeable: "unknown", Checks: map[string]int{"pass": 1}}, true},
		"nothing registered": {prsnapshot.Snapshot{State: "open"}, true},
		"missing required":   {prsnapshot.Snapshot{State: "open", Blocked: []string{"build"}}, false},
		"green":              {prsnapshot.Snapshot{State: "open", Green: true, Checks: map[string]int{"pass": 1}}, false},
		"failed":             {prsnapshot.Snapshot{State: "open", Checks: map[string]int{"fail": 1}}, false},
		"draft":              {prsnapshot.Snapshot{State: "open", Draft: true, Checks: map[string]int{"pass": 1}}, false},
		"closed":             {prsnapshot.Snapshot{State: "closed"}, false},
	} {
		if got, _ := observationOf(snapshotOutcome(test.snapshot, prwatch.KindChecksPending), at); got.waiting != test.waiting {
			t.Errorf("%s: waiting = %v", name, got.waiting)
		}
	}
}

// TestBudgetLeftCountsTheRollingHour pins the window.
func TestBudgetLeftCountsTheRollingHour(t *testing.T) {
	t.Parallel()
	now := newClock().Now()
	kept, left := budgetLeft([]time.Time{now.Add(-61 * time.Minute), now.Add(-59 * time.Minute), now}, now, 5)
	if len(kept) != 2 || left != 3 {
		t.Errorf("kept %v left %d", kept, left)
	}
	if _, left := budgetLeft(nil, now, 5); left != 5 {
		t.Errorf("left = %d", left)
	}
}

// TestPullBatchIsBoundedAndOldestDueFirst pins the selection.
func TestPullBatchIsBoundedAndOldestDueFirst(t *testing.T) {
	t.Parallel()
	now := newClock().Now()
	waiting := pullObservation{waiting: true}
	observed := map[string]pullObservation{
		pullKey("acme/widgets", 1): waiting, pullKey("acme/widgets", 2): waiting, pullKey("acme/widgets", 3): {done: true}, pullKey("acme/widgets", 5): {green: true},
	}
	attempts := map[string]pullAttempt{
		pullKey("acme/widgets", 1): {at: now.Add(-5 * time.Minute)},
		pullKey("acme/widgets", 2): {at: now.Add(-10 * time.Minute)},
		pullKey("acme/widgets", 5): {at: now.Add(-time.Minute)},
	}
	bindings := []worktrees.RegisteredPullRequestBinding{binding(1), binding(2), binding(3), binding(4), binding(2), binding(5), {Repository: "acme/widgets"}, {PullRequest: 9}}
	numbers := func(batch []worktrees.RegisteredPullRequestBinding) (got []int) {
		for _, picked := range batch {
			got = append(got, picked.PullRequest)
		}
		return got
	}
	batch, throttled := pullBatch(bindings, observed, attempts, now, 2, 10)
	if !slices.Equal(numbers(batch), []int{4, 2}) || throttled {
		t.Errorf("batch = %v throttled %v, want the never asked 4 then the oldest due 2", numbers(batch), throttled)
	}
	batch, throttled = pullBatch(bindings, observed, attempts, now, 10, 1)
	if !slices.Equal(numbers(batch), []int{4}) || !throttled {
		t.Errorf("with a budget of 1: %v throttled %v", numbers(batch), throttled)
	}
	if batch, throttled = pullBatch(bindings, observed, attempts, now, 10, 0); len(batch) != 0 || !throttled {
		t.Errorf("with no budget: %v throttled %v", batch, throttled)
	}
	if batch, throttled = pullBatch(bindings, observed, attempts, now, 10, -4); len(batch) != 0 || !throttled {
		t.Errorf("with an overspent budget: %v throttled %v", batch, throttled)
	}
}

// TestBindingsReadWithoutAnObserverIsQuiet covers a snapshotter that observes
// nothing.
func TestBindingsReadWithoutAnObserverIsQuiet(t *testing.T) {
	t.Parallel()
	snapshotter, _ := newSnapshotter((&fakeSources{bindings: []worktrees.RegisteredPullRequestBinding{binding(1)}}).collectors(), nil)
	refreshAndSettle(t, snapshotter)
	if pull := pullEntry(t, snapshotter, 1); pull.State != "" || pull.CheckedAt != (time.Time{}) {
		t.Errorf("entry = %+v", pull)
	}
}

// TestObservedPullRequestLeaksNothingButTheAllowedFields feeds the observer a
// snapshot with a sentinel in every field (head, base, address, failure
// details, blocked checks) and requires that the document carries none of
// them, except the first failing check's name, which the metadata set allows.
func TestObservedPullRequestLeaksNothingButTheAllowedFields(t *testing.T) {
	t.Parallel()
	snapshot := filled[prsnapshot.Snapshot]()
	snapshot.State, snapshot.Merged, snapshot.Draft, snapshot.Mergeable, snapshot.Err = "open", false, false, "clean", nil
	snapshot.Checks, snapshot.Failed = map[string]int{"fail": 1}, []string{"go-ci / test"}
	outcome := filled[prwatch.Outcome]()
	outcome.Kind, outcome.Snapshot = prwatch.KindChecksFailed, snapshot
	observer := &fakeObserver{script: map[int][]observeStep{7: {{outcome: outcome}}}}
	sources := sentinelSources()
	snapshotter, _ := newSnapshotter(sources.collectors(), func(options *Options) { options.PullRequests = observer })
	refreshAndSettle(t, snapshotter)
	body, err := jsonString(snapshotter.Document())
	if err != nil {
		t.Fatal(err)
	}
	if got := pullEntry(t, snapshotter, 7); got.FailedCheck != "go-ci / test" || got.Mergeable != "clean" {
		t.Fatalf("entry = %+v", got)
	}
	if strings.Contains(body, sentinel) {
		start := strings.Index(body, sentinel)
		t.Fatalf("the document carries an observation field outside the set: ...%s...", body[start:min(len(body), start+60)])
	}
}

// TestAPullRequestNoLongerRecordedIsForgotten drops what is remembered of a
// pull request once its record is gone.
func TestAPullRequestNoLongerRecordedIsForgotten(t *testing.T) {
	t.Parallel()
	observer := &fakeObserver{script: map[int][]observeStep{1: {openChecks(map[string]int{"pass": 1})}}}
	snapshotter, _, sources := pullObserved(observer, binding(1))
	refreshAndSettle(t, snapshotter)
	if len(snapshotter.observed) != 1 {
		t.Fatalf("observed = %v", snapshotter.observed)
	}
	sources.mu.Lock()
	sources.bindings = nil
	sources.mu.Unlock()
	refreshAndSettle(t, snapshotter)
	if len(snapshotter.observed) != 0 {
		t.Errorf("observed = %v, want it empty", snapshotter.observed)
	}
}
