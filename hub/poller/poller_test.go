// Copyright 2026 Sneat Co.

package poller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dal-go/dalgo/adapters/dalgo2memory"

	"github.com/sneat-dev/wb/api/githubapp"
	"github.com/sneat-dev/wb/api/githubapp/dalgostore"
	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
)

const (
	firstSHA  = "1111111111111111111111111111111111111111"
	secondSHA = "2222222222222222222222222222222222222222"
)

var pollClock = time.Date(2026, 9, 11, 14, 2, 11, 0, time.UTC)

// localMachine is the one machine a loopback hub knows.
var localMachine = hub.Machine{ID: "machine-a", Name: "laptop", IdentityID: "local"}

// fakeGitHub serves the two endpoints the poller reads, with per-repository
// state a test mutates between ticks. httptest is used rather than a
// RoundTripper so the whole-journey test in Task 3 can reuse the same server.
type fakeGitHub struct {
	mu sync.Mutex
	// repositories maps the path name the poller asks for to what GitHub
	// answers with.
	repositories map[string]*fakeRepository
	// status, when non-zero, is returned for every request instead.
	status int
	// remaining and reset populate the rate-limit headers; remaining < 0
	// omits them entirely, which is what a proxy that strips them looks like.
	remaining int
	reset     time.Time
	requests  int
}

type fakeRepository struct {
	fullName      string
	defaultBranch string
	sha           string
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{repositories: map[string]*fakeRepository{}, remaining: -1}
}

func (fake *fakeGitHub) set(path string, repository *fakeRepository) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.repositories[path] = repository
}

func (fake *fakeGitHub) requestCount() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.requests
}

func (fake *fakeGitHub) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.requests++
	if fake.remaining >= 0 {
		writer.Header().Set("X-RateLimit-Remaining", strconv.Itoa(fake.remaining))
		writer.Header().Set("X-RateLimit-Reset", strconv.FormatInt(fake.reset.Unix(), 10))
	}
	if request.Header.Get("Authorization") != "Bearer operator-token" {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	if fake.status != 0 {
		writer.WriteHeader(fake.status)
		return
	}
	trimmed := strings.TrimPrefix(request.URL.Path, "/repos/")
	if owner, rest, found := strings.Cut(trimmed, "/"); found {
		name, tail, _ := strings.Cut(rest, "/")
		repository, known := fake.repositories[owner+"/"+name]
		if !known {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if tail == "" {
			_, _ = fmt.Fprintf(writer, `{"id":987,"full_name":%q,"default_branch":%q,"private":true}`, repository.fullName, repository.defaultBranch)
			return
		}
		if tail == "commits/"+repository.defaultBranch {
			_, _ = fmt.Fprintf(writer, `{"sha":%q,"commit":{"message":"private"}}`, repository.sha)
			return
		}
	}
	writer.WriteHeader(http.StatusNotFound)
}

// pollHarness is one poller wired to a fake GitHub, a real DALgo-backed
// store, and a buffer that collects the narrated lines.
type pollHarness struct {
	poller  *Poller
	github  *fakeGitHub
	server  *httptest.Server
	lines   *bytes.Buffer
	events  *recordingEventStore
	options *Options
}

// recordingEventStore keeps every enqueued event and delegates deduplication
// to the real store behind it, so a replay behaves as it would in production.
type recordingEventStore struct {
	inner  hub.RepositoryEventStore
	events []repositoryevent.Event
	err    error
}

func (store *recordingEventStore) EnqueueForMachines(ctx context.Context, event repositoryevent.Event, machines []hub.Machine) (hub.EnqueueResult, error) {
	if store.err != nil {
		return hub.EnqueueResult{}, store.err
	}
	result, err := store.inner.EnqueueForMachines(ctx, event, machines)
	if err == nil && !result.Duplicate {
		store.events = append(store.events, event)
	}
	return result, err
}

func (store *recordingEventStore) Poll(ctx context.Context, machine hub.Machine, cursor string, limit int) (repositoryevent.PollResponse, error) {
	return store.inner.Poll(ctx, machine, cursor, limit)
}

func (store *recordingEventStore) Acknowledge(ctx context.Context, machine hub.Machine, request repositoryevent.AckRequest) (repositoryevent.AckResponse, error) {
	return store.inner.Acknowledge(ctx, machine, request)
}

// snapshotInventory is the machine inventory the poller reads its repository
// list from.
type snapshotInventory struct {
	records []hub.StoredMachineSnapshot
	err     error
}

func (snapshotInventory) StoreLatest(context.Context, hub.StoredMachineSnapshot) (hub.MachineSnapshotStoreResult, error) {
	panic("not used")
}

func (inventory snapshotInventory) ListLatest(context.Context) ([]hub.StoredMachineSnapshot, error) {
	return inventory.records, inventory.err
}

func inventoryOf(repositories ...string) snapshotInventory {
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	return snapshotInventory{records: []hub.StoredMachineSnapshot{
		// A snapshot from a different machine must never be polled by this
		// one, so one is always present.
		{IdentityID: "local", MachineID: "machine-b", Snapshot: machinesnapshot.Snapshot{
			SchemaVersion: machinesnapshot.SchemaVersion, Login: "alex", Machine: "desktop", PublishedAt: at,
			Repositories: []string{"github.com/acme/other"}, Worktrees: []machinesnapshot.Worktree{},
		}, ReceivedAt: at, Digest: "b"},
		{IdentityID: "local", MachineID: localMachine.ID, Snapshot: machinesnapshot.Snapshot{
			SchemaVersion: machinesnapshot.SchemaVersion, Login: "alex", Machine: localMachine.Name, PublishedAt: at,
			Repositories: repositories, Worktrees: []machinesnapshot.Worktree{},
		}, ReceivedAt: at, Digest: "a"},
	}}
}

func newDocumentStore() githubapp.DocumentStore {
	return dalgostore.New(dalgo2memory.New(dalgo2memory.FirestoreProfile()))
}

func newPollHarness(t *testing.T, inventory snapshotInventory, mutate func(*Options)) *pollHarness {
	t.Helper()
	github := newFakeGitHub()
	server := httptest.NewServer(github)
	t.Cleanup(server.Close)
	store := newDocumentStore()
	events, _ := hub.NewRepositoryEventStore(store)
	recorder := &recordingEventStore{inner: events}
	lines := &bytes.Buffer{}
	options := Options{
		Client:       server.Client(),
		APIBaseURL:   server.URL,
		Token:        func() (string, error) { return "operator-token", nil },
		Snapshots:    inventory,
		Events:       recorder,
		Observations: hub.NewPollObservationStore(store),
		Machine:      localMachine,
		Interval:     time.Minute,
		Now:          func() time.Time { return pollClock },
		Narrate:      narrate.Writer{Out: lines}.Write,
	}
	if mutate != nil {
		mutate(&options)
	}
	return &pollHarness{poller: New(options), github: github, server: server, lines: lines, events: recorder, options: &options}
}

// narrated returns the lines written since the last call, so each tick's
// output can be asserted on its own.
func (harness *pollHarness) narrated() []string {
	out := strings.TrimSuffix(harness.lines.String(), "\n")
	harness.lines.Reset()
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func (harness *pollHarness) wantLine(t *testing.T, want string) {
	t.Helper()
	lines := harness.narrated()
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("narration\n got %q\nwant %q", lines, want)
	}
}

// TestPollerRecordsFirstSightThenQueuesOnlyWhatChanged is
// AC poll-detects-default-branch-update: the first reading is memory, not an
// event; a moved head enqueues exactly one; and the same head read again is
// "no change".
func TestPollerRecordsFirstSightThenQueuesOnlyWhatChanged(t *testing.T) {
	harness := newPollHarness(t, inventoryOf("github.com/acme/app"), nil)
	harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
	ctx := context.Background()

	harness.poller.Tick(ctx)
	harness.wantLine(t, "14:02:11 poll            github.com/acme/app              no change")
	if len(harness.events.events) != 0 {
		t.Fatalf("first sight enqueued %+v; the head it is already at was not a push the daemon missed", harness.events.events)
	}
	if got := harness.poller.Repositories(); got != 1 {
		t.Fatalf("repositories polled = %d", got)
	}
	if got := harness.github.requestCount(); got != 2 {
		t.Fatalf("requests = %d; one tick must make exactly one pair per repository", got)
	}

	harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: secondSHA})
	harness.poller.Tick(ctx)
	harness.wantLine(t, "14:02:11 poll            github.com/acme/app              default branch main -> 2222222; queued for laptop")
	if len(harness.events.events) != 1 {
		t.Fatalf("events = %+v", harness.events.events)
	}
	event := harness.events.events[0]
	if event.Reason != repositoryevent.ReasonDefaultBranchUpdated || event.Ref != "refs/heads/main" || event.TargetSHA != secondSHA || event.OccurredAt == nil {
		t.Fatalf("event = %+v", event)
	}
	if event.ID != "poll:acme_app:default_branch_updated:"+secondSHA {
		t.Fatalf("event id %q is not derived from repository, reason and target", event.ID)
	}

	harness.poller.Tick(ctx)
	harness.wantLine(t, "14:02:11 poll            github.com/acme/app              no change")
	if len(harness.events.events) != 1 {
		t.Fatalf("a second reading of the same head enqueued again: %+v", harness.events.events)
	}
}

// TestPollerTreatsADefaultBranchSwitchAsAnUpdate covers the other half of the
// comparison: the branch itself moved, not just its head.
func TestPollerTreatsADefaultBranchSwitchAsAnUpdate(t *testing.T) {
	harness := newPollHarness(t, inventoryOf("github.com/acme/app"), nil)
	harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "master", sha: firstSHA})
	ctx := context.Background()
	harness.poller.Tick(ctx)
	harness.narrated()

	harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
	harness.poller.Tick(ctx)
	harness.wantLine(t, "14:02:11 poll            github.com/acme/app              default branch main -> 1111111; queued for laptop")
	if len(harness.events.events) != 1 || harness.events.events[0].Ref != "refs/heads/main" {
		t.Fatalf("events = %+v", harness.events.events)
	}
}

// TestPollerDetectsARenameAndCarriesThePreviousName is
// AC poll-detects-rename: the event names where the clone must be moved from,
// and the observation is re-keyed so the old name is never compared again.
func TestPollerDetectsARenameAndCarriesThePreviousName(t *testing.T) {
	harness := newPollHarness(t, inventoryOf("github.com/acme/app"), nil)
	harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
	ctx := context.Background()
	harness.poller.Tick(ctx)
	harness.narrated()

	// GitHub redirects the old path to the renamed repository, so the poller
	// still asks for acme/app and is answered with the new full_name.
	harness.github.set("acme/app", &fakeRepository{fullName: "acme/renamed", defaultBranch: "main", sha: firstSHA})
	harness.poller.Tick(ctx)
	harness.wantLine(t, "14:02:11 poll            github.com/acme/app              renamed from acme/app; queued for laptop")
	if len(harness.events.events) != 1 {
		t.Fatalf("events = %+v", harness.events.events)
	}
	event := harness.events.events[0]
	if event.Reason != repositoryevent.ReasonRepositoryRenamed || event.Repository != "github.com/acme/renamed" || event.PreviousRepository != "github.com/acme/app" {
		t.Fatalf("event = %+v", event)
	}

	observations := harness.options.Observations
	if _, found, err := observations.LoadPollObservation(ctx, "github.com/acme/app"); err != nil || found {
		t.Fatalf("the observation was not re-keyed: found=%t err=%v", found, err)
	}
	if stored, found, err := observations.LoadPollObservation(ctx, "github.com/acme/renamed"); err != nil || !found || stored.SHA != firstSHA {
		t.Fatalf("re-keyed observation = %+v, %t, %v", stored, found, err)
	}
}

// TestPollerSkipsRepositoriesAnAppAlreadyCovers is the rule webhook mode
// depends on: a repository delivered by webhook must not also be polled.
func TestPollerSkipsRepositoriesAnAppAlreadyCovers(t *testing.T) {
	harness := newPollHarness(t, inventoryOf("github.com/acme/app", "github.com/acme/covered"), func(options *Options) {
		options.Covered = func(repository string) bool { return repository == "github.com/acme/covered" }
	})
	harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
	harness.poller.Tick(context.Background())
	harness.wantLine(t, "14:02:11 poll            github.com/acme/app              no change")
	if got := harness.poller.Repositories(); got != 1 {
		t.Fatalf("repositories polled = %d; a covered repository must not be counted", got)
	}
	if got := harness.github.requestCount(); got != 2 {
		t.Fatalf("requests = %d; a covered repository must not be asked for", got)
	}
}

// TestPollerWaitsForTheRateLimitWindowToReset proves the poller spends the
// operator's remaining budget on the next tick only when there is enough of
// it, and says so on the console when there is not.
func TestPollerWaitsForTheRateLimitWindowToReset(t *testing.T) {
	harness := newPollHarness(t, inventoryOf("github.com/acme/app"), nil)
	harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
	reset := pollClock.Add(7 * time.Minute)
	harness.github.remaining, harness.github.reset = 1, reset

	delay := harness.poller.Tick(context.Background())
	if delay != 7*time.Minute {
		t.Fatalf("delay = %s; the next tick must wait for the window to reset", delay)
	}
	lines := harness.narrated()
	if len(lines) != 2 || lines[1] != "14:02:11 poll            github.com                       rate limited; next poll at "+reset.Local().Format("15:04:05") {
		t.Fatalf("narration = %q", lines)
	}
}

// TestPollerWithBudgetToSpareKeepsItsInterval is the same reading with a
// healthy budget: no waiting, no rate-limit line.
func TestPollerWithBudgetToSpareKeepsItsInterval(t *testing.T) {
	harness := newPollHarness(t, inventoryOf("github.com/acme/app"), nil)
	harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
	harness.github.remaining, harness.github.reset = 5000, pollClock.Add(time.Hour)

	if delay := harness.poller.Tick(context.Background()); delay != time.Minute {
		t.Fatalf("delay = %s", delay)
	}
	harness.wantLine(t, "14:02:11 poll            github.com/acme/app              no change")
}

// TestPollerWaitsNoLessThanZeroWhenTheWindowHasAlreadyReset guards the clamp:
// a reset in the past must not become a negative delay.
func TestPollerWaitsNoLessThanZeroWhenTheWindowHasAlreadyReset(t *testing.T) {
	harness := newPollHarness(t, inventoryOf("github.com/acme/app"), nil)
	harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
	harness.github.remaining, harness.github.reset = 0, pollClock.Add(-time.Minute)

	if delay := harness.poller.Tick(context.Background()); delay != 0 {
		t.Fatalf("delay = %s", delay)
	}
}

// TestPollerBacksOffExponentiallyOnTransientFailures is the degradation the
// plan's risk section demands: a GitHub incident must lengthen the interval,
// not fail the daemon, and recovery must restore it.
func TestPollerBacksOffExponentiallyOnTransientFailures(t *testing.T) {
	harness := newPollHarness(t, inventoryOf("github.com/acme/app"), nil)
	harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
	harness.github.status = http.StatusInternalServerError
	ctx := context.Background()

	if delay := harness.poller.Tick(ctx); delay != time.Minute {
		t.Fatalf("first failure delay = %s", delay)
	}
	harness.wantLine(t, "14:02:11 poll            github.com/acme/app              error: github responded 500")
	if delay := harness.poller.Tick(ctx); delay != 2*time.Minute {
		t.Fatalf("second failure delay = %s", delay)
	}
	harness.narrated()
	if delay := harness.poller.Tick(ctx); delay != 4*time.Minute {
		t.Fatalf("third failure delay = %s", delay)
	}
	harness.narrated()

	harness.github.status = 0
	if delay := harness.poller.Tick(ctx); delay != time.Minute {
		t.Fatalf("recovered delay = %s; a clean tick must restore the configured interval", delay)
	}
}

// TestPollerBackoffIsCapped keeps a long incident from pushing the next tick
// past the ten-minute ceiling.
func TestPollerBackoffIsCapped(t *testing.T) {
	harness := newPollHarness(t, inventoryOf("github.com/acme/app"), func(options *Options) { options.Interval = 8 * time.Minute })
	harness.github.status = http.StatusTooManyRequests
	ctx := context.Background()
	harness.poller.Tick(ctx)
	if delay := harness.poller.Tick(ctx); delay != MaxBackoff {
		t.Fatalf("capped delay = %s", delay)
	}
	if delay := harness.poller.Tick(ctx); delay != MaxBackoff {
		t.Fatalf("delay stayed above the cap: %s", delay)
	}
}

// TestPollerNarratesAFailureToReadTheToken is the operator's first hour: the
// token path is wrong, and the console has to say so without printing a
// token that was never read.
func TestPollerNarratesAFailureToReadTheToken(t *testing.T) {
	harness := newPollHarness(t, inventoryOf("github.com/acme/app"), func(options *Options) {
		options.Token = func() (string, error) { return "", errors.New("read hub GitHub token file: no such file") }
	})
	if delay := harness.poller.Tick(context.Background()); delay != time.Minute {
		t.Fatalf("delay = %s", delay)
	}
	harness.wantLine(t, "14:02:11 poll            github.com                       error: read hub GitHub token file: no such file")
	if harness.github.requestCount() != 0 {
		t.Fatal("a tick without a token must not reach GitHub")
	}
}

// TestPollerNarratesFailuresItCannotRetreatFrom covers every remaining way a
// tick can fail, each of which must be one line and no event.
func TestPollerNarratesFailuresItCannotRetreatFrom(t *testing.T) {
	ctx := context.Background()

	t.Run("the inventory cannot be listed", func(t *testing.T) {
		harness := newPollHarness(t, snapshotInventory{err: errors.New("backend is down")}, nil)
		harness.poller.Tick(ctx)
		harness.wantLine(t, "14:02:11 poll            github.com                       error: list machine snapshots")
	})

	t.Run("there is no snapshot store at all", func(t *testing.T) {
		harness := newPollHarness(t, snapshotInventory{}, func(options *Options) { options.Snapshots = nil })
		harness.poller.Tick(ctx)
		harness.wantLine(t, "14:02:11 poll            github.com                       error: machine snapshot store is unavailable")
	})

	t.Run("a repository name is not owner/repository", func(t *testing.T) {
		harness := newPollHarness(t, inventoryOf("github.com/acme"), nil)
		harness.poller.Tick(ctx)
		harness.wantLine(t, `14:02:11 poll            github.com/acme                  error: "github.com/acme" is not an owner/repository name`)
	})

	t.Run("github does not know the repository", func(t *testing.T) {
		harness := newPollHarness(t, inventoryOf("github.com/acme/gone"), nil)
		harness.poller.Tick(ctx)
		harness.wantLine(t, "14:02:11 poll            github.com/acme/gone             error: github responded 404")
	})

	t.Run("the enqueue is refused", func(t *testing.T) {
		harness := newPollHarness(t, inventoryOf("github.com/acme/app"), nil)
		harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
		harness.poller.Tick(ctx)
		harness.narrated()
		harness.events.err = errors.New("store is unavailable")
		harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: secondSHA})
		harness.poller.Tick(ctx)
		harness.wantLine(t, "14:02:11 poll            github.com/acme/app              error: enqueue default_branch_updated: store is unavailable")
	})

	t.Run("there is no observation store", func(t *testing.T) {
		harness := newPollHarness(t, inventoryOf("github.com/acme/app"), func(options *Options) { options.Observations = nil })
		harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
		harness.poller.Tick(ctx)
		harness.wantLine(t, "14:02:11 poll            github.com/acme/app              error: poll observation store is unavailable")
	})

	t.Run("the observation cannot be read", func(t *testing.T) {
		harness := newPollHarness(t, inventoryOf("github.com/acme/app"), func(options *Options) {
			options.Observations = failingObservations{err: errors.New("backend is down")}
		})
		harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
		harness.poller.Tick(ctx)
		harness.wantLine(t, "14:02:11 poll            github.com/acme/app              error: backend is down")
	})

	t.Run("the first observation cannot be saved", func(t *testing.T) {
		harness := newPollHarness(t, inventoryOf("github.com/acme/app"), func(options *Options) {
			options.Observations = failingObservations{saveErr: errors.New("disk is full")}
		})
		harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
		harness.poller.Tick(ctx)
		harness.wantLine(t, "14:02:11 poll            github.com/acme/app              error: disk is full")
	})

	t.Run("github answers without a default branch", func(t *testing.T) {
		harness := newPollHarness(t, inventoryOf("github.com/acme/app"), nil)
		harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "", sha: firstSHA})
		harness.poller.Tick(ctx)
		harness.wantLine(t, "14:02:11 poll            github.com/acme/app              error: github returned no full_name or default_branch")
	})

	t.Run("the request cannot be made at all", func(t *testing.T) {
		harness := newPollHarness(t, inventoryOf("github.com/acme/app"), nil)
		harness.server.Close()
		harness.poller.Tick(ctx)
		lines := harness.narrated()
		if len(lines) != 1 || !strings.Contains(lines[0], "error: reach github:") {
			t.Fatalf("narration = %q", lines)
		}
	})
}

// TestPollerReportsAnEmptyHeadCommit covers the one malformed answer that
// passes repository validation but leaves nothing to enqueue.
func TestPollerReportsAnEmptyHeadCommit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "/commits/") {
			_, _ = writer.Write([]byte(`{"sha":""}`))
			return
		}
		_, _ = writer.Write([]byte(`{"id":1,"full_name":"acme/app","default_branch":"main"}`))
	}))
	defer server.Close()
	lines := &bytes.Buffer{}
	store := newDocumentStore()
	events, _ := hub.NewRepositoryEventStore(store)
	poller := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		Token:     func() (string, error) { return "operator-token", nil },
		Snapshots: inventoryOf("github.com/acme/app"), Events: events,
		Observations: hub.NewPollObservationStore(store), Machine: localMachine,
		Interval: time.Minute, Now: func() time.Time { return pollClock },
		Narrate: narrate.Writer{Out: lines}.Write,
	})
	poller.Tick(context.Background())
	if !strings.Contains(lines.String(), "error: github returned no head commit sha") {
		t.Fatalf("narration = %q", lines.String())
	}
}

// TestPollerRejectsAnUndecodableResponse covers the body that is not JSON at
// all, which a captive proxy produces.
func TestPollerRejectsAnUndecodableResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("<html>sign in to your wifi</html>"))
	}))
	defer server.Close()
	lines := &bytes.Buffer{}
	store := newDocumentStore()
	events, _ := hub.NewRepositoryEventStore(store)
	poller := New(Options{
		Client: server.Client(), APIBaseURL: server.URL + "/",
		Token:     func() (string, error) { return "operator-token", nil },
		Snapshots: inventoryOf("github.com/acme/app"), Events: events,
		Observations: hub.NewPollObservationStore(store), Machine: localMachine,
		Interval: time.Minute, Now: func() time.Time { return pollClock },
		Narrate: narrate.Writer{Out: lines}.Write,
	})
	poller.Tick(context.Background())
	if !strings.Contains(lines.String(), "error: decode github response:") {
		t.Fatalf("narration = %q", lines.String())
	}
}

// TestPollerRejectsAnUnbuildableRequest covers the base URL that cannot form
// a request at all, which a mistyped hub.store.url would produce.
func TestPollerRejectsAnUnbuildableRequest(t *testing.T) {
	lines := &bytes.Buffer{}
	store := newDocumentStore()
	events, _ := hub.NewRepositoryEventStore(store)
	poller := New(Options{
		Client: http.DefaultClient, APIBaseURL: "http://\x7f",
		Token:     func() (string, error) { return "operator-token", nil },
		Snapshots: inventoryOf("github.com/acme/app"), Events: events,
		Observations: hub.NewPollObservationStore(store), Machine: localMachine,
		Interval: time.Minute, Now: func() time.Time { return pollClock },
		Narrate: narrate.Writer{Out: lines}.Write,
	})
	poller.Tick(context.Background())
	if !strings.Contains(lines.String(), "error: build github request:") {
		t.Fatalf("narration = %q", lines.String())
	}
}

// TestPollerRunTicksUntilTheContextEnds is the daemon's own use: Run stops
// cleanly on shutdown and reports no error for it.
func TestPollerRunTicksUntilTheContextEnds(t *testing.T) {
	ticks := 0
	harness := newPollHarness(t, inventoryOf("github.com/acme/app"), nil)
	harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
	ctx, cancel := context.WithCancel(context.Background())
	harness.poller = New(Options{
		Client: harness.server.Client(), APIBaseURL: harness.server.URL,
		Token:     func() (string, error) { return "operator-token", nil },
		Snapshots: inventoryOf("github.com/acme/app"), Events: harness.events,
		Observations: hub.NewPollObservationStore(newDocumentStore()), Machine: localMachine,
		Interval: time.Minute, Now: func() time.Time { return pollClock },
		Narrate: narrate.Writer{Out: harness.lines}.Write,
		Sleep: func(context.Context, time.Duration) error {
			ticks++
			if ticks == 2 {
				cancel()
			}
			return nil
		},
	})
	if err := harness.poller.Run(ctx); err != nil {
		t.Fatalf("Run = %v; a cancelled poller is a clean shutdown", err)
	}
	if ticks != 2 {
		t.Fatalf("ticks = %d", ticks)
	}
}

// TestPollerRunStopsWhenTheSleepIsInterrupted is the other shutdown path: the
// context ends while the poller is between ticks.
func TestPollerRunStopsWhenTheSleepIsInterrupted(t *testing.T) {
	harness := newPollHarness(t, inventoryOf("github.com/acme/app"), func(options *Options) {
		options.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	})
	harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
	if err := harness.poller.Run(context.Background()); err != nil {
		t.Fatalf("Run = %v", err)
	}
}

// TestNewDefaultsEverythingOptional proves a caller can supply only the
// required fields, which is what the daemon does for Covered and what a
// production build does for the API base URL and the clock.
func TestNewDefaultsEverythingOptional(t *testing.T) {
	poller := New(Options{})
	if poller.options.APIBaseURL != DefaultAPIBaseURL || poller.options.Interval != DefaultInterval {
		t.Fatalf("defaults = %q, %s", poller.options.APIBaseURL, poller.options.Interval)
	}
	if poller.options.Client == nil || poller.options.Now == nil || poller.options.Sleep == nil {
		t.Fatal("New left a required collaborator nil")
	}
	if poller.options.Covered("github.com/acme/app") {
		t.Fatal("nothing is covered without a GitHub App")
	}
	// The default narrator discards, and the default sleep really waits, so
	// both are exercised with values that cost nothing.
	poller.options.Narrate(narrate.Line{At: pollClock, Event: eventName, Subject: "github.com", Action: "no change"})
	if err := poller.options.Sleep(context.Background(), time.Nanosecond); err != nil {
		t.Fatalf("default sleep = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := poller.options.Sleep(cancelled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sleep = %v", err)
	}
}

// TestTransientDelayFallsBackToTheInterval covers the guard that keeps a
// zeroed backoff from returning a zero delay and spinning.
func TestTransientDelayFallsBackToTheInterval(t *testing.T) {
	poller := New(Options{Interval: time.Minute})
	poller.backoff = 0
	if delay := poller.transientDelay(); delay != time.Minute {
		t.Fatalf("delay = %s", delay)
	}
}

// TestShortSHALeavesAShortValueAlone guards the abbreviation against a value
// that is already shorter than the seven characters the console shows.
func TestShortSHALeavesAShortValueAlone(t *testing.T) {
	if got := shortSHA("abc"); got != "abc" {
		t.Fatalf("shortSHA = %q", got)
	}
}

// TestCanonicalRepositoryAcceptsEitherSpelling matches the machine snapshot
// contract, which stores the prefixed form.
func TestCanonicalRepositoryAcceptsEitherSpelling(t *testing.T) {
	if got := canonicalRepository("github.com/Acme/App"); got != "github.com/acme/app" {
		t.Fatalf("prefixed = %q", got)
	}
	if got := canonicalRepository(" Acme/App "); got != "github.com/acme/app" {
		t.Fatalf("bare = %q", got)
	}
}

// TestRateLimitHeadersAreOptional keeps a proxy that strips them from being
// read as "no budget left".
func TestRateLimitHeadersAreOptional(t *testing.T) {
	if readRateLimit(http.Header{}).known {
		t.Fatal("absent headers must not be read as a known budget")
	}
	header := http.Header{}
	header.Set("X-RateLimit-Remaining", "10")
	header.Set("X-RateLimit-Reset", "not-a-number")
	if readRateLimit(header).known {
		t.Fatal("an unparsable reset must not be read as a known budget")
	}
}

// failingObservations is a store whose every call can be made to fail, so
// each of the poller's storage error branches is reachable.
type failingObservations struct {
	err     error
	saveErr error
}

func (store failingObservations) LoadPollObservation(context.Context, string) (hub.PollObservation, bool, error) {
	if store.err != nil {
		return hub.PollObservation{}, false, store.err
	}
	return hub.PollObservation{}, false, nil
}

func (store failingObservations) SavePollObservation(context.Context, hub.PollObservation) error {
	return store.saveErr
}

func (store failingObservations) DeletePollObservation(context.Context, string) error { return nil }

// flakyObservations wraps a real observation store and fails one chosen call,
// so each write the poller performs after a successful GitHub read has its
// failure branch exercised with everything else behaving normally.
type flakyObservations struct {
	inner hub.PollObservationStore
	// failSaveOn is the 1-based ordinal of the SavePollObservation call that
	// fails; 0 never fails.
	failSaveOn int
	saves      int
	deleteErr  error
}

func (store *flakyObservations) LoadPollObservation(ctx context.Context, repository string) (hub.PollObservation, bool, error) {
	return store.inner.LoadPollObservation(ctx, repository)
}

func (store *flakyObservations) SavePollObservation(ctx context.Context, observation hub.PollObservation) error {
	store.saves++
	if store.saves == store.failSaveOn {
		return errors.New("disk is full")
	}
	return store.inner.SavePollObservation(ctx, observation)
}

func (store *flakyObservations) DeletePollObservation(ctx context.Context, repository string) error {
	if store.deleteErr != nil {
		return store.deleteErr
	}
	return store.inner.DeletePollObservation(ctx, repository)
}

// TestPollerNarratesFailuresAfterAGoodReading covers every way a tick can
// fail once GitHub has already answered: the head request, the enqueue, and
// each of the two writes a rename performs. None of them may leave a silent
// tick, because a silent tick is indistinguishable from "nothing changed".
func TestPollerNarratesFailuresAfterAGoodReading(t *testing.T) {
	ctx := context.Background()

	t.Run("the head commit request fails", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if strings.Contains(request.URL.Path, "/commits/") {
				writer.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = writer.Write([]byte(`{"id":1,"full_name":"acme/app","default_branch":"main"}`))
		}))
		defer server.Close()
		lines := &bytes.Buffer{}
		store := newDocumentStore()
		events, _ := hub.NewRepositoryEventStore(store)
		poller := New(Options{
			Client: server.Client(), APIBaseURL: server.URL,
			Token:     func() (string, error) { return "operator-token", nil },
			Snapshots: inventoryOf("github.com/acme/app"), Events: events,
			Observations: hub.NewPollObservationStore(store), Machine: localMachine,
			Interval: time.Minute, Now: func() time.Time { return pollClock },
			Narrate: narrate.Writer{Out: lines}.Write,
		})
		if delay := poller.Tick(ctx); delay != time.Minute {
			t.Fatalf("delay = %s", delay)
		}
		if !strings.Contains(lines.String(), "error: github responded 502") {
			t.Fatalf("narration = %q", lines.String())
		}
	})

	t.Run("a rename cannot be enqueued", func(t *testing.T) {
		harness := newPollHarness(t, inventoryOf("github.com/acme/app"), nil)
		harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
		harness.poller.Tick(ctx)
		harness.narrated()
		harness.events.err = errors.New("store is unavailable")
		harness.github.set("acme/app", &fakeRepository{fullName: "acme/renamed", defaultBranch: "main", sha: firstSHA})
		harness.poller.Tick(ctx)
		harness.wantLine(t, "14:02:11 poll            github.com/acme/app              error: enqueue repository_renamed: store is unavailable")
	})

	t.Run("a rename cannot be recorded under its new name", func(t *testing.T) {
		var flaky *flakyObservations
		harness := newPollHarness(t, inventoryOf("github.com/acme/app"), func(options *Options) {
			flaky = &flakyObservations{inner: options.Observations, failSaveOn: 2}
			options.Observations = flaky
		})
		harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
		harness.poller.Tick(ctx)
		harness.narrated()
		harness.github.set("acme/app", &fakeRepository{fullName: "acme/renamed", defaultBranch: "main", sha: firstSHA})
		harness.poller.Tick(ctx)
		harness.wantLine(t, "14:02:11 poll            github.com/acme/app              error: disk is full")
	})

	t.Run("the name a rename moved away from cannot be dropped", func(t *testing.T) {
		harness := newPollHarness(t, inventoryOf("github.com/acme/app"), func(options *Options) {
			options.Observations = &flakyObservations{inner: options.Observations, deleteErr: errors.New("backend is down")}
		})
		harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
		harness.poller.Tick(ctx)
		harness.narrated()
		harness.github.set("acme/app", &fakeRepository{fullName: "acme/renamed", defaultBranch: "main", sha: firstSHA})
		harness.poller.Tick(ctx)
		harness.wantLine(t, "14:02:11 poll            github.com/acme/app              error: backend is down")
	})

	t.Run("a moved head cannot be recorded", func(t *testing.T) {
		harness := newPollHarness(t, inventoryOf("github.com/acme/app"), func(options *Options) {
			options.Observations = &flakyObservations{inner: options.Observations, failSaveOn: 2}
		})
		harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: firstSHA})
		harness.poller.Tick(ctx)
		harness.narrated()
		harness.github.set("acme/app", &fakeRepository{fullName: "acme/app", defaultBranch: "main", sha: secondSHA})
		harness.poller.Tick(ctx)
		harness.wantLine(t, "14:02:11 poll            github.com/acme/app              error: disk is full")
	})
}
