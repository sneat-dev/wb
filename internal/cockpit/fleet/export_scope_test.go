package fleet

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
	"github.com/sneat-dev/wb/internal/remotestate"
)

// bodyOf is the identity body a payload serves.
func bodyOf(t *testing.T, payload cockpit.Payload) []byte {
	t.Helper()
	recorder := httptest.NewRecorder()
	cockpit.ServePayload(recorder, httptest.NewRequest(http.MethodGet, "/", nil), payload)
	return recorder.Body.Bytes()
}

// TestTheScopedFleetReadIsThisMachinesPartAlone proves the read the export verb
// makes of a daemon that also shows other machines
// (cockpit-views#req:cockpit-export-verb): scope=own answers this machine's own
// entries only, exactly the fleet of its export, however large the rest of the
// document is; scope=machine answers its machine entry alone; both say what
// was left out, in numbers; both are prepared once for a publication; and
// neither is demand for the other machines, even from an owner.
func TestTheScopedFleetReadIsThisMachinesPartAlone(t *testing.T) {
	t.Parallel()
	sources := vmSources()
	sources.records["/vm/wt-2"] = WorktreeRecord{Task: strings.Repeat("long-name-", 40), Branch: "feature/vm-2", CreatedAt: newClock().Now()}
	// Other machines, which make the whole document far larger than this
	// machine's part of it.
	for _, name := range []string{"one", "two", "three"} {
		heavy := remotestate.Snapshot{Login: "a", Machine: name, PublishedAt: newClock().Now().Add(-time.Hour)}
		for index := range 300 {
			heavy.Worktrees = append(heavy.Worktrees, remotestate.WorktreeState{Task: strings.Repeat("t", 60) + name + string(rune('a'+index%26)) + strings.Repeat("x", index), Repository: "o/r", Branch: "b"})
		}
		sources.remote = append(sources.remote, remotestate.Entry{Snapshot: heavy})
	}
	var compressed atomic.Int64
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	snapshotter, _ := newLive(t, sources, &fakeExporter{answer: answering(full, full)}, func(options *Options) {
		options.Sampler = filledSampler(t, &countingSource{}, 3)
		options.Compress = func(body []byte) []byte { compressed.Add(1); return body }
	})
	refreshAndSettle(t, snapshotter)
	pollAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	whole := server.get(cockpit.APIPrefix+FleetRoute, nil, ExportReaderHeader, "1")
	snapshotter.fleetAsked.Store(0)
	snapshotter.fleetOwnerAsked.Store(0)
	for len(snapshotter.kick) > 0 {
		<-snapshotter.kick
	}

	before := compressed.Load()
	var own, machine *httptest.ResponseRecorder
	for range 3 {
		own = server.get(cockpit.APIPrefix+FleetRoute+"?scope="+ScopeOwn, server.owner())
		machine = server.get(cockpit.APIPrefix+FleetRoute+"?scope="+ScopeMachine, nil)
	}
	if got := compressed.Load() - before; got != 2 {
		t.Errorf("six scoped reads compressed %d bodies, want one for each scope", got)
	}
	if snapshotter.fleetAsked.Load() != 0 || snapshotter.fleetOwnerAsked.Load() != 0 || len(snapshotter.kick) != 0 {
		t.Error("a scoped read was recorded as demand for the other machines")
	}
	if own.Code != http.StatusOK || machine.Code != http.StatusOK || own.Body.Len()*10 > whole.Body.Len() {
		t.Fatalf("scoped reads = %d and %d; this machine's part is %d of %d bytes, want a small part", own.Code, machine.Code, own.Body.Len(), whole.Body.Len())
	}
	for name, recorder := range map[string]*httptest.ResponseRecorder{"own": own, "machine": machine} {
		if got := recorder.Header().Get(ExportDropsHeader); got != "0,1,0,0" || ParseExportDrops(got).Total() != 1 {
			t.Errorf("scope %s: %s = %q, want the one worktree left out", name, ExportDropsHeader, got)
		}
		if strings.Contains(recorder.Body.String(), "long-name-") || strings.Contains(recorder.Body.String(), RouteCached) || strings.Contains(recorder.Body.String(), RouteLiveRemote) {
			t.Errorf("scope %s carries an entry that was left out, or another machine's: %s", name, recorder.Body.String())
		}
	}
	var scoped, bare Document
	if err := json.Unmarshal(own.Body.Bytes(), &scoped); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(machine.Body.Bytes(), &bare); err != nil {
		t.Fatal(err)
	}
	exported := snapshotter.Export(false)
	wantFleet, _ := json.Marshal(exported.Fleet)
	if got := bytes.TrimSpace(own.Body.Bytes()); !bytes.Equal(got, wantFleet) {
		t.Errorf("scope own is not the fleet of this machine's export:\n got %s\nwant %s", got, wantFleet)
	}
	if len(scoped.Machines) != 1 || len(scoped.Worktrees) != 2 || scoped.WarmingUp {
		t.Errorf("scope own = %d machines, %d worktrees", len(scoped.Machines), len(scoped.Worktrees))
	}
	if len(bare.Machines) != 1 || bare.Machines[0].Machine != testMachine || bare.Repositories == nil || len(bare.Repositories)+len(bare.Worktrees)+len(bare.PullRequests)+len(bare.Agents) != 0 {
		t.Errorf("scope machine = %+v, want this machine's entry and empty lists", bare)
	}
	// An unknown scope is the whole document, as an older daemon answers.
	if other := server.get(cockpit.APIPrefix+FleetRoute+"?scope=everything", nil, ExportReaderHeader, "1"); other.Body.String() != whole.Body.String() || other.Header().Get(ExportDropsHeader) != "" {
		t.Error("an unknown scope was not answered with the whole document")
	}
}

// TestExportDropsTravelAsFourCounts is the header of a scoped read, both ways,
// and the sum of two passes: anything but four counts within range is no drops.
func TestExportDropsTravelAsFourCounts(t *testing.T) {
	t.Parallel()
	drops := ExportDrops{Repositories: 1, Worktrees: 2, PullRequests: 3, Agents: 4}
	if got := ParseExportDrops(drops.Header()); got != drops || drops.Header() != "1,2,3,4" {
		t.Errorf("%q parsed as %+v", drops.Header(), got)
	}
	if got := drops.Plus(ExportDrops{Repositories: 10, Worktrees: 20, PullRequests: 30, Agents: 40}); got != (ExportDrops{11, 22, 33, 44}) {
		t.Errorf("the sum = %+v", got)
	}
	for _, header := range []string{"", "1,2,3", "1,2,3,4,5", "1,2,x,4", "1,-2,3,4", "1,2,3,99999999999", " 1,2,3,4", "/etc/passwd"} {
		if got := ParseExportDrops(header); got != (ExportDrops{}) {
			t.Errorf("%q parsed as %+v, want no drops", header, got)
		}
	}
}

// TestTheHubExportEncodesOnlyTheHalfThatMoved: the metrics history moves every
// few seconds and the document seldom. A new sample makes a new export whose
// fleet half is the one already validated and encoded for the published
// document, and the envelope that is served is byte for byte the one the
// envelope type encodes to.
func TestTheHubExportEncodesOnlyTheHalfThatMoved(t *testing.T) {
	t.Parallel()
	clock := newClock()
	clock.advance(-time.Hour)
	ticks := make(chan time.Time)
	sampler := machinemetrics.New(machinemetrics.Options{
		Source: advancing{&countingSource{}, clock}, Now: clock.Now,
		Tick: func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
	})
	stop := sampler.Start(t.Context())
	snapshotter, _ := newSnapshotter(vmSources().collectors(), func(options *Options) { options.Sampler = sampler })
	refreshAndSettle(t, snapshotter)
	first, failure := snapshotter.ExportPayload(false)
	if failure != "" {
		t.Fatal(failure)
	}
	want, _ := json.Marshal(snapshotter.Export(false))
	if got := bytes.TrimSpace(bodyOf(t, first)); !bytes.Equal(got, want) {
		t.Fatalf("the served envelope is not the envelope type's encoding:\n got %s\nwant %s", got, want)
	}
	only, _ := snapshotter.ExportPayload(true)
	wantOnly, _ := json.Marshal(snapshotter.Export(true))
	if got := bytes.TrimSpace(bodyOf(t, only)); !bytes.Equal(got, wantOnly) {
		t.Fatalf("the served metrics-only envelope is not the envelope type's encoding:\n got %s\nwant %s", got, wantOnly)
	}
	snapshotter.exports.mu.Lock()
	fleetHalf := snapshotter.exports.fleet
	snapshotter.exports.mu.Unlock()
	version := sampler.Version()
	ticks <- clock.Now()
	stop()
	if sampler.Version() == version {
		t.Fatal("the sampler took no new sample (the test would be vacuous)")
	}
	second, failure := snapshotter.ExportPayload(false)
	if failure != "" || bytes.Equal(bodyOf(t, second), bodyOf(t, first)) {
		t.Fatalf("after a new sample the export = %q, with the same body %v", failure, bytes.Equal(bodyOf(t, second), bodyOf(t, first)))
	}
	envelope, err := DecodeEnvelope(bytes.NewReader(bodyOf(t, second)), false, newClock().Now())
	if err != nil || len(envelope.Metrics.Samples) != 2 || len(envelope.Fleet.Worktrees) != 3 {
		t.Fatalf("the export after a new sample: %v, %d samples", err, len(envelope.Metrics.Samples))
	}
	snapshotter.exports.mu.Lock()
	defer snapshotter.exports.mu.Unlock()
	if snapshotter.exports.fleet != fleetHalf {
		t.Error("a new sample built the fleet half again")
	}
}
