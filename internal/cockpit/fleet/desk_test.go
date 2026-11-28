package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit"
)

type fakeDesk struct {
	mu       sync.Mutex
	diag     string
	panics   bool
	exports  []Envelope
	throughs []*Throughput
	onPub    func(int)
}

func (d *fakeDesk) Diagnostic() string { d.mu.Lock(); defer d.mu.Unlock(); return d.diag }

func (d *fakeDesk) Publish(_ context.Context, source DeskSource) {
	if d.panics {
		panic("desk publisher panicked")
	}
	envelope, throughput := source.Export(false), source.LocalThroughput()
	d.mu.Lock()
	d.exports, d.throughs = append(d.exports, envelope), append(d.throughs, throughput)
	count, notify := len(d.exports), d.onPub
	d.mu.Unlock()
	if notify != nil {
		notify(count)
	}
}

func localMachine(t *testing.T, snapshotter *Snapshotter) Machine {
	t.Helper()
	for _, machine := range snapshotter.Document().Machines {
		if machine.Route == RouteLocal {
			return machine
		}
	}
	t.Fatal("no local machine")
	return Machine{}
}

func TestDeskPublisherRunsAfterAPassAndItsErrorShowsOnThisMachineOnly(t *testing.T) {
	t.Parallel()
	publisher := &fakeDesk{diag: "revoked"}
	snapshotter, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), func(o *Options) { o.Desk = publisher })
	refreshAndSettle(t, snapshotter)
	if got := localMachine(t, snapshotter).DeskError; got != "revoked" {
		t.Fatalf("desk_error = %q", got)
	}
	publisher.mu.Lock()
	exports := len(publisher.exports)
	publisher.mu.Unlock()
	if exports != 1 {
		t.Fatalf("the Desk publisher ran %d times after one pass", exports)
	}
	publisher.mu.Lock()
	publisher.diag = "Desk said: your token /Users/x is bad"
	publisher.mu.Unlock()
	refreshAndSettle(t, snapshotter)
	if got := localMachine(t, snapshotter).DeskError; got != "" {
		t.Fatalf("a diagnostic outside the codes is never shown, got %q", got)
	}
	body, _ := json.Marshal(snapshotter.Export(false))
	if strings.Contains(string(body), "desk_error") {
		t.Fatalf("the export carries desk_error: %s", body)
	}
	if rule := stringRules["Machine.desk_error"]; rule("revoked") || !rule("") {
		t.Fatal("the decoder accepts desk_error in an export")
	}
}

func TestDeskPublisherPanicIsContainedAndThroughputIsOffered(t *testing.T) {
	t.Parallel()
	snapshotter, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), func(o *Options) { o.Desk = &fakeDesk{panics: true} })
	refreshAndSettle(t, snapshotter)
	if got := localMachine(t, snapshotter).DeskError; got != "" {
		t.Fatalf("a panicking publisher with no diagnostic shows %q", got)
	}
	snapshotter.mu.Lock()
	snapshotter.throughput = &Throughput{WindowDays: 7}
	snapshotter.mu.Unlock()
	if got := snapshotter.LocalThroughput(); got == nil || got.WindowDays != 7 {
		t.Fatalf("LocalThroughput() = %+v", got)
	}
	quiet, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), nil)
	refreshAndSettle(t, quiet)
	if got := localMachine(t, quiet).DeskError; got != "" {
		t.Fatalf("no Desk publisher shows %q", got)
	}
}

type startingDesk struct {
	fakeDesk
	started chan context.Context
}

func (d *startingDesk) Start(ctx context.Context) { d.started <- ctx }

// TestTheDeskPublisherStartsWithTheDaemonNotTheFirstPass: heartbeats must
// not wait for a full pass (desk-connect#req:heartbeat), so the snapshotter
// starts a Desk publisher that can start, before its first pass completes.
func TestTheDeskPublisherStartsWithTheDaemonNotTheFirstPass(t *testing.T) {
	t.Parallel()
	publisher := &startingDesk{started: make(chan context.Context, 1)}
	snapshotter, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), func(o *Options) { o.Desk = publisher })
	stop := snapshotter.Start(t.Context())
	ctx := <-publisher.started
	stop()
	if ctx.Err() == nil {
		t.Fatal("the publisher's context must end when the snapshotter stops")
	}
}

// TestTheThroughputScopeAnswersTheThroughputAloneAndIsNoDemand serves wb desk
// push the throughput block the other scopes leave out.
func TestTheThroughputScopeAnswersTheThroughputAloneAndIsNoDemand(t *testing.T) {
	t.Parallel()
	snapshotter, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), nil)
	refreshAndSettle(t, snapshotter)
	snapshotter.mu.Lock()
	snapshotter.throughput = &Throughput{WindowDays: 7, PerDay: []ThroughputDay{}, Slowest: []ThroughputTask{}}
	snapshotter.mu.Unlock()
	server := newCockpitServer(t, snapshotter)
	snapshotter.fleetAsked.Store(0)
	answer := server.get(cockpit.APIPrefix+FleetRoute+"?scope="+ScopeThroughput, nil, ExportReaderHeader, "1")
	var got map[string]json.RawMessage
	if err := json.Unmarshal(answer.Body.Bytes(), &got); err != nil || answer.Code != http.StatusOK || len(got) != 1 || !strings.Contains(string(got["throughput"]), `"window_days":7`) {
		t.Fatalf("throughput scope = %d %s", answer.Code, answer.Body.String())
	}
	if snapshotter.fleetAsked.Load() != 0 {
		t.Fatal("the throughput read was recorded as demand for the other machines")
	}
}

type gatedTerminals struct {
	*fakeTerminals
	release <-chan struct{}
}

func (g gatedTerminals) List(ctx context.Context, limit int) ([]TerminalFile, bool, error) {
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	return g.fakeTerminals.List(ctx, limit)
}

// TestThroughputComputedAfterAPassReachesTheDeskPublisherWithoutAnotherPass
// verifies that when the asynchronous throughput scan finishes after the pass's
// own Desk publish, the new throughput block triggers another Desk publish
// without waiting for the next pass.
func TestThroughputComputedAfterAPassReachesTheDeskPublisherWithoutAnotherPass(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	firstPublish := make(chan struct{})
	var firstOnce sync.Once
	publisher := &fakeDesk{onPub: func(int) { firstOnce.Do(func() { close(firstPublish) }) }}
	terminals := newFakeTerminals()
	snapshotter, clock := throughputSnapshotter(gatedTerminals{fakeTerminals: terminals, release: release}, func(o *Options) {
		o.Desk = publisher
	})
	now := clock.Now()
	terminals.put("a", now, landedTerminal("task-a", now.Add(-3*time.Hour), now.Add(-time.Hour)))

	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-firstPublish
	for snapshotter.deskBusy.Load() {
		runtime.Gosched()
	}
	close(release)
	snapshotter.side.Wait()

	publisher.mu.Lock()
	throughs := append([]*Throughput(nil), publisher.throughs...)
	publisher.mu.Unlock()
	if len(throughs) != 2 {
		t.Fatalf("Desk publisher saw %d calls after one pass, want 2 (initial + throughput)", len(throughs))
	}
	if throughs[0] != nil {
		t.Fatalf("first publish saw throughput %+v before scan finished, want nil", throughs[0])
	}
	if throughs[1] == nil || len(throughs[1].PerDay) != 1 || throughs[1].PerDay[0].Landed != 1 {
		t.Fatalf("second publish throughput = %+v, want the computed block", throughs[1])
	}
}
