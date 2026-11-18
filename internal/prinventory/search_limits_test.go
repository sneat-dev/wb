package prinventory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

var limitsNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

const emptySearch = `[{"total_count":0,"items":[]}]`

type clockRecorder struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
	notes  []string
	err    error
}

func (c *clockRecorder) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clockRecorder) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return c.err
}
func (c *clockRecorder) Notify(s string) { c.mu.Lock(); c.notes = append(c.notes, s); c.mu.Unlock() }

func runWith(clock *clockRecorder, runner Runner, owners ...Owner) Report {
	return Inventory(context.Background(), Options{Owners: owners, Runner: runner, Parallel: 1,
		Now: clock.Now, Sleep: clock.Sleep, Notify: clock.Notify})
}

func isSearch(args []string) bool { return strings.Contains(strings.Join(args, " "), "/search/issues") }

var errValidation = errors.New("gh: Validation Failed (HTTP 422)")

func emptyOrgRunner(lookup func(path string) ([]byte, error)) *fakeRunner {
	return &fakeRunner{fn: func(args []string) ([]byte, error) {
		if isSearch(args) {
			return nil, errValidation
		}
		if len(args) == 2 && args[0] == "api" {
			return lookup(args[1])
		}
		return nil, fmt.Errorf("unexpected call %v", args)
	}}
}

func TestEmptyOrganisationIsCompleteWithZeroPullRequests(t *testing.T) {
	t.Parallel()
	clock := &clockRecorder{now: limitsNow}
	runner := emptyOrgRunner(func(path string) ([]byte, error) {
		if path != "orgs/gouchdb" {
			t.Fatalf("lookup path = %q", path)
		}
		return []byte(`{"public_repos":0,"total_private_repos":0}`), nil
	})
	report := runWith(clock, runner, Owner{Login: "gouchdb", Qualifier: "org"})
	if !report.Complete || report.Counts.OwnersCompleted != 1 || report.Counts.OwnersFailed != 0 || len(report.Diagnostics) != 0 || report.Counts.PullRequests != 0 {
		t.Fatalf("report = %+v", report)
	}
	if !report.OwnerResults[0].Complete {
		t.Fatalf("owner result = %+v", report.OwnerResults[0])
	}
}

func TestEmptyUserIsCompleteWithZeroPullRequests(t *testing.T) {
	t.Parallel()
	runner := emptyOrgRunner(func(path string) ([]byte, error) {
		if path != "users/nobody" {
			t.Fatalf("lookup path = %q", path)
		}
		return []byte(`{"public_repos":0}`), nil
	})
	report := runWith(&clockRecorder{now: limitsNow}, runner, Owner{Login: "nobody", Qualifier: "user"})
	if !report.Complete {
		t.Fatalf("report = %+v", report)
	}
}

func TestValidationFailureStaysAnErrorWithoutProofOfEmptiness(t *testing.T) {
	t.Parallel()
	cases := map[string]func(string) ([]byte, error){
		"org has public repos":      func(string) ([]byte, error) { return []byte(`{"public_repos":3,"total_private_repos":0}`), nil },
		"org has private repos":     func(string) ([]byte, error) { return []byte(`{"public_repos":0,"total_private_repos":2}`), nil },
		"org private count unknown": func(string) ([]byte, error) { return []byte(`{"public_repos":0}`), nil },
		"owner does not exist":      func(string) ([]byte, error) { return nil, errors.New("gh: Not Found (HTTP 404)") },
		"owner lookup unparseable":  func(string) ([]byte, error) { return []byte(`not json`), nil },
	}
	for name, lookup := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			report := runWith(&clockRecorder{now: limitsNow}, emptyOrgRunner(lookup), Owner{Login: "acme", Qualifier: "org"})
			if report.Complete || report.Counts.OwnersFailed != 1 || len(report.Diagnostics) != 1 || !strings.Contains(report.Diagnostics[0].Message, "GitHub owner query failed") {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

func TestUserWithPrivateRepositoryCountStaysAnError(t *testing.T) {
	t.Parallel()
	runner := emptyOrgRunner(func(string) ([]byte, error) { return []byte(`{"public_repos":0,"total_private_repos":4}`), nil })
	report := runWith(&clockRecorder{now: limitsNow}, runner, Owner{Login: "me", Qualifier: "user"})
	if report.Complete {
		t.Fatalf("report = %+v", report)
	}
}

func TestOtherSearchFailuresDoNotTriggerEmptinessLookup(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{fn: func(args []string) ([]byte, error) {
		if strings.Contains(args[1], "orgs/") {
			t.Fatalf("unexpected emptiness lookup %v", args)
		}
		if !isSearch(args) {
			return rateInfo(30, limitsNow.Add(time.Minute))()
		}
		return nil, errors.New("gh: Server Error (HTTP 500)")
	}}
	report := runWith(&clockRecorder{now: limitsNow}, runner, Owner{Login: "acme", Qualifier: "org"})
	if report.Complete || len(runner.calls) != 2 {
		t.Fatalf("report = %+v calls = %v", report, runner.calls)
	}
}

func rateLimitRunner(failures int, info func() ([]byte, error)) *fakeRunner {
	searches := 0
	return &fakeRunner{fn: func(args []string) ([]byte, error) {
		if isSearch(args) {
			searches++
			if searches <= failures {
				return nil, errors.New("boom")
			}
			return []byte(emptySearch), nil
		}
		if len(args) == 2 && args[1] == "rate_limit" {
			return info()
		}
		return nil, fmt.Errorf("unexpected call %v", args)
	}}
}

func rateInfo(remaining int, reset time.Time) func() ([]byte, error) {
	return func() ([]byte, error) {
		return []byte(fmt.Sprintf(`{"resources":{"search":{"remaining":%d,"reset":%d}}}`, remaining, reset.Unix())), nil
	}
}

func TestSearchRateLimitWaitsUntilReportedResetThenRetries(t *testing.T) {
	t.Parallel()
	clock := &clockRecorder{now: limitsNow}
	runner := rateLimitRunner(1, rateInfo(0, limitsNow.Add(20*time.Second)))
	report := runWith(clock, runner, Owner{Login: "sneat-co", Qualifier: "org"})
	if !report.Complete || len(report.Diagnostics) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if len(clock.sleeps) != 1 || clock.sleeps[0] != 21*time.Second {
		t.Fatalf("sleeps = %v, want [21s]", clock.sleeps)
	}
	if len(clock.notes) != 1 || !strings.Contains(clock.notes[0], "sneat-co") || !strings.Contains(clock.notes[0], "21s") || !strings.Contains(clock.notes[0], "search rate limit") {
		t.Fatalf("notes = %q", clock.notes)
	}
}

func TestSearchRateLimitWaitEdgesAndNonLimitFailures(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		info      func() ([]byte, error)
		wantSleep []time.Duration
	}{
		"reset already passed":          {rateInfo(0, limitsNow.Add(-5*time.Second)), []time.Duration{time.Second}},
		"clock behind GitHub is capped": {rateInfo(0, limitsNow.Add(time.Hour)), []time.Duration{maxSearchWait}},
		"quota remains (not a limit)":   {rateInfo(10, limitsNow.Add(5*time.Second)), nil},
		"rate limit query fails":        {func() ([]byte, error) { return nil, errors.New("boom") }, nil},
		"rate limit response not JSON":  {func() ([]byte, error) { return []byte("x"), nil }, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			clock := &clockRecorder{now: limitsNow}
			report := runWith(clock, rateLimitRunner(1, tc.info), Owner{Login: "acme", Qualifier: "org"})
			if report.Complete != (tc.wantSleep != nil) || fmt.Sprint(clock.sleeps) != fmt.Sprint(tc.wantSleep) {
				t.Fatalf("complete=%v sleeps=%v want %v", report.Complete, clock.sleeps, tc.wantSleep)
			}
			if name == "clock behind GitHub is capped" && !strings.Contains(clock.notes[0], "capped at 1m30s") {
				t.Fatalf("notes = %q", clock.notes)
			}
		})
	}
}

func TestPullRequestTitleMentioningRateLimitIsNotARateLimit(t *testing.T) {
	t.Parallel()
	clock := &clockRecorder{now: limitsNow}
	runner := &fakeRunner{fn: func(args []string) ([]byte, error) {
		if isSearch(args) {
			return nil, errors.New(`gh api: exit status 1: HTTP 500 {"items":[{"title":"fix API rate limit exceeded handling (HTTP 422)"}]}`)
		}
		return rateInfo(25, limitsNow.Add(30*time.Second))()
	}}
	report := runWith(clock, runner, Owner{Login: "acme", Qualifier: "org"})
	if report.Complete || len(clock.sleeps) != 0 || len(clock.notes) != 0 {
		t.Fatalf("report = %+v sleeps = %v", report, clock.sleeps)
	}
}

func TestWithoutASleeperARateLimitIsAnErrorAndNeverWaits(t *testing.T) {
	t.Parallel()
	runner := rateLimitRunner(1, rateInfo(0, limitsNow.Add(5*time.Second)))
	report := Inventory(context.Background(), Options{Owners: []Owner{{Login: "acme", Qualifier: "org"}}, Runner: runner, Now: func() time.Time { return limitsNow }})
	if report.Complete || len(runner.calls) != 1 {
		t.Fatalf("report = %+v calls = %v", report, runner.calls)
	}
}

func TestSearchRateLimitGivesUpAfterBoundedWaits(t *testing.T) {
	t.Parallel()
	clock := &clockRecorder{now: limitsNow}
	runner := rateLimitRunner(100, rateInfo(0, limitsNow.Add(time.Second)))
	report := runWith(clock, runner, Owner{Login: "acme", Qualifier: "org"})
	if report.Complete || len(report.Diagnostics) != 1 || !strings.Contains(report.Diagnostics[0].Message, "GitHub owner query failed") {
		t.Fatalf("report = %+v", report)
	}
	if len(clock.sleeps) != searchRateLimitWaits {
		t.Fatalf("sleeps = %v, want %d bounded waits", clock.sleeps, searchRateLimitWaits)
	}
}

func TestSearchRateLimitWaitInterruptedByCancellation(t *testing.T) {
	t.Parallel()
	clock := &clockRecorder{now: limitsNow, err: context.Canceled}
	report := runWith(clock, rateLimitRunner(1, rateInfo(0, limitsNow.Add(time.Second))), Owner{Login: "acme", Qualifier: "org"})
	if report.Complete || !strings.Contains(report.Diagnostics[0].Message, "context canceled") {
		t.Fatalf("report = %+v", report)
	}
}

func TestSearchRateLimitPausesLaterOwnersUntilReset(t *testing.T) {
	t.Parallel()
	clock := &clockRecorder{now: limitsNow}
	var order []string
	failedOnce := false
	runner := &fakeRunner{fn: func(args []string) ([]byte, error) {
		if isSearch(args) {
			order = append(order, args[len(args)-3])
			if !failedOnce {
				failedOnce = true
				return nil, errors.New("boom")
			}
			return []byte(emptySearch), nil
		}
		return rateInfo(0, limitsNow.Add(10*time.Second))()
	}}
	report := runWith(clock, runner, Owner{Login: "a", Qualifier: "org"}, Owner{Login: "b", Qualifier: "org"})
	if !report.Complete || len(order) != 3 {
		t.Fatalf("report = %+v order = %v", report, order)
	}
	if len(clock.sleeps) != 1 {
		t.Fatalf("later owner must not wait again once the reset passed: %v", clock.sleeps)
	}
}

func TestGateHoldsEveryWorkerUntilResetPasses(t *testing.T) {
	t.Parallel()
	clock := &clockRecorder{now: limitsNow}
	gate := &searchGate{}
	gate.holdUntil(limitsNow.Add(7 * time.Second))
	gate.holdUntil(limitsNow.Add(3 * time.Second)) // an earlier reset never shortens the hold
	if err := gate.wait(context.Background(), clock.Now, clock.Sleep); err != nil {
		t.Fatal(err)
	}
	if len(clock.sleeps) != 1 || clock.sleeps[0] != 7*time.Second {
		t.Fatalf("sleeps = %v", clock.sleeps)
	}
	clock.err = errors.New("stop")
	gate.holdUntil(clock.Now().Add(time.Second))
	if err := gate.wait(context.Background(), clock.Now, clock.Sleep); err == nil {
		t.Fatal("sleep error must propagate")
	}
}

func TestRealSleep(t *testing.T) {
	t.Parallel()
	if err := RealSleep(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if err := RealSleep(context.Background(), time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RealSleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

type concurrentRunner struct {
	mu     sync.Mutex
	events []string
	held   chan struct{}
	failed bool
}

func (r *concurrentRunner) log(e string) { r.mu.Lock(); r.events = append(r.events, e); r.mu.Unlock() }

func (r *concurrentRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	if !isSearch(args) {
		return rateInfo(0, limitsNow.Add(10*time.Second))()
	}
	owner := args[len(args)-3] // q=is:pr is:open org:<login>
	owner = owner[strings.LastIndex(owner, ":")+1:]
	if owner == "b" {
		<-r.held // b only searches once a's rate limit has been recorded
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "search "+owner)
	if owner == "a" && !r.failed {
		r.failed = true
		return nil, errors.New("boom")
	}
	return []byte(emptySearch), nil
}

func TestSharedGatePausesAnotherWorkerUntilTheReset(t *testing.T) {
	t.Parallel()
	runner := &concurrentRunner{held: make(chan struct{})}
	secondSleep := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	now, sleeps := limitsNow, 0
	options := Options{
		Owners: []Owner{{Login: "a", Qualifier: "org"}, {Login: "b", Qualifier: "org"}, {Login: "c", Qualifier: "org"}},
		Runner: runner, Parallel: 2,
		Now:    func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
		Notify: func(string) { close(runner.held) },
		Sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			sleeps++
			n := sleeps
			mu.Unlock()
			runner.log(fmt.Sprintf("sleep %d", n))
			if n == 1 {
				<-release // the worker that hit the limit stays asleep
			} else {
				close(secondSleep) // another worker was held by the shared gate
			}
			mu.Lock()
			now = now.Add(d)
			mu.Unlock()
			return nil
		},
	}
	done := make(chan Report)
	go func() { done <- Inventory(context.Background(), options) }()
	<-secondSleep
	close(release)
	report := <-done
	if !report.Complete || sleeps != 2 {
		t.Fatalf("report = %+v sleeps = %d events = %v", report, sleeps, runner.events)
	}
	cSearch, cSleep := -1, -1
	for i, e := range runner.events {
		if e == "search c" {
			cSearch = i
		}
		if e == "sleep 2" {
			cSleep = i
		}
	}
	if cSleep < 0 || cSearch < cSleep {
		t.Fatalf("owner c searched before waiting out the shared pause: %v", runner.events)
	}
}

func TestRateLimitWaitWithoutANotifierStillWaits(t *testing.T) {
	t.Parallel()
	clock := &clockRecorder{now: limitsNow}
	report := Inventory(context.Background(), Options{Owners: []Owner{{Login: "acme", Qualifier: "org"}},
		Runner: rateLimitRunner(1, rateInfo(0, limitsNow.Add(4*time.Second))), Now: clock.Now, Sleep: clock.Sleep})
	if !report.Complete || len(clock.sleeps) != 1 {
		t.Fatalf("report = %+v sleeps = %v", report, clock.sleeps)
	}
}
