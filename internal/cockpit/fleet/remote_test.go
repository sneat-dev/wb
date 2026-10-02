package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// vmKey is the key the local configuration gives the other machine in these
// tests, and vmOwnName the name that machine gives itself, which is never used.
const (
	vmKey     = "vm"
	vmOwnName = "exporter-own-name"
)

// vmSources is another machine's own state: one repository with three
// worktrees, a recorded pull request for the first and a running run.
func vmSources() *fakeSources {
	repo := discover.Repo{Host: "github.com", Org: "acme", Name: "engine", Path: "/vm/engine"}
	now := newClock().Now()
	sources := &fakeSources{
		repos: []discover.Repo{repo}, branch: "main",
		worktrees: map[string][]LinkedWorktree{"acme/engine": {}},
		records:   map[string]WorktreeRecord{},
		branches:  map[string][]BranchRef{"acme/engine": {}},
		bindings:  []worktrees.RegisteredPullRequestBinding{{Task: "vm-task-1", Repository: "acme/engine", PullRequest: 41, URL: "https://github.com/acme/engine/pull/41"}},
		runs:      []agents.Result{{AgentID: "agt-vm", State: agents.StateRunning, Repository: "acme/engine", StartedAt: now.Add(-time.Minute)}},
	}
	for index, owner := range []string{worktrees.OwnerLive, worktrees.OwnerGone, ""} {
		task, branch, path := fmt.Sprintf("vm-task-%d", index+1), fmt.Sprintf("feature/vm-%d", index+1), fmt.Sprintf("/vm/wt-%d", index+1)
		sources.worktrees["acme/engine"] = append(sources.worktrees["acme/engine"], LinkedWorktree{Path: path, Branch: branch})
		sources.records[path] = WorktreeRecord{Task: task, Branch: branch, CreatedAt: now.Add(-time.Hour), Owner: owner}
		sources.branches["acme/engine"] = append(sources.branches["acme/engine"], BranchRef{Name: branch, Scope: BranchLocal, Upstream: "origin/" + branch, Ahead: index + 1, Behind: index})
	}
	return sources
}

// exportOf is the envelope a machine that calls itself name exports of sources,
// with samples metric samples.
func exportOf(t *testing.T, name string, sources *fakeSources, samples int, metricsOnly bool) Envelope {
	t.Helper()
	snapshotter, _ := newSnapshotter(sources.collectors(), func(options *Options) {
		options.Machine = name
		if samples > 0 {
			options.Sampler = filledSampler(t, &countingSource{}, samples)
		}
	})
	refreshAndSettle(t, snapshotter)
	return snapshotter.Export(metricsOnly)
}

// fakeExporter is a RemoteExporter that answers from a function and records
// each call's time and whether it was metrics-only.
type fakeExporter struct {
	mu          sync.Mutex
	clock       *manualClock
	times       []time.Duration
	metricsOnly []bool
	answer      func(target RemoteTarget, metricsOnly bool) (Envelope, error)
}

func (f *fakeExporter) Export(_ context.Context, target RemoteTarget, metricsOnly bool) (Envelope, error) {
	f.mu.Lock()
	if f.clock != nil {
		f.times = append(f.times, f.clock.Now().Sub(newClock().Now()))
	}
	f.metricsOnly = append(f.metricsOnly, metricsOnly)
	answer := f.answer
	f.mu.Unlock()
	return answer(target, metricsOnly)
}

func (f *fakeExporter) set(answer func(RemoteTarget, bool) (Envelope, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer = answer
}

func (f *fakeExporter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.metricsOnly)
}

// callsAt is the seconds since the start of the test's clock of each call of
// the given kind.
func (f *fakeExporter) callsAt(metricsOnly bool) []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var seconds []int
	for index, at := range f.times {
		if f.metricsOnly[index] == metricsOnly {
			seconds = append(seconds, int(at/time.Second))
		}
	}
	return seconds
}

// answering is an exporter answer that always gives full for a fleet export
// and only for a metrics-only one.
func answering(full, only Envelope) func(RemoteTarget, bool) (Envelope, error) {
	return func(_ RemoteTarget, metricsOnly bool) (Envelope, error) {
		if metricsOnly {
			return only, nil
		}
		return full, nil
	}
}

func failing(err error) func(RemoteTarget, bool) (Envelope, error) {
	return func(RemoteTarget, bool) (Envelope, error) { return Envelope{}, err }
}

// newLive is a snapshotter of the local machine that reads the machine vmKey
// through exporter, named as the HTTP transport.
func newLive(t *testing.T, sources *fakeSources, exporter RemoteExporter, change func(*Options)) (*Snapshotter, *manualClock) {
	t.Helper()
	return newSnapshotter(sources.collectors(), func(options *Options) {
		options.Login = testLogin
		options.Remotes = []RemoteTarget{{Machine: vmKey}}
		options.Transports = []RemoteTransport{{Name: TransportHTTP, Exporter: exporter}}
		if change != nil {
			change(options)
		}
	})
}

// pollAndSettle runs one look of the background loop, as it is while an owner
// session reads the fleet document (the reader that is demand for every
// transport), and waits for the exports it started. pollIdle is the same look
// with no reader.
func pollAndSettle(t *testing.T, snapshotter *Snapshotter) {
	t.Helper()
	snapshotter.fleetRead(demandOwner)
	snapshotter.pollRemotes(t.Context())
	snapshotter.side.Wait()
}

func pollIdle(t *testing.T, snapshotter *Snapshotter) {
	t.Helper()
	snapshotter.pollRemotes(t.Context())
	snapshotter.side.Wait()
}

func machineNamed(document Document, name string) (Machine, bool) {
	for _, machine := range document.Machines {
		if machine.Machine == name {
			return machine, true
		}
	}
	return Machine{}, false
}

// entriesOf is the ids of every entry of the machine with id, by collection.
func entriesOf(document Document, machineID string) map[string][]string {
	ids := map[string][]string{}
	for _, item := range document.Repositories {
		if item.MachineID == machineID {
			ids["repositories"] = append(ids["repositories"], item.ID)
		}
	}
	for _, item := range document.Worktrees {
		if item.MachineID == machineID {
			ids["worktrees"] = append(ids["worktrees"], item.ID)
		}
	}
	for _, item := range document.PullRequests {
		if item.MachineID == machineID {
			ids["pull_requests"] = append(ids["pull_requests"], item.ID)
		}
	}
	for _, item := range document.Agents {
		if item.MachineID == machineID {
			ids["agents"] = append(ids["agents"], item.ID)
		}
	}
	return ids
}

// allIDs is every entry id of a document, with the collection it is in.
func allIDs(document Document) []string {
	var ids []string
	for _, item := range document.Machines {
		ids = append(ids, "machine "+item.ID)
	}
	for _, item := range document.Repositories {
		ids = append(ids, "repository "+item.ID)
	}
	for _, item := range document.Worktrees {
		ids = append(ids, "worktree "+item.ID)
	}
	for _, item := range document.PullRequests {
		ids = append(ids, "pull request "+item.ID)
	}
	for _, item := range document.Agents {
		ids = append(ids, "agent "+item.ID)
	}
	return ids
}

func requireUniqueIDs(t *testing.T, document Document) {
	t.Helper()
	seen := map[string]bool{}
	for _, id := range allIDs(document) {
		if seen[id] {
			t.Errorf("the document holds %s twice", id)
		}
		seen[id] = true
	}
}

// cachedVM is a published-store snapshot of the machine vmKey, 25 days old,
// with one worktree and an open pull request.
func cachedVM(login string) remotestate.Entry {
	return remotestate.Entry{Snapshot: remotestate.Snapshot{
		Login: login, Machine: vmKey, PublishedAt: newClock().Now().Add(-25 * 24 * time.Hour), WBVersion: "v0.1.0",
		KnownRepositories: []string{"acme/engine"},
		Worktrees: []remotestate.WorktreeState{{
			Task: "stale-task", Repository: "acme/engine", Branch: "feature/stale", Lifecycle: "working",
			PullRequest: &remotestate.PullRequestState{Number: 9, State: "OPEN", URL: "https://github.com/acme/engine/pull/9"},
		}},
	}}
}

// TestRemoteFailureNamesTheCodeAndWhetherToFallBack is the rule that turns an
// exporter's error into a remote_error code and the decision to try the next
// transport: a refused envelope is bad_payload and never falls back, whatever
// wraps it; a typed failure keeps its code and its decision; a code outside the
// vocabulary, an untyped error and a panic are the transport being unavailable.
func TestRemoteFailureNamesTheCodeAndWhetherToFallBack(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		transport string
		err       error
		want      RemoteError
	}{
		"a refused envelope over http":   {TransportHTTP, refuse("an unknown field"), RemoteError{Code: RemoteErrorBadPayload}},
		"a refused envelope over ssh":    {TransportSSH, refuse("an unknown field"), RemoteError{Code: RemoteErrorBadPayload}},
		"a wrapped refusal":              {TransportHTTP, fmt.Errorf("decode: %w", refuse("x")), RemoteError{Code: RemoteErrorBadPayload}},
		"a typed fallback failure":       {TransportHTTP, &RemoteError{Code: RemoteErrorHTTPAuthFailed, Fallback: true}, RemoteError{Code: RemoteErrorHTTPAuthFailed, Fallback: true}},
		"a typed final failure":          {TransportHTTP, &RemoteError{Code: RemoteErrorHTTPUnavailable}, RemoteError{Code: RemoteErrorHTTPUnavailable}},
		"a typed ssh failure":            {TransportSSH, &RemoteError{Code: RemoteErrorWBTooOld}, RemoteError{Code: RemoteErrorWBTooOld}},
		"a code outside the vocabulary":  {TransportHTTP, &RemoteError{Code: sentinel + "code"}, RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}},
		"an untyped error over http":     {TransportHTTP, errBoom, RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}},
		"an untyped error over ssh":      {TransportSSH, errBoom, RemoteError{Code: RemoteErrorSSHUnavailable, Fallback: true}},
		"a panic":                        {TransportHTTP, errPanicked, RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}},
		"an untyped error, unnamed":      {"", context.DeadlineExceeded, RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}},
		"a refusal beside a typed error": {TransportHTTP, errors.Join(&RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}, refuse("x")), RemoteError{Code: RemoteErrorBadPayload}},
	} {
		if got := remoteFailure(test.transport, test.err); got != test.want {
			t.Errorf("%s: got %+v, want %+v", name, got, test.want)
		}
	}
	if text := (&RemoteError{Code: RemoteErrorTimeout}).Error(); text != "remote export failed: timeout" {
		t.Errorf("error text = %q", text)
	}
	if len(remoteErrorCodes) != 13 {
		t.Errorf("the vocabulary has %d codes, want the thirteen of the requirement", len(remoteErrorCodes))
	}
}

// TestExportFromTriesTransportsInOrderAndFallsBackOnlyOnFallbackClassFailures
// is the transport chain of cockpit-views#req:remote-exporter-transports, with
// two fake transports standing for HTTP and the fallback: the first that yields
// a valid envelope supplies it, the preferred transport's failure is reported
// beside a fallback's data, a failure that is not fallback-class ends the
// attempt, a transport with no route is skipped without a failure, and an
// envelope is validated whatever the transport claims.
func TestExportFromTriesTransportsInOrderAndFallsBackOnlyOnFallbackClassFailures(t *testing.T) {
	t.Parallel()
	valid := exportOf(t, vmOwnName, vmSources(), 2, false)
	invalid := copyEnvelope(t, valid)
	invalid.Fleet.Worktrees[0].Task = strings.Repeat("x", 10_000)
	warm := copyEnvelope(t, valid)
	warm.Fleet.WarmingUp = true
	now := newClock().Now
	good := func() *fakeExporter { return &fakeExporter{answer: answering(valid, valid)} }
	bad := func(err error) *fakeExporter { return &fakeExporter{answer: failing(err)} }
	for name, test := range map[string]struct {
		first, second           *fakeExporter
		transport, failure      string
		ok                      bool
		firstCalls, secondCalls int
		metricsOnlyAsFull       bool
		warming                 bool
	}{
		"the preferred transport works":       {first: good(), second: good(), transport: TransportHTTP, ok: true, firstCalls: 1},
		"a fallback-class failure falls back": {first: bad(&RemoteError{Code: RemoteErrorHTTPAuthFailed, Fallback: true}), second: good(), transport: TransportSSH, failure: RemoteErrorHTTPAuthFailed, ok: true, firstCalls: 1, secondCalls: 1},
		"a final failure does not":            {first: bad(&RemoteError{Code: RemoteErrorHTTPUnavailable}), second: good(), failure: RemoteErrorHTTPUnavailable, firstCalls: 1},
		"a refused envelope does not":         {first: bad(refuse("x")), second: good(), failure: RemoteErrorBadPayload, firstCalls: 1},
		"an invalid envelope is refused here": {first: &fakeExporter{answer: answering(invalid, invalid)}, second: good(), failure: RemoteErrorBadPayload, firstCalls: 1},
		"no route is skipped":                 {first: bad(ErrNoRoute), second: good(), transport: TransportSSH, ok: true, firstCalls: 1, secondCalls: 1},
		"no route anywhere":                   {first: bad(ErrNoRoute), second: bad(ErrNoRoute), firstCalls: 1, secondCalls: 1},
		"both fail":                           {first: bad(&RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}), second: bad(&RemoteError{Code: RemoteErrorWBMissing}), failure: RemoteErrorWBMissing, firstCalls: 1, secondCalls: 1},
		"a panic is the transport failing":    {first: &fakeExporter{answer: func(RemoteTarget, bool) (Envelope, error) { panic("an exporter panicked") }}, second: good(), transport: TransportSSH, failure: RemoteErrorHTTPUnavailable, ok: true, firstCalls: 1, secondCalls: 1},
		"a warming remote is not a failure":   {first: bad(ErrRemoteWarmingUp), second: good(), warming: true, firstCalls: 1},
		"a warming fleet is never taken":      {first: &fakeExporter{answer: answering(warm, warm)}, second: good(), warming: true, firstCalls: 1},
		"warming after a fallback":            {first: bad(&RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}), second: bad(fmt.Errorf("ssh: %w", ErrRemoteWarmingUp)), warming: true, firstCalls: 1, secondCalls: 1},
		"the wrong shape is refused":          {first: good(), second: good(), failure: RemoteErrorBadPayload, firstCalls: 1, metricsOnlyAsFull: true},
	} {
		transports := []RemoteTransport{{Name: TransportHTTP, Exporter: test.first}, {Name: TransportSSH, Exporter: test.second}}
		result := exportFrom(t.Context(), transports, RemoteTarget{Machine: vmKey}, test.metricsOnlyAsFull, now)
		if result.ok != test.ok || result.transport != test.transport || result.failure != test.failure || result.warming != test.warming || test.first.count() != test.firstCalls || test.second.count() != test.secondCalls {
			t.Errorf("%s: %+v, calls %d and %d", name, result, test.first.count(), test.second.count())
		}
		if result.ok != (result.envelope.Fleet != nil) {
			t.Errorf("%s: an envelope came back with ok %v", name, result.ok)
		}
	}
}

// TestRemoteScheduleSaysWhenAnExportIsDue is the cadence as a pure rule: a
// fleet export when its time has come, a metrics-only one only while a client
// asked within the last 60 seconds, at most every 30, and never while the
// machine is failing.
func TestRemoteScheduleSaysWhenAnExportIsDue(t *testing.T) {
	t.Parallel()
	start := newClock().Now()
	at := func(seconds int) time.Time { return start.Add(time.Duration(seconds) * time.Second) }
	// viewed is a schedule of a machine whose fleet a client is reading.
	viewed := func(schedule remoteSchedule) remoteSchedule {
		schedule.viewed, schedule.idleFleet = at(0), schedule.nextFleet.Add(idleKeepalive)
		return schedule
	}
	for name, test := range map[string]struct {
		schedule           remoteSchedule
		now                time.Time
		fetch, metricsOnly bool
	}{
		"a new machine is read at once":      {remoteSchedule{}, at(0), true, false},
		"not before its time":                {viewed(remoteSchedule{nextFleet: at(60)}), at(59), false, false},
		"at its time":                        {viewed(remoteSchedule{nextFleet: at(60)}), at(60), true, false},
		"metrics while asked":                {viewed(remoteSchedule{nextFleet: at(60), nextMetrics: at(30), asked: at(20)}), at(30), true, true},
		"not before thirty seconds":          {viewed(remoteSchedule{nextFleet: at(60), nextMetrics: at(30), asked: at(20)}), at(29), false, false},
		"not when nobody asked":              {viewed(remoteSchedule{nextFleet: at(60), nextMetrics: at(30)}), at(45), false, false},
		"sixty seconds after the last ask":   {viewed(remoteSchedule{nextFleet: at(600), nextMetrics: at(30), asked: at(20)}), at(80), true, true},
		"not after the window closes":        {viewed(remoteSchedule{nextFleet: at(600), nextMetrics: at(30), asked: at(20)}), at(81), false, false},
		"not while the machine is failing":   {viewed(remoteSchedule{nextFleet: at(600), nextMetrics: at(30), asked: at(40), failures: 1}), at(45), false, false},
		"the fleet export comes first":       {viewed(remoteSchedule{nextFleet: at(60), nextMetrics: at(30), asked: at(55)}), at(60), true, false},
		"a failing machine's fleet is still": {viewed(remoteSchedule{nextFleet: at(60), failures: 3}), at(60), true, false},
		// With no reader of the fleet document a machine is read once per keepalive.
		"nobody ever read: not at its interval":  {remoteSchedule{nextFleet: at(60), idleFleet: at(900)}, at(60), false, false},
		"nobody ever read: at its keepalive":     {remoteSchedule{nextFleet: at(60), idleFleet: at(900)}, at(900), true, false},
		"nobody ever read: not before it":        {remoteSchedule{nextFleet: at(60), idleFleet: at(900)}, at(899), false, false},
		"a reader five minutes ago still counts": {remoteSchedule{nextFleet: at(60), idleFleet: at(900), viewed: at(100)}, at(400), true, false},
		"a reader longer ago does not":           {remoteSchedule{nextFleet: at(60), idleFleet: at(900), viewed: at(100)}, at(401), false, false},
		"the first reader after a quiet time":    {remoteSchedule{nextFleet: at(60), idleFleet: at(900), viewed: at(700)}, at(700), true, false},
		"a reader does not hurry the interval":   {remoteSchedule{nextFleet: at(760), idleFleet: at(1600), viewed: at(730)}, at(759), false, false},
		"metrics are asked for with no reader":   {remoteSchedule{nextFleet: at(60), idleFleet: at(900), nextMetrics: at(30), asked: at(20)}, at(30), true, true},
		"a failing machine with no reader waits": {remoteSchedule{nextFleet: at(120), idleFleet: at(900), failures: 1}, at(120), false, false},
		// Every export is a login: none of either kind starts within 30 seconds of
		// the last one's start.
		"a fleet export too soon after a login": {viewed(remoteSchedule{nextFleet: at(70), earliest: at(90)}), at(70), false, false},
		"a fleet export once the floor passed":  {viewed(remoteSchedule{nextFleet: at(70), earliest: at(90)}), at(90), true, false},
		"metrics too soon after a login":        {viewed(remoteSchedule{nextFleet: at(600), nextMetrics: at(30), asked: at(40), earliest: at(75)}), at(60), false, false},
		"metrics once the floor passed":         {viewed(remoteSchedule{nextFleet: at(600), nextMetrics: at(30), asked: at(40), earliest: at(75)}), at(75), true, true},
		"a keepalive too soon after a login":    {remoteSchedule{idleFleet: at(900), earliest: at(920)}, at(900), false, false},
		// A refused SSH login bars SSH: a machine with no other transport waits.
		"ssh alone and barred":                     {viewed(remoteSchedule{nextFleet: at(120), authUntil: at(240)}), at(120), false, false},
		"ssh alone, the bar over":                  {viewed(remoteSchedule{nextFleet: at(120), authUntil: at(240)}), at(240), true, false},
		"another transport is not held by the bar": {viewed(remoteSchedule{nextFleet: at(120), authUntil: at(240)}), at(120), true, false},
	} {
		if fetch, metricsOnly := test.schedule.due(test.now, strings.HasPrefix(name, "ssh alone")); fetch != test.fetch || metricsOnly != test.metricsOnly {
			t.Errorf("%s: due = %v %v, want %v %v", name, fetch, metricsOnly, test.fetch, test.metricsOnly)
		}
	}
}

// TestRemoteScheduleBacksOffByDoublingUpToFiveMinutes is the delay rule: one
// interval after a success, doubling after each failure up to five minutes and
// never under the interval, with a metrics-only failure leaving the fleet
// export's time alone.
func TestRemoteScheduleBacksOffByDoublingUpToFiveMinutes(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		interval time.Duration
		failures int
		limit    time.Duration
		want     time.Duration
	}{
		"no failure":                {time.Minute, 0, maxRemoteBackoff, time.Minute},
		"one":                       {time.Minute, 1, maxRemoteBackoff, 2 * time.Minute},
		"two":                       {time.Minute, 2, maxRemoteBackoff, 4 * time.Minute},
		"three is capped":           {time.Minute, 3, maxRemoteBackoff, 5 * time.Minute},
		"many stay capped":          {time.Minute, 60, maxRemoteBackoff, 5 * time.Minute},
		"a short interval":          {10 * time.Second, 4, maxRemoteBackoff, 160 * time.Second},
		"a short one is capped":     {10 * time.Second, 5, maxRemoteBackoff, 5 * time.Minute},
		"a long interval":           {10 * time.Minute, 4, maxRemoteBackoff, 10 * time.Minute},
		"a refused login, five":     {time.Minute, 5, maxAuthBackoff, 32 * time.Minute},
		"a refused login is capped": {time.Minute, 6, maxAuthBackoff, time.Hour},
		"a refused login stays":     {time.Minute, 600, maxAuthBackoff, time.Hour},
		"an interval over that cap": {2 * time.Hour, 3, maxAuthBackoff, 2 * time.Hour},
	} {
		if got := remoteBackoff(test.interval, test.failures, test.limit); got != test.want {
			t.Errorf("%s: backoff = %v, want %v", name, got, test.want)
		}
	}
	if keepalive(time.Minute) != 15*time.Minute || keepalive(30*time.Minute) != 30*time.Minute {
		t.Error("the keepalive is 15 minutes, or the interval when that is longer")
	}
	start := newClock().Now()
	fleet, only := exported{ok: true}, exported{ok: true, metricsOnly: true}
	schedule := remoteSchedule{asked: start}.after(start, fleet, time.Minute)
	if schedule.failures != 0 || !schedule.nextFleet.Equal(start.Add(time.Minute)) || !schedule.idleFleet.Equal(start.Add(15*time.Minute)) || !schedule.nextMetrics.Equal(start.Add(30*time.Second)) || !schedule.earliest.Equal(start.Add(30*time.Second)) {
		t.Errorf("after a fleet success = %+v", schedule)
	}
	onlyDone := schedule.after(start.Add(30*time.Second), only, time.Minute)
	if !onlyDone.nextFleet.Equal(schedule.nextFleet) || !onlyDone.idleFleet.Equal(schedule.idleFleet) || !onlyDone.nextMetrics.Equal(start.Add(60*time.Second)) || !onlyDone.earliest.Equal(start.Add(60*time.Second)) {
		t.Errorf("after a metrics-only success = %+v", onlyDone)
	}
	// A metrics-only export that fails still holds the next login of either kind
	// back, and leaves the fleet export's time alone.
	failedOnly := schedule.after(start.Add(30*time.Second), exported{metricsOnly: true}, time.Minute)
	if failedOnly.failures != 1 || !failedOnly.nextFleet.Equal(schedule.nextFleet) || !failedOnly.idleFleet.Equal(schedule.idleFleet) || !failedOnly.earliest.Equal(start.Add(60*time.Second)) {
		t.Errorf("after a metrics-only failure = %+v", failedOnly)
	}
	if fetch, _ := failedOnly.due(start.Add(59*time.Second), true); fetch {
		t.Error("an export is due within 30 seconds of a failed metrics-only one")
	}
	failed := schedule.after(start.Add(time.Minute), exported{}, time.Minute).after(start.Add(3*time.Minute), exported{}, time.Minute)
	if failed.failures != 2 || !failed.nextFleet.Equal(start.Add(7*time.Minute)) || !failed.idleFleet.Equal(start.Add(18*time.Minute)) || failed.authFailures != 0 || !failed.authUntil.IsZero() {
		t.Errorf("after two fleet failures = %+v", failed)
	}
	if cleared := failed.after(start.Add(7*time.Minute), fleet, time.Minute); cleared.failures != 0 || !cleared.nextFleet.Equal(start.Add(8*time.Minute)) {
		t.Errorf("a success after failures = %+v", cleared)
	}
	// A refused SSH login bars SSH for a delay that doubles to an hour, whichever
	// kind of export met it, while the fleet export keeps its five-minute cap for
	// the machine's other transport.
	refused := remoteSchedule{failures: 6, authFailures: 6}.after(start, exported{refused: true}, time.Minute)
	if !refused.authUntil.Equal(start.Add(time.Hour)) || !refused.nextFleet.Equal(start.Add(5*time.Minute)) || refused.authFailures != 7 || !refused.sshBarred(start.Add(59*time.Minute)) || refused.sshBarred(start.Add(time.Hour)) {
		t.Errorf("after a seventh refused login = %+v", refused)
	}
	refusedOnly := schedule.after(start.Add(time.Minute), exported{metricsOnly: true, refused: true}, time.Minute)
	if !refusedOnly.authUntil.Equal(start.Add(3*time.Minute)) || !refusedOnly.nextFleet.Equal(schedule.nextFleet) {
		t.Errorf("after a metrics-only export met a refused login = %+v", refusedOnly)
	}
	if fetch, _ := refusedOnly.due(start.Add(2*time.Minute), true); fetch {
		t.Error("a fleet export over ssh is due while ssh is barred by a refused metrics-only login")
	}
	// An answer over HTTP leaves the bar; an answer over SSH lifts it.
	if overHTTP := refused.after(start.Add(time.Minute), fleet, time.Minute); !overHTTP.authUntil.Equal(refused.authUntil) || overHTTP.authFailures != 7 {
		t.Errorf("an http answer lifted ssh's bar: %+v", overHTTP)
	}
	if overSSH := refused.after(start.Add(time.Hour), exported{ok: true, ssh: true}, time.Minute); !overSSH.authUntil.IsZero() || overSSH.authFailures != 0 {
		t.Errorf("an ssh answer left its bar: %+v", overSSH)
	}
}

// TestLiveEntriesReplaceCachedOnesAndKeepTheLocalMachineUntouched proves
// cockpit-views#req:remote-entries-replace-cached with a fake transport: a
// fresh export's entries appear under the configured key with route
// live-remote, the transport and the remote snapshot's time, replace that
// machine's published-store entries (and no other machine's), carry owner
// state, sync facts and pull request state, and leave this machine's entries
// exactly as they were.
func TestLiveEntriesReplaceCachedOnesAndKeepTheLocalMachineUntouched(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources("/repos/widgets")
	sources.remote = append(sources.remote, cachedVM("alex"))
	full := exportOf(t, vmOwnName, vmSources(), 4, false)
	exporter := &fakeExporter{answer: answering(full, full)}
	snapshotter, clock := newLive(t, sources, exporter, nil)
	refreshAndSettle(t, snapshotter)
	before := snapshotter.Document()
	cached, found := machineNamed(before, vmKey)
	if !found || cached.Route != RouteCached || len(entriesOf(before, cached.ID)["worktrees"]) != 1 || cached.RemoteError != "" {
		t.Fatalf("before any export the machine is %+v", cached)
	}
	local := entriesOf(before, localMachineID(testMachine))

	clock.advance(7 * time.Second)
	pollAndSettle(t, snapshotter)
	document := snapshotter.Document()
	requireUniqueIDs(t, document)
	vm, found := machineNamed(document, vmKey)
	if !found || vm.Route != RouteLiveRemote || vm.Transport != TransportHTTP || vm.RemoteError != "" || vm.WBVersion != testVersion {
		t.Fatalf("the live machine = %+v", vm)
	}
	if vm.ID != cached.ID {
		t.Errorf("the machine's id changed from %s to %s when it went live", cached.ID, vm.ID)
	}
	if !vm.ObservedAt.Equal(full.Fleet.SnapshotAt) || vm.ObservedAt.Equal(clock.Now()) {
		t.Errorf("observed_at = %v, want the remote snapshot's %v", vm.ObservedAt, full.Fleet.SnapshotAt)
	}
	if vm.RepositoryCount != 1 || vm.WorktreeCount != 3 {
		t.Errorf("counts = %d repositories and %d worktrees", vm.RepositoryCount, vm.WorktreeCount)
	}
	live := entriesOf(document, vm.ID)
	if len(live["repositories"]) != 1 || len(live["worktrees"]) != 3 || len(live["pull_requests"]) != 1 || len(live["agents"]) != 1 {
		t.Fatalf("live entries = %v", live)
	}
	body, _ := json.Marshal(document)
	for _, gone := range []string{"stale-task", "feature/stale", "pull/9", vmOwnName} {
		if strings.Contains(string(body), gone) {
			t.Errorf("the document still carries %q", gone)
		}
	}
	var repository Repository
	for _, item := range document.Repositories {
		if item.MachineID == vm.ID {
			repository = item
		}
	}
	if repository.Route != RouteLiveRemote || repository.Machine != vmKey || repository.Name != "acme/engine" || repository.Host != "github.com" ||
		repository.WorktreeCount != 3 || repository.RemoteURLWeb != "https://github.com/acme/engine" || repository.DefaultBranch != "main" ||
		repository.LocalBranchCount == nil || *repository.LocalBranchCount != 3 || repository.ActiveAgentCount == nil || *repository.ActiveAgentCount != 1 {
		t.Errorf("the live repository = %+v", repository)
	}
	states := map[string]string{}
	for _, worktree := range document.Worktrees {
		if worktree.MachineID != vm.ID {
			continue
		}
		states[worktree.Task] = worktree.OwnerState
		if worktree.Route != RouteLiveRemote || worktree.Machine != vmKey || worktree.Repository != repository.ID || worktree.Name != worktree.Task || !worktree.ObservedAt.Equal(full.Fleet.SnapshotAt) {
			t.Errorf("a live worktree = %+v", worktree)
		}
		if worktree.Ahead == nil || worktree.Behind == nil || worktree.HasUpstream == nil || !*worktree.HasUpstream {
			t.Errorf("a live worktree has no sync facts: %+v", worktree)
		}
	}
	if states["vm-task-1"] != OwnerActive || states["vm-task-2"] != OwnerOrphaned || states["vm-task-3"] != OwnerUnknown {
		t.Errorf("owner states = %v", states)
	}
	for _, pull := range document.PullRequests {
		if pull.MachineID == vm.ID && (pull.Number != 41 || pull.Repository != repository.ID || pull.Worktree == "" || pull.URL != "https://github.com/acme/engine/pull/41" || pull.Route != RouteLiveRemote) {
			t.Errorf("the live pull request = %+v", pull)
		}
	}
	for _, agent := range document.Agents {
		if agent.MachineID == vm.ID && (agent.Kind != AgentRun || agent.RunID != "agt-vm" || agent.State != "running" || agent.Repository != repository.ID || agent.Route != RouteLiveRemote) {
			t.Errorf("the live agent = %+v", agent)
		}
	}
	// No id is used as the remote sent it.
	sent := map[string]bool{}
	for _, id := range allIDs(*full.Fleet) {
		sent[id] = true
	}
	for _, id := range allIDs(document) {
		if sent[id] {
			t.Errorf("the document uses %s as the remote sent it", id)
		}
	}
	// This machine's entries, and the other cached machine's, are as they were.
	if got := entriesOf(document, localMachineID(testMachine)); fmt.Sprint(got) != fmt.Sprint(local) {
		t.Errorf("the local machine's entries changed: %v, were %v", got, local)
	}
	if desktop, found := machineNamed(document, "desktop"); !found || desktop.Route != RouteCached || len(entriesOf(document, desktop.ID)["worktrees"]) != 1 {
		t.Errorf("another machine's cached entries changed: %+v", desktop)
	}
	// The branch list of a live repository is known and empty, as a cached one's.
	server := newCockpitServer(t, snapshotter)
	branches := server.get("/api/v1/cockpit/branches?repository="+repository.ID, nil)
	if branches.Code != http.StatusOK || !strings.Contains(branches.Body.String(), ReasonCachedRepository) {
		t.Errorf("branches of a live repository = %d %s", branches.Code, branches.Body.String())
	}
	if again := server.get("/api/v1/cockpit/branches?repository="+repository.ID, nil); again.Body.String() != branches.Body.String() {
		t.Errorf("the prepared branch list changed: %s", again.Body.String())
	}
}

// TestALiveExportGoesStaleAfterTwoIntervalsAndTheCachedEntriesReturn proves the
// freshness rule: while the last export was received less than two refresh
// intervals ago its entries stay, with the failure code beside them; after that
// the published-store entries are shown again, with their age and the code; and
// the next success clears the code and brings the live entries back.
func TestALiveExportGoesStaleAfterTwoIntervalsAndTheCachedEntriesReturn(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources("/repos/widgets")
	sources.remote = append(sources.remote, cachedVM("alex"))
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	exporter := &fakeExporter{answer: answering(full, full)}
	var logs []string
	var logged sync.Mutex
	snapshotter, clock := newLive(t, sources, exporter, func(options *Options) {
		options.Logf = func(format string, args ...any) {
			logged.Lock()
			defer logged.Unlock()
			logs = append(logs, fmt.Sprintf(format, args...))
		}
	})
	refreshAndSettle(t, snapshotter)
	pollAndSettle(t, snapshotter)

	exporter.set(failing(&RemoteError{Code: RemoteErrorHTTPAuthFailed, Fallback: true}))
	clock.advance(DefaultInterval)
	pollAndSettle(t, snapshotter)
	vm, _ := machineNamed(snapshotter.Document(), vmKey)
	if vm.Route != RouteLiveRemote || vm.RemoteError != RemoteErrorHTTPAuthFailed || vm.Transport != TransportHTTP || vm.WorktreeCount != 3 {
		t.Fatalf("one interval after the last export the machine = %+v", vm)
	}

	clock.advance(DefaultInterval)
	refreshAndSettle(t, snapshotter)
	vm, _ = machineNamed(snapshotter.Document(), vmKey)
	if vm.Route != RouteCached || vm.RemoteError != RemoteErrorHTTPAuthFailed || vm.Transport != "" || vm.WorktreeCount != 1 {
		t.Fatalf("two intervals after the last export the machine = %+v", vm)
	}
	if !vm.ObservedAt.Equal(newClock().Now().Add(-25 * 24 * time.Hour)) {
		t.Errorf("the cached entry's age = %v, want the snapshot's publish time", vm.ObservedAt)
	}
	body, _ := json.Marshal(snapshotter.Document())
	if !strings.Contains(string(body), "stale-task") || strings.Contains(string(body), "vm-task-1") {
		t.Errorf("the stale machine shows the wrong entries: %s", body)
	}
	if desktop, _ := machineNamed(snapshotter.Document(), "desktop"); desktop.RemoteError != "" {
		t.Errorf("another machine carries the failure: %+v", desktop)
	}
	requireUniqueIDs(t, snapshotter.Document())

	exporter.set(answering(full, full))
	clock.advance(maxRemoteBackoff)
	pollAndSettle(t, snapshotter)
	vm, _ = machineNamed(snapshotter.Document(), vmKey)
	if vm.Route != RouteLiveRemote || vm.RemoteError != "" || vm.WorktreeCount != 3 {
		t.Fatalf("after the next success the machine = %+v", vm)
	}
	logged.Lock()
	recorded := slices.Clone(logs)
	logged.Unlock()
	if logs := recorded; len(logs) != 1 || logs[0] != "cockpit fleet: the export of vm failed (http_auth_failed)" {
		t.Errorf("log = %q, want the failure once, as a code", logs)
	}
}

// TestAFailingMachineIsRetriedWithADelayThatDoublesToFiveMinutes proves the
// backoff of cockpit-views#req:remote-exporter-transports and the visible error
// of #req:remote-error-is-visible for a machine with no published entries: the
// attempts are 2, 4, 5 and 5 minutes apart, a bare machine entry carries the
// code and nothing else, and the remote's own text never reaches the document.
func TestAFailingMachineIsRetriedWithADelayThatDoublesToFiveMinutes(t *testing.T) {
	t.Parallel()
	exporter := &fakeExporter{}
	snapshotter, clock := newLive(t, oneRepoSources("/repos/widgets"), exporter, nil)
	exporter.clock = clock
	exporter.set(failing(fmt.Errorf("dial tcp %sprivate.example: %w", sentinel, errBoom)))
	refreshAndSettle(t, snapshotter)
	if _, found := machineNamed(snapshotter.Document(), vmKey); found {
		t.Fatal("a machine that was never read has an entry")
	}
	for range 17 * 60 / 5 {
		pollAndSettle(t, snapshotter)
		clock.advance(remoteStep)
	}
	if got, want := exporter.callsAt(false), []int{0, 120, 360, 660, 960}; !slices.Equal(got, want) {
		t.Errorf("attempts at %v seconds, want %v", got, want)
	}
	if got := exporter.callsAt(true); len(got) != 0 {
		t.Errorf("a failing machine was asked for metrics at %v", got)
	}
	vm, found := machineNamed(snapshotter.Document(), vmKey)
	if !found || vm.Route != RouteLiveRemote || vm.RemoteError != RemoteErrorHTTPUnavailable || vm.Transport != "" || !vm.ObservedAt.IsZero() || vm.WorktreeCount != 0 || vm.ID != vm.MachineID {
		t.Fatalf("the failing machine = %+v", vm)
	}
	body, _ := json.Marshal(snapshotter.Document())
	if strings.Contains(string(body), sentinel) || strings.Contains(string(body), "dial tcp") {
		t.Errorf("the document carries the transport's error text: %s", body)
	}
	requireUniqueIDs(t, snapshotter.Document())
	// The metrics route knows the machine and has no source for it.
	server := newCockpitServer(t, snapshotter)
	if got := routeOf(t, server, vm.ID); got.Route != RouteNone || got.Reason != ReasonNoSource {
		t.Errorf("metrics of a machine that was never read = %+v", got)
	}
}

// blockingExporter is an exporter that waits until it is released.
type blockingExporter struct {
	entered chan struct{}
	release chan struct{}
	full    Envelope
}

func (b *blockingExporter) Export(ctx context.Context, _ RemoteTarget, _ bool) (Envelope, error) {
	b.entered <- struct{}{}
	select {
	case <-b.release:
		return b.full, nil
	case <-ctx.Done():
		return Envelope{}, ctx.Err()
	}
}

// TestASlowRemoteNeverDelaysTheLocalSnapshotAndIsReadOnceAtATime proves that an
// export that hangs holds up neither a refresh nor a request, that a machine has
// at most one export running, and that an export cut short by the daemon
// stopping records nothing.
func TestASlowRemoteNeverDelaysTheLocalSnapshotAndIsReadOnceAtATime(t *testing.T) {
	t.Parallel()
	exporter := &blockingExporter{entered: make(chan struct{}, 8), release: make(chan struct{}), full: exportOf(t, vmOwnName, vmSources(), 2, false)}
	snapshotter, clock := newLive(t, oneRepoSources("/repos/widgets"), exporter, nil)
	ctx, cancel := context.WithCancel(t.Context())
	snapshotter.pollRemotes(ctx)
	<-exporter.entered
	// The export hangs; the local pass completes and is served.
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if document := snapshotter.Document(); document.WarmingUp || len(document.Worktrees) == 0 {
		t.Fatalf("the local snapshot waited for the remote: %+v", document)
	}
	server := newCockpitServer(t, snapshotter)
	if recorder := server.get(cockpit.APIPrefix+FleetRoute, nil); recorder.Code != http.StatusOK {
		t.Fatalf("a request waited for the remote: %d", recorder.Code)
	}
	// A second look while the first export runs starts nothing.
	clock.advance(10 * time.Minute)
	snapshotter.pollRemotes(ctx)
	select {
	case <-exporter.entered:
		t.Fatal("a second export of the same machine started while the first ran")
	default:
	}
	// The daemon stops: the export ends and nothing of it is recorded.
	cancel()
	snapshotter.side.Wait()
	snapshotter.mu.RLock()
	machine := snapshotter.live[vmKey]
	recorded := machine.remoteError != "" || machine.fleet != nil || machine.schedule.failures != 0 || machine.busy
	snapshotter.mu.RUnlock()
	if recorded {
		t.Errorf("an export cut short by the stop was recorded: %+v", machine)
	}
	// And a look after the stop starts nothing either.
	snapshotter.pollRemotes(ctx)
	snapshotter.side.Wait()
	select {
	case <-exporter.entered:
		t.Fatal("an export started after the daemon stopped")
	default:
	}
}

// TestStartRunsTheBackgroundReadsUntilItIsStopped proves the loop Start runs:
// it reads each configured machine at once and again when a tick finds it due,
// and stopping ends it.
func TestStartRunsTheBackgroundReadsUntilItIsStopped(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	exporter := &fakeExporter{answer: answering(full, full)}
	ticks := make(chan time.Time)
	refresh := make(chan time.Time)
	stopped := 0
	var steps []time.Duration
	snapshotter, clock := newLive(t, oneRepoSources("/repos/widgets"), exporter, func(options *Options) {
		// With no published store, only the exports are counted as side work.
		options.Collectors.Remote = nil
		options.Tick = func(time.Duration) (<-chan time.Time, func()) { return refresh, func() {} }
		options.RemoteTick = func(step time.Duration) (<-chan time.Time, func()) {
			steps = append(steps, step)
			return ticks, func() { stopped++ }
		}
	})
	stop := snapshotter.Start(t.Context())
	ticks <- clock.Now() // the first look has run when the loop takes a tick
	snapshotter.side.Wait()
	if exporter.count() != 1 {
		t.Fatalf("exports after the start = %d, want 1", exporter.count())
	}
	clock.advance(DefaultInterval)
	// A client is reading the fleet document: the machine is read every interval.
	snapshotter.fleetAsked.Store(clock.Now().UnixNano())
	ticks <- clock.Now()
	ticks <- clock.Now()
	snapshotter.side.Wait()
	if exporter.count() != 2 {
		t.Fatalf("exports after one interval = %d, want 2", exporter.count())
	}
	// The local loop takes a tick only once its first pass has ended, and with it
	// that pass's closing publication. Without this wait the stop could cancel a
	// first pass that was still running: the exports' own publications are held
	// back at the in-pass rate, and a cancelled pass publishes nothing when it
	// ends, so the document would not show the machine.
	refresh <- clock.Now()
	stop()
	if stopped != 1 || len(steps) != 1 || steps[0] != remoteStep {
		t.Errorf("the loop's ticker: stopped %d times, steps %v", stopped, steps)
	}
	if vm, found := machineNamed(snapshotter.Document(), vmKey); !found || vm.Route != RouteLiveRemote {
		t.Errorf("the started daemon shows %+v", vm)
	}
}

// TestNothingIsReadWithNoTransportOrNoTargetOrForThisMachine proves the
// snapshotter's half of cockpit-views#ac:no-route-configured-makes-no-request
// and the rule that an export is never applied to this machine: with no
// transport, with no target, with a target that is this machine, and with a
// target the transport has no route to, sixty seconds on a fake clock start no
// export that reaches a host, the document has this machine's entries only, and
// the metrics route answers none.
func TestNothingIsReadWithNoTransportOrNoTargetOrForThisMachine(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	requests := 0
	hub := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	t.Cleanup(hub.Close)
	for name, change := range map[string]func(*Options, *fakeExporter){
		"no transport": func(options *Options, _ *fakeExporter) { options.Transports = nil },
		"no target":    func(options *Options, _ *fakeExporter) { options.Remotes = nil },
		"this machine": func(options *Options, _ *fakeExporter) {
			options.Remotes = []RemoteTarget{{Machine: testMachine, HTTP: &HTTPRoute{URL: hub.URL, TokenFile: "/nowhere/token"}}, {Machine: ""}}
		},
		"no route for the transport": func(options *Options, _ *fakeExporter) {
			options.Transports = []RemoteTransport{{Name: TransportHTTP, Exporter: NewHTTPExporter(nil)}}
		},
	} {
		exporter := &fakeExporter{answer: answering(full, full)}
		sources := &fakeSources{remote: []remotestate.Entry{cachedVM("alex")}}
		snapshotter, clock := newLive(t, sources, exporter, func(options *Options) { change(options, exporter) })
		refreshAndSettle(t, snapshotter)
		for range 60 / 5 {
			pollAndSettle(t, snapshotter)
			clock.advance(remoteStep)
		}
		if exporter.count() != 0 || requests != 0 {
			t.Errorf("%s: %d exports and %d requests, want none", name, exporter.count(), requests)
		}
		vm, found := machineNamed(snapshotter.Document(), vmKey)
		if !found || vm.Route != RouteCached || vm.RemoteError != "" {
			t.Errorf("%s: the machine = %+v, want its published entries as they are", name, vm)
		}
		if local, _ := machineNamed(snapshotter.Document(), testMachine); local.Route != RouteLocal || local.Transport != "" || local.RemoteError != "" {
			t.Errorf("%s: this machine's entry = %+v", name, local)
		}
		server := newCockpitServer(t, snapshotter)
		if got := routeOf(t, server, vm.ID); got.Route != RouteNone {
			t.Errorf("%s: metrics = %+v, want none", name, got)
		}
	}
}

// TestMetricsOnlyExportsRunOnlyWhileAClientAsks proves
// cockpit-views#ac:metrics-only-export-is-demand-driven on a fake clock: fleet
// exports run once per refresh interval throughout, metrics-only exports run
// only while a request for that machine's metrics is within the last 60
// seconds, at most 30 seconds after the last export, and none after the window
// closes; and a request never causes an export itself.
func TestMetricsOnlyExportsRunOnlyWhileAClientAsks(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	only := exportOf(t, vmOwnName, vmSources(), 6, true)
	exporter := &fakeExporter{answer: answering(full, only)}
	snapshotter, clock := newLive(t, oneRepoSources("/repos/widgets"), exporter, nil)
	exporter.clock = clock
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	step := func(second int, ask bool) {
		if ask && second%10 == 0 {
			vm, _ := machineNamed(snapshotter.Document(), vmKey)
			before := exporter.count()
			if got := routeOf(t, server, vm.ID); got.Route != RouteLiveRemote || got.FetchedAt == nil || len(got.Samples) != 6 {
				t.Fatalf("at %d s the metrics = %+v", second, got)
			}
			if exporter.count() != before {
				t.Fatalf("a request caused an export at %d s", second)
			}
		}
		pollAndSettle(t, snapshotter)
		clock.advance(remoteStep)
	}
	// Five minutes with no client, two minutes of one asking every 10 seconds,
	// then five minutes of silence.
	for second := 0; second < 300; second += 5 {
		step(second, false)
	}
	if got := exporter.callsAt(true); len(got) != 0 {
		t.Fatalf("metrics-only exports with no client at %v", got)
	}
	for second := 300; second <= 420; second += 5 {
		step(second, true)
	}
	for second := 425; second < 720; second += 5 {
		step(second, false)
	}
	if got, want := exporter.callsAt(false), []int{0, 60, 120, 180, 240, 300, 360, 420, 480, 540, 600, 660}; !slices.Equal(got, want) {
		t.Errorf("fleet exports at %v seconds, want one per interval %v", got, want)
	}
	// The last request is at 420 s: the window closes at 480 s.
	if got, want := exporter.callsAt(true), []int{330, 390, 450}; !slices.Equal(got, want) {
		t.Errorf("metrics-only exports at %v seconds, want %v", got, want)
	}
}

// TestLiveMetricsAreServedWithTheirFetchTimeAndExpire proves the live-remote
// source of cockpit-views#ac:metrics-route-serves-each-source: the history of
// the last export with fetched_at (this daemon's receipt time), under the
// machine's id whether its entries are live or cached; a machine whose export
// says it has no metrics, or whose history is two intervals old, falls through
// to the next source; a failed metrics-only export is recorded; and a
// non-loopback or forwarded request gets no sample
// (cockpit-views#ac:non-loopback-metrics-request-is-refused).
func TestLiveMetricsAreServedWithTheirFetchTimeAndExpire(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources("/repos/widgets")
	sources.remote = append(sources.remote, cachedVM("alex"))
	full := exportOf(t, vmOwnName, vmSources(), 5, false)
	only := exportOf(t, vmOwnName, vmSources(), 3, true)
	exporter := &fakeExporter{answer: answering(full, only)}
	next := &fakeMetrics{answers: map[string]MetricsAnswer{}}
	snapshotter, clock := newLive(t, sources, exporter, func(options *Options) { options.Metrics = []MetricsSource{next} })
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	vm, _ := machineNamed(snapshotter.Document(), vmKey)
	past := newClock().Now().Add(-time.Minute)
	next.answers[vm.ID] = MetricsAnswer{Route: RouteCached, Samples: []machinemetrics.Sample{{Load1: ptr(9.0), SampledAt: past}}, Version: 1}

	// Before any export the next source answers, and the ask is recorded.
	if got := routeOf(t, server, vm.ID); got.Route != RouteCached {
		t.Fatalf("before any export = %+v", got)
	}
	clock.advance(3 * time.Second)
	pollAndSettle(t, snapshotter)
	received := clock.Now()
	live := routeOf(t, server, vm.ID)
	if live.Route != RouteLiveRemote || live.FetchedAt == nil || !live.FetchedAt.Equal(received) || len(live.Samples) != 5 || live.Machine != vm.ID {
		t.Fatalf("after the export = %+v", live)
	}
	for index := 1; index < len(live.Samples); index++ {
		if !live.Samples[index].SampledAt.After(live.Samples[index-1].SampledAt) {
			t.Errorf("the history is not oldest first: %+v", live.Samples)
		}
	}
	// A metrics-only export, 30 seconds on, replaces the history.
	clock.advance(metricsOnlyInterval)
	pollAndSettle(t, snapshotter)
	if got := routeOf(t, server, vm.ID); got.Route != RouteLiveRemote || len(got.Samples) != 3 || !got.FetchedAt.Equal(clock.Now()) {
		t.Fatalf("after the metrics-only export = %+v", got)
	}
	// Neither a foreign host nor a forwarded request gets a sample.
	request := httptest.NewRequest(http.MethodGet, metricsURL+vm.ID, nil)
	request.Host = "vm.example"
	foreign := httptest.NewRecorder()
	server.api.ServeHTTP(foreign, request)
	if foreign.Code != http.StatusMisdirectedRequest || strings.Contains(foreign.Body.String(), "samples") {
		t.Errorf("Host: vm.example = %d %s, want 421 and no sample", foreign.Code, foreign.Body.String())
	}
	if forwarded := server.get(metricsURL+vm.ID, nil, "X-Forwarded-For", "203.0.113.9"); forwarded.Code != http.StatusUnauthorized || strings.Contains(forwarded.Body.String(), "samples") {
		t.Errorf("a forwarded request = %d %s, want 401 and no sample", forwarded.Code, forwarded.Body.String())
	}
	// The fleet export one interval after the first carries the history again.
	clock.advance(metricsOnlyInterval)
	pollAndSettle(t, snapshotter)
	if got := routeOf(t, server, vm.ID); len(got.Samples) != 5 || !got.FetchedAt.Equal(clock.Now()) {
		t.Fatalf("after the second fleet export = %+v", got)
	}
	// A failed metrics-only export is shown on the machine and keeps the history.
	exporter.set(failing(&RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}))
	clock.advance(metricsOnlyInterval)
	calls := exporter.count()
	pollAndSettle(t, snapshotter)
	exporter.mu.Lock()
	lastWasMetricsOnly := exporter.metricsOnly[len(exporter.metricsOnly)-1]
	exporter.mu.Unlock()
	if failed, _ := machineNamed(snapshotter.Document(), vmKey); exporter.count() != calls+1 || !lastWasMetricsOnly || failed.RemoteError != RemoteErrorHTTPUnavailable || failed.Route != RouteLiveRemote {
		t.Fatalf("after a failed metrics-only export: %d calls, metrics-only %v, machine %+v", exporter.count()-calls, lastWasMetricsOnly, failed)
	}
	if got := routeOf(t, server, vm.ID); got.Route != RouteLiveRemote || len(got.Samples) != 5 {
		t.Errorf("a failed metrics-only export lost the history: %+v", got)
	}
	// The history goes stale two intervals after it was received: the next
	// source answers again, under the same id, now a cached machine's.
	clock.advance(2 * DefaultInterval)
	refreshAndSettle(t, snapshotter)
	stale, _ := machineNamed(snapshotter.Document(), vmKey)
	if stale.ID != vm.ID || stale.Route != RouteCached {
		t.Fatalf("the stale machine = %+v", stale)
	}
	if got := routeOf(t, server, vm.ID); got.Route != RouteCached || got.FetchedAt != nil {
		t.Errorf("a stale history is still served: %+v", got)
	}
	// A machine whose export says it has no metrics has no live source.
	none := exportOf(t, vmOwnName, vmSources(), 0, false)
	exporter.set(answering(none, none))
	clock.advance(maxRemoteBackoff)
	pollAndSettle(t, snapshotter)
	if got := routeOf(t, server, vm.ID); got.Route != RouteCached {
		t.Errorf("a machine with no metrics of its own = %+v, want the next source", got)
	}
	if _, known := (liveMetrics{snapshotter: snapshotter}).MachineMetrics(localMachineID(testMachine)); known {
		t.Error("the live source answers for this machine")
	}
}

// TestResponseMachineNameIsIgnoredForPlacement proves
// cockpit-views#ac:response-machine-name-is-ignored-for-placement: an export
// that names another machine, or this one (and so carries exactly the ids this
// machine's own entries have), is placed on the configured key; nothing is
// applied to this machine; an entry of a third machine is dropped by the
// mapping and refuses the whole envelope at the boundary; and a target
// configured under this machine's own name is never read.
func TestResponseMachineNameIsIgnoredForPlacement(t *testing.T) {
	t.Parallel()
	{
		const named = "mac"
		// The export is of the same repository this machine has, under another name.
		full := exportOf(t, named, oneRepoSources("/repos/widgets"), 2, false)
		exporter := &fakeExporter{answer: answering(full, full)}
		snapshotter, _ := newLive(t, oneRepoSources("/repos/widgets"), exporter, nil)
		refreshAndSettle(t, snapshotter)
		before := snapshotter.Document()
		pollAndSettle(t, snapshotter)
		document := snapshotter.Document()
		requireUniqueIDs(t, document)
		vm, found := machineNamed(document, vmKey)
		if !found || vm.Route != RouteLiveRemote || len(entriesOf(document, vm.ID)["worktrees"]) != 2 {
			t.Fatalf("an export naming %q is placed as %+v", named, vm)
		}
		if _, found := machineNamed(document, named); found {
			t.Errorf("a machine entry named %q appeared", named)
		}
		local, _ := machineNamed(document, testMachine)
		was, _ := machineNamed(before, testMachine)
		if local.Route != RouteLocal || local.ID != was.ID || local.WorktreeCount != was.WorktreeCount || local.Transport != "" ||
			fmt.Sprint(entriesOf(document, local.ID)) != fmt.Sprint(entriesOf(before, was.ID)) {
			t.Errorf("an export naming %q changed this machine: %+v, was %+v", named, local, was)
		}
		for _, worktree := range document.Worktrees {
			if (worktree.MachineID == vm.ID) != (worktree.Machine == vmKey) || (worktree.MachineID == vm.ID) != (worktree.Route == RouteLiveRemote) {
				t.Errorf("a worktree is on the wrong machine: %+v", worktree)
			}
		}
	}

	// An export that is this machine's own (it names this machine, or its machine
	// entry has this machine's id: a tunnel or proxy that leads back here) is
	// refused, whatever address it came from: nothing of it is placed anywhere,
	// this machine's entries are untouched, and the configured key says why. A
	// metrics-only export of this machine is refused the same way.
	own := exportOf(t, testMachine, oneRepoSources("/repos/widgets"), 2, false)
	renamed := copyEnvelope(t, own)
	renamed.Machine = "another-name"
	ownMetrics := exportOf(t, testMachine, oneRepoSources("/repos/widgets"), 2, true)
	for name, envelope := range map[string]Envelope{"by its name": own, "by its machine id": renamed} {
		logs := &logRecorder{}
		snapshotter, _ := newLive(t, oneRepoSources("/repos/widgets"), &fakeExporter{answer: answering(envelope, ownMetrics)}, func(options *Options) { options.Logf = logs.logf })
		refreshAndSettle(t, snapshotter)
		before := snapshotter.Document()
		pollAndSettle(t, snapshotter)
		document := snapshotter.Document()
		vm, _ := machineNamed(document, vmKey)
		local, _ := machineNamed(document, testMachine)
		if vm.RemoteError != RemoteErrorSelfExport || vm.WorktreeCount != 0 || len(document.Worktrees) != len(before.Worktrees) || local.WorktreeCount != 2 {
			t.Errorf("this machine's own export, recognised %s: vm %+v, %d worktrees", name, vm, len(document.Worktrees))
		}
		if logs.count("the export of vm failed (self_export)") != 1 {
			t.Errorf("%s: log = %q", name, logs.all())
		}
		machine := snapshotter.live[vmKey]
		if snapshotter.recordExport(t.Context(), machine, exportResult{}, true, newClock().Now(), newClock().Now(), liveView{}, [32]byte{}, 0, "", false); machine.samples != nil {
			t.Errorf("%s: metrics of this machine were kept", name)
		}
	}
	snapshotter, _ := newLive(t, oneRepoSources("/repos/widgets"), &fakeExporter{answer: answering(ownMetrics, ownMetrics)}, nil)
	if !snapshotter.ownExport(ownMetrics) || snapshotter.ownExport(exportOf(t, vmOwnName, vmSources(), 0, true)) {
		t.Error("a metrics-only export is not told apart by its name")
	}

	// A third machine's entries: dropped by the mapping, and the envelope that
	// carries them is refused whole at the boundary.
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	third := copyEnvelope(t, full)
	stranger := entryID(kindMachine, "third")
	third.Fleet.Worktrees = append(third.Fleet.Worktrees, Worktree{
		Entry:      Entry{ID: entryID(kindWorktree, "third", "x"), Machine: "third", MachineID: stranger, Route: RouteLocal},
		Repository: third.Fleet.Repositories[0].ID, Name: "third-task", Task: "third-task", Branch: "third-branch",
	})
	cachedEntry := third.Fleet.Worktrees[0]
	cachedEntry.ID, cachedEntry.Route, cachedEntry.Task = entryID(kindWorktree, "cached", "y"), RouteCached, "cached-task"
	third.Fleet.Worktrees = append(third.Fleet.Worktrees, cachedEntry)
	view := mapLive(vmKey, entryID(kindMachine, "/"+vmKey), third.Fleet, newClock().Now(), 0)
	if len(view.worktrees) != 3 {
		t.Fatalf("the mapping kept %d worktrees, want the machine's own 3", len(view.worktrees))
	}
	for _, worktree := range view.worktrees {
		if worktree.Task == "third-task" || worktree.Task == "cached-task" {
			t.Errorf("the mapping kept %+v", worktree)
		}
	}
	exporter := &fakeExporter{answer: answering(third, third)}
	reader, _ := newLive(t, oneRepoSources("/repos/widgets"), exporter, nil)
	refreshAndSettle(t, reader)
	pollAndSettle(t, reader)
	body, _ := json.Marshal(reader.Document())
	vm, _ := machineNamed(reader.Document(), vmKey)
	if vm.RemoteError != RemoteErrorBadPayload || strings.Contains(string(body), "third") || strings.Contains(string(body), "vm-task") {
		t.Errorf("an envelope with a third machine's entry: %+v %s", vm, body)
	}
}

// ownPublication is a snapshot this machine published itself under login: its
// own name and its projects root.
func ownPublication(login string) remotestate.Entry {
	return remotestate.Entry{Snapshot: remotestate.Snapshot{Login: login, Machine: testMachine, ProjectsRoot: "/projects", PublishedAt: newClock().Now().Add(-time.Hour)}}
}

// TestLiveMachineIDIsThePublishedEntrysWhenThereIsExactlyOne is the id rule of a
// configured machine: the id of its one published entry (so it keeps its id
// when it goes live), else an id derived from this machine's login and the key.
// Only a publication under this machine's own login is the machine. While the
// login is not known none is: a machine of another login with the same name
// stays a machine of its own, is never hidden behind the configured machine's
// live entries and never lends it its id. The login is the one given, or the
// one this machine's own publication in the store carries, unless two logins
// both claim to be this machine.
func TestLiveMachineIDIsThePublishedEntrysWhenThereIsExactlyOne(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	for name, test := range map[string]struct {
		login     string
		published []remotestate.Entry
		wantID    string
		cached    int // the machines still shown from the published store under the name
	}{
		"no publication":                         {"", nil, entryID(kindMachine, "/vm"), 0},
		"no publication, login known":            {"alex", nil, entryID(kindMachine, "alex/vm"), 0},
		"one publication, login unknown":         {"", []remotestate.Entry{cachedVM("alex")}, entryID(kindMachine, "/vm"), 1},
		"one publication, login known":           {"alex", []remotestate.Entry{cachedVM("alex")}, entryID(kindMachine, "alex/vm"), 0},
		"one, published twice":                   {"alex", []remotestate.Entry{cachedVM("alex"), cachedVM("alex")}, entryID(kindMachine, "alex/vm"), 0},
		"two logins, login unknown":              {"", []remotestate.Entry{cachedVM("alex"), cachedVM("someone")}, entryID(kindMachine, "/vm"), 2},
		"two logins, login known":                {"alex", []remotestate.Entry{cachedVM("alex"), cachedVM("someone")}, entryID(kindMachine, "alex/vm"), 1},
		"another login only, ours known":         {"alex", []remotestate.Entry{cachedVM("someone")}, entryID(kindMachine, "alex/vm"), 1},
		"another login only, ours unknown":       {"", []remotestate.Entry{cachedVM("someone")}, entryID(kindMachine, "/vm"), 1},
		"the login learned from our publication": {"", []remotestate.Entry{ownPublication("alex"), cachedVM("alex"), cachedVM("someone")}, entryID(kindMachine, "alex/vm"), 1},
		"two logins claim to be this machine":    {"", []remotestate.Entry{ownPublication("alex"), ownPublication("someone"), cachedVM("alex")}, entryID(kindMachine, "/vm"), 1},
	} {
		exporter := &fakeExporter{answer: answering(full, full)}
		snapshotter, _ := newLive(t, &fakeSources{remote: test.published}, exporter, func(options *Options) {
			options.Login, options.ProjectsRoot = test.login, "/projects"
		})
		refreshAndSettle(t, snapshotter)
		pollAndSettle(t, snapshotter)
		// A machine of another login never has the configured machine's SSH route.
		for _, route := range snapshotter.MachineRoutes() {
			if route.MachineID == entryID(kindMachine, "someone/vm") {
				t.Errorf("%s: another login's machine was given the configured machine's route", name)
			}
		}
		document := snapshotter.Document()
		requireUniqueIDs(t, document)
		live, cached := 0, 0
		for _, machine := range document.Machines {
			switch {
			case machine.Machine != vmKey:
			case machine.Route == RouteLiveRemote:
				live++
				if machine.ID != test.wantID {
					t.Errorf("%s: the live machine's id = %s, want %s", name, machine.ID, test.wantID)
				}
			default:
				cached++
			}
		}
		if live != 1 || cached != test.cached {
			t.Errorf("%s: %d live and %d cached machines named vm, want 1 and %d", name, live, cached, test.cached)
		}
		for _, worktree := range document.Worktrees {
			if worktree.Route == RouteLiveRemote && worktree.MachineID != test.wantID {
				t.Errorf("%s: a live worktree is on machine %s", name, worktree.MachineID)
			}
		}
	}
}

// TestALiveMachineIsMappedAgainWhenItsIDChanges is a publication of the machine
// that arrives after its export: the machine takes the published entry's id,
// and its entries follow.
func TestALiveMachineIsMappedAgainWhenItsIDChanges(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	sources := &fakeSources{}
	snapshotter, _ := newLive(t, sources, &fakeExporter{answer: answering(full, full)}, func(options *Options) { options.Login = "" })
	refreshAndSettle(t, snapshotter)
	pollAndSettle(t, snapshotter)
	first, _ := machineNamed(snapshotter.Document(), vmKey)
	// The login is learned later (this machine's own publication arrives with the
	// machine's), and with it the publication that is the machine.
	sources.change(func(f *fakeSources) {
		own := cachedVM("alex")
		own.Snapshot.Machine, own.Snapshot.ProjectsRoot = testMachine, "/projects"
		f.remote = []remotestate.Entry{cachedVM("alex"), own}
	})
	snapshotter.projectsRoot = "/projects"
	refreshAndSettle(t, snapshotter)
	document := snapshotter.Document()
	second, _ := machineNamed(document, vmKey)
	if first.ID != entryID(kindMachine, "/vm") || second.ID != entryID(kindMachine, "alex/vm") || second.Route != RouteLiveRemote {
		t.Fatalf("the machine's ids = %s then %s (%s)", first.ID, second.ID, second.Route)
	}
	if got := entriesOf(document, second.ID); len(got["worktrees"]) != 3 || len(got["agents"]) != 1 || len(entriesOf(document, first.ID)) != 0 {
		t.Errorf("the entries did not follow the machine: %v", got)
	}
	requireUniqueIDs(t, document)
}

// TestMapLiveKeepsOnlyWhatBelongsAndDropsDanglingReferences is the mapping on
// documents the boundary would refuse, to prove it does not rely on it: a
// document with no machine entry yields a bare machine, a worktree whose
// repository is not in the document is dropped, a reference to a missing entry
// is cleared, an entry with no id or a repeated one is dropped, the agents are
// capped, and a code index is carried field by field.
func TestMapLiveKeepsOnlyWhatBelongsAndDropsDanglingReferences(t *testing.T) {
	t.Parallel()
	observed := newClock().Now()
	machineID := entryID(kindMachine, "/vm")
	empty := emptyDocument(time.Minute)
	bare := mapLive(vmKey, machineID, &empty, observed, 0)
	if bare.machine.ID != machineID || bare.machine.Machine != vmKey || bare.machine.Route != RouteLiveRemote || len(bare.repositories)+len(bare.worktrees)+len(bare.pullRequests)+len(bare.agents) != 0 {
		t.Fatalf("an empty document maps to %+v", bare)
	}
	orphans := emptyDocument(time.Minute)
	orphans.Worktrees = []Worktree{{Entry: Entry{ID: "wt-1", MachineID: "mach-x", Route: RouteLocal}, Task: "orphan"}}
	if view := mapLive(vmKey, machineID, &orphans, observed, 0); len(view.worktrees) != 0 {
		t.Errorf("entries with no machine entry were kept: %+v", view.worktrees)
	}

	own := func(id string) Entry {
		return Entry{ID: id, Machine: sentinel + "name", MachineID: "mach-own", Route: RouteLocal}
	}
	document := emptyDocument(time.Minute)
	document.Machines = []Machine{
		{Entry: Entry{ID: "", Route: RouteLocal}},
		{Entry: own("mach-own"), WBVersion: "v9", OS: "linux", Arch: "arm64", CPUCount: 4, BootTime: observed.Add(-time.Hour), Transport: TransportSSH, RemoteError: RemoteErrorTimeout},
	}
	statistics := &CodeStatistics{Indexed: true, Files: 3, Symbols: 4, Edges: 5, Kinds: []KindCount{{Kind: "func", Count: 2}}}
	document.Repositories = []Repository{
		{Entry: own("repo-1"), Host: "github.com", Name: "acme/engine", RemoteURLWeb: "https://evil.example/x", CodeIndex: []CodeIndex{{Indexer: "codegrapher", State: CodeIndexStale, Behind: 2, ReceiptAt: observed, Statistics: statistics, receiptKey: "secret"}, {Indexer: "other", State: CodeIndexNever}}},
		{Entry: own("repo-1"), Name: "acme/duplicate"},
		{Entry: own(""), Name: "acme/no-id"},
	}
	document.Worktrees = []Worktree{
		{Entry: own("wt-1"), Repository: "repo-1", Task: "kept", Name: "another-name", Branch: "b"},
		{Entry: own("wt-2"), Repository: "repo-missing", Task: "dangling"},
		{Entry: own("wt-1"), Repository: "repo-1", Task: "duplicate"},
	}
	document.PullRequests = []PullRequest{
		{Entry: own("pr-1"), Repository: "repo-1", Worktree: "wt-1", Number: 1, URL: "https://github.com/acme/engine/pull/1"},
		{Entry: own("pr-2"), Repository: "repo-missing", Worktree: "wt-2", Number: 2, URL: "javascript:alert(1)"},
		{Entry: Entry{ID: "pr-3", MachineID: "mach-third", Route: RouteLocal}, Repository: "repo-1", Number: 3},
		{Entry: Entry{ID: "pr-4", MachineID: "mach-own", Route: RouteCached}, Repository: "repo-1", Number: 4},
	}
	document.Agents = []Agent{{Entry: Entry{ID: "ag-third", MachineID: "mach-third", Route: RouteLocal}, Kind: AgentRun, State: "running"}}
	for index := range agentCap + 5 {
		document.Agents = append(document.Agents, Agent{Entry: own(fmt.Sprintf("ag-%d", index)), Kind: AgentRun, State: "running", Repository: "repo-missing"})
	}
	view := mapLive(vmKey, machineID, &document, observed, 3)
	huge := document
	huge.Machines = []Machine{{Entry: own("mach-own"), CPUCount: maxCPUCount + 1}}
	if got := mapLive(vmKey, machineID, &huge, observed, 0).machine.CPUCount; got != 0 {
		t.Errorf("an implausible CPU count is kept: %d", got)
	}
	if view.machine.WBVersion != "v9" || view.machine.OS != "linux" || view.machine.CPUCount != 4 || view.machine.Transport != "" || view.machine.RemoteError != "" || view.machine.RepositoryCount != 1 || view.machine.WorktreeCount != 1 ||
		view.machine.ExportDropped != 3+5 || !view.machine.AgentsTruncated {
		t.Errorf("the machine = %+v", view.machine)
	}
	if len(view.repositories) != 1 || view.repositories[0].Name != "acme/engine" || view.repositories[0].RemoteURLWeb != "https://github.com/acme/engine" || view.repositories[0].WorktreeCount != 1 {
		t.Fatalf("the repositories = %+v", view.repositories)
	}
	index := view.repositories[0].CodeIndex
	if len(index) != 2 || index[0].Statistics == nil || index[0].Statistics == statistics || index[0].Statistics.Files != 3 || len(index[0].Statistics.Kinds) != 1 || index[0].receiptKey != "" || index[0].Behind != 2 || index[1].Statistics != nil {
		t.Errorf("the code index = %+v", index)
	}
	if len(view.worktrees) != 1 || view.worktrees[0].Task != "kept" || view.worktrees[0].Name != "kept" || view.worktrees[0].Repository != view.repositories[0].ID || view.worktrees[0].CodeIndex != nil {
		t.Errorf("the worktrees = %+v", view.worktrees)
	}
	if len(view.pullRequests) != 2 || view.pullRequests[0].Worktree != view.worktrees[0].ID || view.pullRequests[1].Repository != "" || view.pullRequests[1].Worktree != "" || view.pullRequests[1].URL != "" {
		t.Errorf("the pull requests = %+v", view.pullRequests)
	}
	if len(view.agents) != agentCap || view.agents[0].Repository != "" {
		t.Errorf("%d agents, the first on %q", len(view.agents), view.agents[0].Repository)
	}
	body, _ := json.Marshal(view.worktrees)
	if strings.Contains(string(body), sentinel) || view.worktrees[0].Machine != vmKey {
		t.Errorf("an entry carries the name the document gave: %s", body)
	}
}

// TestObservedTimeIsTheRemoteSnapshotsAndNeverInTheFuture is the observed_at of
// a live-remote entry.
func TestObservedTimeIsTheRemoteSnapshotsAndNeverInTheFuture(t *testing.T) {
	t.Parallel()
	received := newClock().Now()
	snapshot, exported := received.Add(-40*time.Second), received.Add(-10*time.Second)
	for name, test := range map[string]struct {
		envelope Envelope
		want     time.Time
	}{
		"the snapshot's time":       {Envelope{ExportedAt: exported, Fleet: &Document{SnapshotAt: snapshot}}, snapshot},
		"no snapshot time":          {Envelope{ExportedAt: exported, Fleet: &Document{}}, exported},
		"no fleet":                  {Envelope{ExportedAt: exported}, exported},
		"a time ahead of the clock": {Envelope{ExportedAt: exported, Fleet: &Document{SnapshotAt: received.Add(3 * time.Second)}}, received},
	} {
		if got := observedTime(test.envelope, received); !got.Equal(test.want) {
			t.Errorf("%s: observed = %v, want %v", name, got, test.want)
		}
	}
}

// TestNewEnvelopeDropsAndCountsAnEntryItsOwnDecoderWouldRefuse proves that one
// odd entry cannot take a machine's export down: a task name over the cap and a
// repository with a time in the future are left out and counted, the rest is
// exported, and the envelope passes its own decoder.
func TestNewEnvelopeDropsAndCountsAnEntryItsOwnDecoderWouldRefuse(t *testing.T) {
	t.Parallel()
	sources := vmSources()
	sources.records["/vm/wt-2"] = WorktreeRecord{Task: strings.Repeat("long-", 80), Branch: "feature/vm-2", CreatedAt: newClock().Now()}
	sources.bindings = append(sources.bindings, worktrees.RegisteredPullRequestBinding{Task: "other", Repository: "acme/missing", PullRequest: 5})
	snapshotter, clock := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	if len(snapshotter.Document().Worktrees) != 3 {
		t.Fatalf("the document has %d worktrees", len(snapshotter.Document().Worktrees))
	}
	envelope := snapshotter.Export(false)
	if envelope.Dropped != 1 || len(envelope.Fleet.Worktrees) != 2 || len(envelope.Fleet.PullRequests) != 2 || len(envelope.Fleet.Agents) != 1 {
		t.Fatalf("dropped %d, kept %d worktrees", envelope.Dropped, len(envelope.Fleet.Worktrees))
	}
	body, _ := json.Marshal(envelope)
	if !strings.Contains(string(body), `"dropped":1`) || strings.Contains(string(body), "long-long-") {
		t.Errorf("the envelope = %s", body)
	}
	if _, err := DecodeEnvelope(strings.NewReader(string(body)), false, clock.Now()); err != nil {
		t.Fatalf("the envelope is refused by its own decoder: %v", err)
	}
	// A repository active in the future is dropped, and with it everything of it:
	// its three worktrees, its pull request and its agent, each counted. The pull
	// request of no repository stays.
	document := snapshotter.Document()
	document.Repositories[0].LastActivityAt = clock.Now().Add(time.Hour)
	future, drops := NewEnvelope(document, MetricsResponse{Route: RouteNone, Reason: ReasonNoSource}, clock.Now(), false)
	if drops != (ExportDrops{Repositories: 1, Worktrees: 3, PullRequests: 1, Agents: 1}) || drops.Total() != 6 {
		t.Errorf("drops by kind = %+v", drops)
	}
	if future.Dropped != 6 || len(future.Fleet.Repositories) != 0 || future.Validate(false, clock.Now()) != nil {
		t.Errorf("a repository in the future: dropped %d, %d repositories, valid %v", future.Dropped, len(future.Fleet.Repositories), future.Validate(false, clock.Now()))
	}
	if view := mapLive(vmKey, "mach-x", future.Fleet, clock.Now(), 0); len(view.worktrees) != 0 || len(view.pullRequests) != 1 || len(view.agents) != 0 {
		t.Errorf("a reader of it keeps %d worktrees and %d pull requests", len(view.worktrees), len(view.pullRequests))
	}
	// An id is the kept entry's: a refused entry that claimed the id first takes
	// nothing of the valid repository that has it after.
	twice := snapshotter.Document()
	refused, valid := twice.Repositories[0], twice.Repositories[0]
	refused.LastActivityAt, valid.LastActivityAt = clock.Now().Add(time.Hour), clock.Now()
	twice.Repositories = []Repository{refused, valid}
	kept, drops := NewEnvelope(twice, MetricsResponse{Route: RouteNone, Reason: ReasonNoSource}, clock.Now(), false)
	// (The one worktree dropped is the fixture's, for its own task name.)
	if drops != (ExportDrops{Repositories: 1, Worktrees: 1}) || len(kept.Fleet.Repositories) != 1 || len(kept.Fleet.Worktrees) != 2 || len(kept.Fleet.Agents) != 1 || len(kept.Fleet.PullRequests) != 2 {
		t.Errorf("a refused entry before the valid one of the same id: drops %+v, %d worktrees", drops, len(kept.Fleet.Worktrees))
	}
	// A clean export counts nothing and says nothing.
	clean, _ := json.Marshal(exportOf(t, vmOwnName, vmSources(), 1, false))
	if strings.Contains(string(clean), "dropped") {
		t.Errorf("a clean export carries dropped: %s", clean)
	}
}

// TestAnExportNeverCarriesAnotherMachinesLiveEntriesOrTheErrorOfAReadOfThem
// proves that what this machine read live from another is never exported as its
// own, and that the two machine fields the exporter adds are refused in an
// export, where they can only be forged.
func TestAnExportNeverCarriesAnotherMachinesLiveEntriesOrTheErrorOfAReadOfThem(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	snapshotter, clock := newLive(t, oneRepoSources("/repos/widgets"), &fakeExporter{answer: answering(full, full)}, nil)
	refreshAndSettle(t, snapshotter)
	pollAndSettle(t, snapshotter)
	exported := snapshotter.Export(false)
	body, _ := json.Marshal(exported)
	if len(exported.Fleet.Machines) != 1 || strings.Contains(string(body), "vm-task") || strings.Contains(string(body), RouteLiveRemote) || strings.Contains(string(body), `"transport"`) {
		t.Fatalf("the export carries a live machine: %s", body)
	}
	if err := exported.Validate(false, clock.Now()); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Machine){
		"transport":    func(machine *Machine) { machine.Transport = TransportHTTP },
		"remote_error": func(machine *Machine) { machine.RemoteError = RemoteErrorBadPayload },
	} {
		forged := copyEnvelope(t, exported)
		change(&forged.Fleet.Machines[0])
		if err := forged.Validate(false, clock.Now()); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("an export with a machine's %s = %v, want it refused", name, err)
		}
	}
}

// TestTheLoginIsLearnedFromItsSourceAndThenKept: a daemon that does not know
// its login at the start takes no published entry for a configured machine's;
// once the source says the login (the periodic publisher resolved it), the next
// read of the other machines finds that login's publication, which the machine
// then replaces and takes its id from, and a later answer of the source changes
// nothing.
func TestTheLoginIsLearnedFromItsSourceAndThenKept(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	var said atomic.Pointer[string]
	source := func() string {
		if login := said.Load(); login != nil {
			return *login
		}
		return ""
	}
	sources := &fakeSources{remote: []remotestate.Entry{cachedVM("alex"), cachedVM("someone")}}
	snapshotter, clock := newLive(t, sources, &fakeExporter{answer: answering(full, full)}, func(options *Options) {
		options.Login, options.LoginSource = "", source
	})
	refreshAndSettle(t, snapshotter)
	pollAndSettle(t, snapshotter)
	named := func() (live Machine, cached int) {
		for _, machine := range snapshotter.Document().Machines {
			switch {
			case machine.Machine != vmKey:
			case machine.Route == RouteLiveRemote:
				live = machine
			default:
				cached++
			}
		}
		return live, cached
	}
	if live, cached := named(); live.ID != entryID(kindMachine, "/vm") || cached != 2 {
		t.Fatalf("with the login unknown: the live machine %s beside %d published ones, want none of them taken for it", live.ID, cached)
	}
	for _, login := range []string{"alex", "someone"} {
		said.Store(&login)
		clock.advance(time.Second)
		refreshAndSettle(t, snapshotter)
		if live, cached := named(); live.ID != entryID(kindMachine, "alex/vm") || cached != 1 {
			t.Fatalf("after the source said %s: the live machine %s beside %d published ones, want alex's publication replaced", login, live.ID, cached)
		}
	}
}
