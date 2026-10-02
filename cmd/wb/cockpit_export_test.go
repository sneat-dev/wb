package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
	"github.com/sneat-dev/wb/internal/daemon"
)

var exportNow = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// fakeCockpitDaemon is an httptest server that answers the fleet and
// machine-metrics routes the way a daemon does, and records every request so
// a test can prove what the verb asked and what it sent.
type fakeCockpitDaemon struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	// fleetStatus and metricsStatus, when not zero, are answered instead of 200.
	fleetStatus, metricsStatus int
	document                   cockpitfleet.Document
	metrics                    cockpitfleet.MetricsResponse
	// fleetBody, when set, is the fleet body served as is; cutShort promises a
	// longer fleet body than it sends.
	fleetBody string
	cutShort  bool
	// legacy makes it a daemon of before the scoped read: it ignores the scope
	// parameter and answers the whole document. hang, when set, holds every
	// request until it is closed.
	legacy bool
	hang   chan struct{}
	// served is the size of each fleet body it answered.
	served []int
}

// scoped is what a daemon answers a scoped fleet read with: this machine's part
// of document, built by the function the daemon builds it with, and what it
// left out.
func scoped(document cockpitfleet.Document, scope string) (cockpitfleet.Document, cockpitfleet.ExportDrops) {
	envelope, drops := cockpitfleet.NewEnvelope(document, cockpitfleet.MetricsResponse{}, exportNow, false)
	own := *envelope.Fleet
	if scope == cockpitfleet.ScopeMachine {
		own.Repositories, own.Worktrees, own.PullRequests, own.Agents = []cockpitfleet.Repository{}, []cockpitfleet.Worktree{}, []cockpitfleet.PullRequest{}, []cockpitfleet.Agent{}
	}
	return own, drops
}

func newFakeCockpitDaemon(t *testing.T) *fakeCockpitDaemon {
	t.Helper()
	fake := &fakeCockpitDaemon{}
	fake.document, fake.metrics = exportDocument(), exportMetrics()
	fake.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fake.mu.Lock()
		fake.requests = append(fake.requests, request.Clone(context.Background()))
		fleetStatus, metricsStatus, body, cutShort, hang := fake.fleetStatus, fake.metricsStatus, fake.fleetBody, fake.cutShort, fake.hang
		fake.mu.Unlock()
		if hang != nil {
			<-hang
		}
		status := fleetStatus
		var payload any = fake.document
		switch request.URL.Path {
		case cockpit.APIPrefix + cockpitfleet.FleetRoute:
			if scope := request.URL.Query().Get("scope"); !fake.legacy && fleetStatus == 0 && body == "" && !cutShort && (scope == cockpitfleet.ScopeOwn || scope == cockpitfleet.ScopeMachine) {
				own, drops := scoped(fake.document, scope)
				writer.Header().Set(cockpitfleet.ExportDropsHeader, drops.Header())
				payload = own
			}
			if encoded, err := json.Marshal(payload); err == nil {
				fake.mu.Lock()
				fake.served = append(fake.served, len(encoded))
				fake.mu.Unlock()
			}
			// The verb says that its read is not a person looking; a daemon that
			// took it for one would keep reading its own other machines.
			if request.Header.Get(cockpitfleet.ExportReaderHeader) == "" {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			if cutShort {
				writer.Header().Set("Content-Length", "100")
				_, _ = writer.Write([]byte("{"))
				return
			}
			if body != "" {
				_, _ = writer.Write([]byte(body))
				return
			}
		case cockpit.APIPrefix + cockpitfleet.MetricsRoute:
			status, payload = metricsStatus, fake.metrics
		default:
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if status != 0 {
			writer.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(writer).Encode(payload)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeCockpitDaemon) listen() string { return strings.TrimPrefix(f.server.URL, "http://") }

func (f *fakeCockpitDaemon) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var paths []string
	for _, request := range f.requests {
		paths = append(paths, request.Method+" "+request.URL.RequestURI())
	}
	return paths
}

// fixtureID is an entry id of the shape the daemon derives: a prefix and 20 hex digits.
func fixtureID(prefix string, n int) string { return fmt.Sprintf("%s-%020x", prefix, n) }

var (
	exportLocalMachine = fixtureID("mach", 1)
	exportVMMachine    = fixtureID("mach", 2)
)

func exportDocument() cockpitfleet.Document {
	local := func(id string) cockpitfleet.Entry {
		return cockpitfleet.Entry{ID: id, Machine: "laptop", MachineID: exportLocalMachine, Route: cockpitfleet.RouteLocal, ObservedAt: exportNow}
	}
	cached := func(id string) cockpitfleet.Entry {
		return cockpitfleet.Entry{ID: id, Machine: "vm", MachineID: exportVMMachine, Route: cockpitfleet.RouteCached, ObservedAt: exportNow.Add(-time.Hour)}
	}
	repo1, repo2, wt1, wt2, pr1, ag1 := fixtureID("repo", 1), fixtureID("repo", 2), fixtureID("wt", 1), fixtureID("wt", 2), fixtureID("pr", 1), fixtureID("ag", 1)
	return cockpitfleet.Document{
		SchemaVersion: cockpitfleet.SchemaVersion, SnapshotAt: exportNow, RefreshIntervalSeconds: 60,
		Machines: []cockpitfleet.Machine{{Entry: cached(exportVMMachine), WBVersion: "v0.1.0"}, {Entry: local(exportLocalMachine), WBVersion: "v0.2.0", WorktreeCount: 1}},
		Repositories: []cockpitfleet.Repository{
			{Entry: local(repo1), Name: "acme/widgets", WorktreeCount: 1}, {Entry: cached(repo2), Name: "acme/other"},
		},
		Worktrees: []cockpitfleet.Worktree{
			{Entry: local(wt1), Repository: repo1, Name: "task-a", Task: "task-a", Branch: "feature/a"},
			{Entry: cached(wt2), Repository: repo2, Name: "task-b", Task: "task-b", Branch: "feature/b"},
		},
		PullRequests: []cockpitfleet.PullRequest{{Entry: local(pr1), Repository: repo1, Worktree: wt1, Number: 7, State: "open"}},
		Agents:       []cockpitfleet.Agent{{Entry: local(ag1), Kind: cockpitfleet.AgentRun, State: "running"}},
	}
}

func exportMetrics() cockpitfleet.MetricsResponse {
	var samples []machinemetrics.Sample
	for index := range 360 {
		load, total, used := float64(index)/10, uint64(1000), uint64(index)
		samples = append(samples, machinemetrics.Sample{Load1: &load, MemoryTotalBytes: &total, MemoryUsedBytes: &used, SampledAt: exportNow.Add(-time.Duration(360-index) * time.Second)})
	}
	return cockpitfleet.MetricsResponse{Machine: exportLocalMachine, Route: cockpitfleet.RouteLocal, Samples: samples}
}

// failingStartSeams is every seam of `wb cockpit` that could start a daemon,
// mint a login code or open a browser, each failing the test when reached.
func failingStartSeams(t *testing.T, export cockpitExportDependencies) cockpitCommandDependencies {
	t.Helper()
	return cockpitCommandDependencies{
		daemon: daemonDependencies{
			start: func(string, []string, string) (int, error) {
				t.Error("the export verb started a daemon")
				return 0, errors.New("start")
			},
			executable: func() (string, error) {
				t.Error("the export verb resolved the wb executable to start a daemon")
				return "", errors.New("executable")
			},
			localClient: func(string, string) (*http.Client, error) {
				t.Error("the export verb opened the owner channel to mint a login code")
				return nil, errors.New("owner channel")
			},
			stop: func(int, daemon.Supervisor, string) error {
				t.Error("the export verb stopped a daemon")
				return errors.New("stop")
			},
		},
		open: func(string) error {
			t.Error("the export verb opened a browser")
			return errors.New("open")
		},
		isTerminal: func(any) bool { return true },
		local: func(context.Context, daemonDependencies, string, string, bool) (cockpitLocalSession, error) {
			t.Error("the export verb used cockpitLocalFromDaemon, which starts a daemon and mints a code")
			return cockpitLocalSession{}, errors.New("local")
		},
		export: export,
	}
}

func exportDependencies(record daemon.State, found bool, alive bool) cockpitExportDependencies {
	return cockpitExportDependencies{
		loadRecord:   func(string) (daemon.State, bool, error) { return record, found, nil },
		alive:        func(int) bool { return alive },
		processStart: func(int) (time.Time, bool) { return time.Time{}, false },
		client:       cockpitExportClient,
		now:          func() time.Time { return exportNow },
	}
}

func readyRecord(listen string) daemon.State {
	return daemon.State{Status: daemon.StatusReady, PID: 4242, Listen: listen}
}

func runExport(t *testing.T, deps cockpitCommandDependencies, args ...string) (stdout string, err error) {
	t.Helper()
	command := newCockpitCmdWithDependencies(&invocation{projectsRoot: "/root"}, deps)
	command.SetArgs(append([]string{"export"}, args...))
	command.SilenceUsage, command.SilenceErrors = true, true
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&bytes.Buffer{})
	err = command.Execute()
	return out.String(), err
}

func requireTypedExportFailure(t *testing.T, stdout string, err error, code, name string) {
	t.Helper()
	var coded *exitError
	if !errors.As(err, &coded) || coded.code != exitFindings {
		t.Fatalf("%s: err = %v, want the findings exit code %d", name, err, exitFindings)
	}
	if want := `{"schema_version":1,"error":"` + code + `"}` + "\n"; stdout != want {
		t.Fatalf("%s: stdout = %q, want %q", name, stdout, want)
	}
}

// TestCockpitExportWithNoRunningDaemonFailsAndStartsNothing proves
// cockpit-views#ac:export-without-a-daemon-fails-and-starts-nothing, the
// no-daemon half, for every way a daemon can be absent, with every start seam
// of the command failing the test if reached.
func TestCockpitExportWithNoRunningDaemonFailsAndStartsNothing(t *testing.T) {
	t.Parallel()
	closed := httptest.NewServer(http.NotFoundHandler())
	closedListen := strings.TrimPrefix(closed.URL, "http://")
	closed.Close()
	for name, deps := range map[string]cockpitExportDependencies{
		"no record":                               exportDependencies(daemon.State{}, false, false),
		"a stopped record":                        exportDependencies(daemon.State{Status: daemon.StatusStopped, PID: 4242, Listen: "127.0.0.1:1"}, true, true),
		"a record with no process":                exportDependencies(daemon.State{Status: daemon.StatusReady, Listen: "127.0.0.1:1"}, true, true),
		"a recorded process gone":                 exportDependencies(readyRecord("127.0.0.1:1"), true, false),
		"a listener that is closed":               exportDependencies(readyRecord(closedListen), true, true),
		"a stale record of a recycled process id": staleGeneration(),
	} {
		stdout, err := runExport(t, failingStartSeams(t, deps), "--format", "json")
		requireTypedExportFailure(t, stdout, err, "daemon_not_running", name)
	}
}

// staleGeneration is a record whose process id now belongs to a process that
// started at another time.
func staleGeneration() cockpitExportDependencies {
	deps := exportDependencies(daemon.State{Status: daemon.StatusReady, PID: 4242, Listen: "127.0.0.1:1", ProcessStartedAt: exportNow.Add(-time.Hour)}, true, true)
	deps.processStart = func(int) (time.Time, bool) { return exportNow.Add(-time.Minute), true }
	return deps
}

// TestCockpitExportAcceptsAProcessOfTheRecordedGeneration: a matching start, an
// unobservable one and a record with no start time all go on to read the daemon.
func TestCockpitExportAcceptsAProcessOfTheRecordedGeneration(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*daemon.State, *cockpitExportDependencies){
		"a matching start": func(r *daemon.State, d *cockpitExportDependencies) {
			r.ProcessStartedAt = exportNow
			d.processStart = func(int) (time.Time, bool) { return exportNow, true }
		},
		"an unobservable start": func(r *daemon.State, d *cockpitExportDependencies) { r.ProcessStartedAt = exportNow },
		"no recorded start": func(_ *daemon.State, d *cockpitExportDependencies) {
			d.processStart = func(int) (time.Time, bool) { return exportNow, true }
		},
	} {
		fake := newFakeCockpitDaemon(t)
		record := readyRecord(fake.listen())
		deps := exportDependencies(record, true, true)
		change(&record, &deps)
		deps.loadRecord = func(string) (daemon.State, bool, error) { return record, true, nil }
		if _, err := runExport(t, failingStartSeams(t, deps)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestCockpitExportDialsOnlyTheRecordedLoopbackAddress: a record naming an
// address the shared loopback rule refuses is a failed export and no request is made.
func TestCockpitExportDialsOnlyTheRecordedLoopbackAddress(t *testing.T) {
	t.Parallel()
	fake := newFakeCockpitDaemon(t)
	_, port, _ := net.SplitHostPort(fake.listen())
	for _, listen := range []string{"128.0.0.1:" + port, "203.0.113.9:8766", "0.0.0.0:" + port, "not-an-address", "localhost2:" + port} {
		stdout, err := runExport(t, failingStartSeams(t, exportDependencies(readyRecord(listen), true, true)))
		requireTypedExportFailure(t, stdout, err, "export_failed", listen)
	}
	if got := fake.paths(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
	for listen, want := range map[string]string{"127.0.0.1:8766": "127.0.0.1:8766", "[::1]:8766": "[::1]:8766", "localhost:8766": "localhost:8766", "127.0.0.2:8766": "127.0.0.2:8766", "[0:0:0:0:0:0:0:1]:8766": "[0:0:0:0:0:0:0:1]:8766"} {
		base, ok := cockpitLoopbackBase(listen)
		if !ok || base.Host != want || base.Scheme != "http" {
			t.Errorf("base for %s = %v %v", listen, base, ok)
		}
	}
}

// TestCockpitExportRefusedByTheDaemon is the other half: a daemon with
// cockpit.anonymous_metadata false answers 401 to an anonymous read.
func TestCockpitExportRefusedByTheDaemon(t *testing.T) {
	t.Parallel()
	for name, set := range map[string]func(*fakeCockpitDaemon){
		"the fleet route answers 401":   func(f *fakeCockpitDaemon) { f.fleetStatus = http.StatusUnauthorized },
		"the fleet route answers 403":   func(f *fakeCockpitDaemon) { f.fleetStatus = http.StatusForbidden },
		"the metrics route answers 401": func(f *fakeCockpitDaemon) { f.metricsStatus = http.StatusUnauthorized },
	} {
		fake := newFakeCockpitDaemon(t)
		set(fake)
		deps := failingStartSeams(t, exportDependencies(readyRecord(fake.listen()), true, true))
		stdout, err := runExport(t, deps, "--format", "json")
		requireTypedExportFailure(t, stdout, err, "export_refused", name)
	}
}

// TestCockpitExportCarriesOnlyTheMetadataSet proves
// cockpit-views#ac:export-carries-only-the-metadata-set at the verb: one
// envelope with this machine's entries and 360 samples that passes the strict
// decoder, and with --metrics-only no fleet; and that the verb read as an
// anonymous caller: two GETs of the daemon's own routes, no cookie, no
// credential, and nothing else.
func TestCockpitExportCarriesOnlyTheMetadataSet(t *testing.T) {
	t.Parallel()
	fake := newFakeCockpitDaemon(t)
	deps := failingStartSeams(t, exportDependencies(readyRecord(fake.listen()), true, true))

	stdout, err := runExport(t, deps, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(stdout, "\n") != 1 || !strings.HasSuffix(stdout, "}\n") {
		t.Fatalf("stdout is not one JSON line: %q", stdout)
	}
	envelope, err := cockpitfleet.DecodeEnvelope(strings.NewReader(stdout), false, exportNow)
	if err != nil {
		t.Fatalf("the printed envelope is refused by the strict decoder: %v", err)
	}
	if envelope.Machine != "laptop" || !envelope.ExportedAt.Equal(exportNow) || len(envelope.Metrics.Samples) != 360 || envelope.Metrics.Route != "local" {
		t.Errorf("envelope = %+v", envelope)
	}
	fleetPart := envelope.Fleet
	if len(fleetPart.Machines) != 1 || len(fleetPart.Repositories) != 1 || len(fleetPart.Worktrees) != 1 || len(fleetPart.PullRequests) != 1 || len(fleetPart.Agents) != 1 {
		t.Errorf("the envelope holds entries of another machine: %+v", fleetPart)
	}
	for _, other := range []string{"vm", "task-b", "acme/other", exportVMMachine} {
		if strings.Contains(stdout, `"`+other+`"`) {
			t.Errorf("the envelope carries %q of another machine", other)
		}
	}

	stdout, err = runExport(t, deps, "--metrics-only")
	if err != nil {
		t.Fatal(err)
	}
	only, err := cockpitfleet.DecodeEnvelope(strings.NewReader(stdout), true, exportNow)
	if err != nil || only.Fleet != nil || strings.Contains(stdout, `"fleet"`) || len(only.Metrics.Samples) != 360 || only.Machine != "laptop" {
		t.Errorf("metrics-only: err = %v, envelope = %s", err, stdout)
	}

	metricsPath := "GET /api/v1/cockpit/machine-metrics?machine=" + exportLocalMachine
	wantPaths := []string{"GET /api/v1/cockpit/fleet?scope=own", metricsPath, "GET /api/v1/cockpit/fleet?scope=machine", metricsPath}
	if got := fake.paths(); !reflect.DeepEqual(got, wantPaths) {
		t.Errorf("requests = %v, want this machine's own entries and its metrics, then its machine entry and its metrics", got)
	}
	// The metrics-only export reads nothing of the fleet but the machine entry.
	if len(fake.served) != 2 || fake.served[1] >= fake.served[0] {
		t.Errorf("the fleet bodies served = %v bytes, want the metrics-only read smaller than the full one", fake.served)
	}
	for _, request := range fake.requests {
		if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" || request.Header.Get("Origin") != "" || request.Header.Get("X-Forwarded-For") != "" {
			t.Errorf("the verb sent a credential or forwarding header: %v", request.Header)
		}
	}
}

// TestCockpitExportOfAWarmingDaemonIsWarmingUp: a daemon whose first pass has
// not ended holds a partial fleet, which must never replace what a reader has.
// The full export is the typed reason warming_up; the metrics-only export, which
// needs no fleet, is made, and with no machine entry yet it has no metrics.
func TestCockpitExportOfAWarmingDaemonIsWarmingUp(t *testing.T) {
	t.Parallel()
	fake := newFakeCockpitDaemon(t)
	fake.document = exportDocument()
	fake.document.WarmingUp = true
	stdout, err := runExport(t, failingStartSeams(t, exportDependencies(readyRecord(fake.listen()), true, true)))
	requireTypedExportFailure(t, stdout, err, "warming_up", "a warming daemon")
	if strings.Contains(stdout, "task-a") {
		t.Errorf("a partial fleet was printed: %s", stdout)
	}

	bare := newFakeCockpitDaemon(t)
	bare.document = cockpitfleet.Document{SchemaVersion: cockpitfleet.SchemaVersion, RefreshIntervalSeconds: 60, WarmingUp: true}
	stdout, err = runExport(t, failingStartSeams(t, exportDependencies(readyRecord(bare.listen()), true, true)), "--metrics-only")
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := cockpitfleet.DecodeEnvelope(strings.NewReader(stdout), true, exportNow)
	if err != nil || envelope.Metrics.Route != "none" || envelope.Metrics.Reason != "no_source" || envelope.Machine != "" || envelope.Fleet != nil {
		t.Fatalf("err = %v, envelope = %s", err, stdout)
	}
	if got := bare.paths(); len(got) != 1 {
		t.Errorf("requests = %v, want only the fleet read", got)
	}
}

// TestCockpitExportOfADaemonWithNoMachineYetHasNoneMetrics: a document with no
// machine entry (a first pass that found nothing to name) still yields a
// well-formed envelope.
func TestCockpitExportOfADaemonWithNoMachineYetHasNoneMetrics(t *testing.T) {
	t.Parallel()
	fake := newFakeCockpitDaemon(t)
	fake.document = cockpitfleet.Document{SchemaVersion: cockpitfleet.SchemaVersion, RefreshIntervalSeconds: 60}
	stdout, err := runExport(t, failingStartSeams(t, exportDependencies(readyRecord(fake.listen()), true, true)))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := cockpitfleet.DecodeEnvelope(strings.NewReader(stdout), false, exportNow)
	if err != nil || envelope.Metrics.Route != "none" || envelope.Metrics.Reason != "no_source" || envelope.Machine != "" {
		t.Fatalf("err = %v, envelope = %s", err, stdout)
	}
	if got := fake.paths(); len(got) != 1 {
		t.Errorf("requests = %v, want only the fleet read", got)
	}
}

// TestCockpitExportFailuresArePrintedAsExportFailed: a daemon that answers badly,
// or that this binary does not understand, is the third typed reason, with a
// fixed message, never a dependency's error text.
func TestCockpitExportFailuresArePrintedAsExportFailed(t *testing.T) {
	t.Parallel()
	// The machine's own entry breaking a rule fails the export: there is nothing
	// to drop it in favour of. An odd entry of another kind is dropped instead
	// (TestCockpitExportDropsAndCountsAnEntryThatBreaksARule).
	hostile := exportDocument()
	hostile.Machines[1].WBVersion = "not a version"
	// A daemon that could not list its repositories, and holds none, cannot say
	// what the machine has: its empty fleet is not exported in its place.
	unlistable := exportDocument()
	unlistable.Error, unlistable.RepositoriesTotal = cockpitfleet.ErrorRepositoriesUnreadable, 0
	unlistable.Repositories, unlistable.Worktrees, unlistable.PullRequests, unlistable.Agents = nil, nil, nil, nil
	cases := map[string]func(*fakeCockpitDaemon){
		"a daemon that cannot list its repositories": func(f *fakeCockpitDaemon) { f.document = unlistable },
		"a 500 from the fleet route":                 func(f *fakeCockpitDaemon) { f.fleetStatus = http.StatusInternalServerError },
		"a 500 from the metrics route":               func(f *fakeCockpitDaemon) { f.metricsStatus = http.StatusInternalServerError },
		"a fleet body with a new field":              func(f *fakeCockpitDaemon) { f.fleetBody = `{"schema_version":2,"mystery":1}` },
		"a fleet body that is not JSON":              func(f *fakeCockpitDaemon) { f.fleetBody = `<html>` },
		"a fleet body cut short":                     func(f *fakeCockpitDaemon) { f.cutShort = true },
		"a fleet body over its bound":                func(f *fakeCockpitDaemon) { f.fleetBody = strings.Repeat(" ", cockpitDocumentLimit+1) },
		"a document that breaks a rule":              func(f *fakeCockpitDaemon) { f.document = hostile },
	}
	for name, set := range cases {
		fake := newFakeCockpitDaemon(t)
		set(fake)
		stdout, err := runExport(t, failingStartSeams(t, exportDependencies(readyRecord(fake.listen()), true, true)))
		requireTypedExportFailure(t, stdout, err, "export_failed", name)
	}
	// Its metrics are still exported: they do not depend on the fleet.
	fake := newFakeCockpitDaemon(t)
	fake.document = unlistable
	stdout, err := runExport(t, failingStartSeams(t, exportDependencies(readyRecord(fake.listen()), true, true)), "--metrics-only")
	if only, decodeErr := cockpitfleet.DecodeEnvelope(strings.NewReader(stdout), true, exportNow); err != nil || decodeErr != nil || len(only.Metrics.Samples) != 360 {
		t.Fatalf("the metrics-only export of a daemon that cannot list its repositories: %v %v %.200s", err, decodeErr, stdout)
	}
}

// TestCockpitExportDropsAndCountsAnEntryThatBreaksARule: one worktree with a
// name over the cap does not take the machine's export down. It is left out and
// counted, the rest is printed, the verb succeeds, and stderr says how many
// entries of which kind were left out and never which.
func TestCockpitExportDropsAndCountsAnEntryThatBreaksARule(t *testing.T) {
	t.Parallel()
	fake := newFakeCockpitDaemon(t)
	fake.document = exportDocument()
	fake.document.Worktrees[0].Task = strings.Repeat("a", 300)
	command := newCockpitCmdWithDependencies(&invocation{projectsRoot: "/root"}, failingStartSeams(t, exportDependencies(readyRecord(fake.listen()), true, true)))
	command.SetArgs([]string{"export"})
	command.SilenceUsage, command.SilenceErrors = true, true
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	if err := command.Execute(); err != nil {
		t.Fatalf("err = %v, stdout = %s", err, out.String())
	}
	stdout := out.String()
	envelope, err := cockpitfleet.DecodeEnvelope(strings.NewReader(stdout), false, exportNow)
	if err != nil || envelope.Dropped != 1 || len(envelope.Fleet.Worktrees) != 0 || len(envelope.Fleet.Repositories) != 1 || len(envelope.Fleet.PullRequests) != 1 {
		t.Fatalf("err = %v, envelope = %s", err, stdout)
	}
	if envelope.Fleet.PullRequests[0].Worktree != "" {
		t.Errorf("the pull request still names the worktree that was left out: %+v", envelope.Fleet.PullRequests[0])
	}
	if strings.Contains(stdout, "aaaaaaaa") || strings.Contains(errOut.String(), "aaaaaaaa") {
		t.Errorf("the dropped entry was printed: %s %s", stdout, errOut.String())
	}
	if want := "wb cockpit export: left out 1 entries the envelope's rules refuse (repositories 0, worktrees 1, pull requests 0, agents 0)\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
	// A clean export says nothing on stderr.
	clean := newFakeCockpitDaemon(t)
	command = newCockpitCmdWithDependencies(&invocation{projectsRoot: "/root"}, failingStartSeams(t, exportDependencies(readyRecord(clean.listen()), true, true)))
	command.SetArgs([]string{"export"})
	errOut.Reset()
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&errOut)
	if err := command.Execute(); err != nil || errOut.Len() != 0 {
		t.Errorf("a clean export: %v, stderr %q", err, errOut.String())
	}
}

func TestCockpitExportReportsAnUnreadableDaemonRecordWithoutItsPath(t *testing.T) {
	t.Parallel()
	deps := failingStartSeams(t, cockpitExportDependencies{
		loadRecord: func(string) (daemon.State, bool, error) {
			return daemon.State{}, false, &os.PathError{Op: "open", Path: "/home/secret/.wb/daemon.json", Err: errors.New("denied")}
		},
	})
	stdout, err := runExport(t, deps)
	requireTypedExportFailure(t, stdout, err, "export_failed", "unreadable record")
	if strings.Contains(err.Error(), "secret") || strings.Contains(stdout, "secret") {
		t.Fatalf("the path leaked: %v %s", err, stdout)
	}
}

func TestCockpitExportRejectsAFormatOtherThanJSON(t *testing.T) {
	t.Parallel()
	_, err := runExport(t, failingStartSeams(t, exportDependencies(daemon.State{}, false, false)), "--format", "text")
	var coded *exitError
	if !errors.As(err, &coded) || coded.code != exitUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
}

func TestCockpitExportRefusesPositionalArguments(t *testing.T) {
	t.Parallel()
	if _, err := runExport(t, failingStartSeams(t, exportDependencies(daemon.State{}, false, false)), "extra"); err == nil {
		t.Fatal("a positional argument was accepted")
	}
}

// TestCockpitExportHasNoWayToStartAnything is the structural half of the
// no-start proof: the verb's dependencies are the daemon record, a liveness
// check, a client and a clock, and nothing that starts, stops, replaces or
// signs in to a daemon. A new field fails here and must be justified.
func TestCockpitExportHasNoWayToStartAnything(t *testing.T) {
	t.Parallel()
	var names []string
	for field := range reflect.TypeFor[cockpitExportDependencies]().Fields() {
		names = append(names, field.Name)
	}
	if want := []string{"loadRecord", "alive", "processStart", "client", "now"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("cockpitExportDependencies fields = %v, want exactly %v", names, want)
	}
}

func TestCockpitExportDefaultsReadTheRecordWithoutStarting(t *testing.T) {
	t.Parallel()
	deps := defaultCockpitExportDependencies()
	if deps.loadRecord == nil || deps.alive == nil || deps.processStart == nil || deps.client == nil || deps.now == nil {
		t.Fatalf("defaults = %+v", deps)
	}
	client := deps.client()
	if client.Jar != nil || client.Timeout == 0 || client.CheckRedirect == nil {
		t.Errorf("client = %+v, want no cookie jar, a timeout and no redirects", client)
	}
	if err := client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("a redirect is followed: %v", err)
	}
	if now := deps.now(); time.Since(now) > time.Minute {
		t.Errorf("now() = %v", now)
	}
	if deps.alive(0) {
		t.Error("process 0 reported alive")
	}
}

func TestCockpitExportDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound))
	t.Cleanup(redirector.Close)
	deps := failingStartSeams(t, exportDependencies(readyRecord(strings.TrimPrefix(redirector.URL, "http://")), true, true))
	stdout, err := runExport(t, deps)
	requireTypedExportFailure(t, stdout, err, "export_failed", "a redirect")
	if hits.Load() != 0 {
		t.Fatalf("the redirect was followed %d times", hits.Load())
	}
}

// TestCockpitExportReportsAnUnwritableStdout: a closed stdout is an error, not a
// silent success, for the envelope and for a typed failure.
func TestCockpitExportReportsAnUnwritableStdout(t *testing.T) {
	t.Parallel()
	fake := newFakeCockpitDaemon(t)
	for name, deps := range map[string]cockpitExportDependencies{
		"an envelope":     exportDependencies(readyRecord(fake.listen()), true, true),
		"a typed failure": exportDependencies(daemon.State{}, false, false),
	} {
		command := newCockpitCmdWithDependencies(&invocation{}, failingStartSeams(t, deps))
		command.SetArgs([]string{"export"})
		command.SilenceUsage, command.SilenceErrors = true, true
		command.SetOut(failingWriter{err: errors.New("closed pipe")})
		command.SetErr(&bytes.Buffer{})
		var coded *exitError
		if err := command.Execute(); err == nil || errors.As(err, &coded) {
			t.Errorf("%s: err = %v, want the write error", name, err)
		}
	}
}

// TestCockpitExportDefaultsReadARealRecordFile reads a daemon record the way the
// daemon writes it, from the projects root's runtime directory.
func TestCockpitExportDefaultsReadARealRecordFile(t *testing.T) {
	root := daemonTestRoot(t)
	deps := defaultCockpitExportDependencies()
	if _, found, err := deps.loadRecord(root); err != nil || found {
		t.Fatalf("no record yet: found %v, err %v", found, err)
	}
	store := daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}
	saved := daemon.State{SchemaVersion: daemon.StateSchemaVersion, Status: daemon.StatusReady, PID: 99, Listen: "127.0.0.1:1", UpdatedAt: exportNow}
	saved.Queue.SchemaVersion = daemon.QueueSchemaVersion
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}
	record, found, err := deps.loadRecord(root)
	if err != nil || !found || record.PID != 99 || record.Listen != "127.0.0.1:1" {
		t.Fatalf("record = %+v, found %v, err %v", record, found, err)
	}
}

// TestCockpitExportReachesNoStartPath is the no-start proof that can fail. It
// parses the package's own sources and walks the functions reachable from the
// verb's constructor and its default dependencies; none may reference anything
// that starts, stops or replaces a daemon, mints a login code, opens a browser or
// builds the default daemon dependencies (whose start seam is the real one). The
// one place that runs launchctl for the liveness check, launchdPID, is held
// separately to the read-only `print` argument.
func TestCockpitExportReachesNoStartPath(t *testing.T) {
	t.Parallel()
	forbidden := map[string]bool{
		"cockpitLocalFromDaemon": true, "cockpitOwnerClient": true, "startDaemonProcess": true, "stopDaemonProcess": true,
		"runLaunchctl": true, "openBrowser": true, "defaultDaemonDependencies": true, "daemonLocalHTTPClient": true,
		"peerAdminClient": true, "LoginCodeRPCPath": true, "LoginPath": true, "launchdBootstrap": true,
		"Start": true, "Restart": true, "Stop": true, "Replace": true,
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	bodies := map[string][]*ast.FuncDecl{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			if function, ok := declaration.(*ast.FuncDecl); ok && function.Recv == nil && function.Body != nil {
				bodies[function.Name.Name] = append(bodies[function.Name.Name], function)
			}
		}
	}
	reached := map[string]bool{}
	queue := []string{"newCockpitExportCmd", "defaultCockpitExportDependencies"}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if reached[name] {
			continue
		}
		reached[name] = true
		if name == "launchdPID" {
			continue // read-only, checked below
		}
		for _, function := range bodies[name] {
			ast.Inspect(function.Body, func(node ast.Node) bool {
				identifier, ok := node.(*ast.Ident)
				if !ok {
					return true
				}
				if forbidden[identifier.Name] {
					t.Errorf("%s reaches %s, which can start, stop or sign in to a daemon or open a browser", name, identifier.Name)
				}
				if _, declared := bodies[identifier.Name]; declared {
					queue = append(queue, identifier.Name)
				}
				return true
			})
		}
	}
	for _, must := range []string{"cockpitExportEnvelope", "cockpitExportGet", "daemonProcessAlive", "launchdPID", "requireOutputFormat"} {
		if !reached[must] {
			t.Errorf("the walk never reached %s: it is not walking the real call graph", must)
		}
	}
	for _, function := range bodies["launchdPID"] {
		calls := 0
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if callee, ok := call.Fun.(*ast.Ident); ok && callee.Name == "runLaunchctl" {
				calls++
				if literal, ok := call.Args[0].(*ast.BasicLit); !ok || literal.Value != `"print"` {
					t.Errorf("launchdPID runs launchctl with something other than the read-only print")
				}
			}
			return true
		})
		if calls != 1 {
			t.Errorf("launchdPID calls runLaunchctl %d times, want exactly one", calls)
		}
	}
}

// TestCockpitExportThroughTheRootCommand runs the real command tree against a
// projects root with no daemon (so it needs neither launchd nor a socket): the
// verb is wired, prints the typed failure, exits with the findings code and
// writes only fixed text to stderr.
func TestCockpitExportThroughTheRootCommand(t *testing.T) {
	root := daemonTestRoot(t)
	var stdout, stderr bytes.Buffer
	code := runWithStdin([]string{"--projects-root", root, "cockpit", "export", "--format", "json"}, strings.NewReader(""), &stdout, &stderr)
	if code != exitFindings || stdout.String() != `{"schema_version":1,"error":"daemon_not_running"}`+"\n" {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), root) {
		t.Errorf("stderr names the projects root: %q", stderr.String())
	}
}

func TestSideEffectFreeCommandsRecordNoHeartbeat(t *testing.T) {
	t.Parallel()
	for id, want := range map[string]bool{"version": true, "cockpit export": true, "cockpit": false, "daemon status": false, "": false} {
		if got := sideEffectFreeCommand(id); got != want {
			t.Errorf("sideEffectFreeCommand(%q) = %v, want %v", id, got, want)
		}
	}
}

// crowdedDocument is a daemon's document whose other machines' entries alone are
// over the verb's bound, while this machine's own are a handful.
func crowdedDocument() cockpitfleet.Document {
	document := exportDocument()
	for index := 0; len(document.Worktrees) < 40000; index++ {
		document.Worktrees = append(document.Worktrees, cockpitfleet.Worktree{
			Entry:      cockpitfleet.Entry{ID: fixtureID("wt", 100+index), Machine: "vm", MachineID: exportVMMachine, Route: cockpitfleet.RouteCached, ObservedAt: exportNow.Add(-time.Hour)},
			Repository: fixtureID("repo", 2), Name: strings.Repeat("n", 100), Task: strings.Repeat("t", 100), Branch: "feature/other",
		})
	}
	return document
}

// TestCockpitExportOfAMachineThatShowsManyOthersIsItsOwnEntries: the daemon's
// document is over the verb's bound because of the other machines it shows. The
// verb asks for this machine's own entries only and makes its export; the
// metrics-only export asks for the machine entry alone. Against a daemon of
// before the scoped read, which answers the whole document, the export fails as
// it did (and does not break otherwise: a small document is read as before).
func TestCockpitExportOfAMachineThatShowsManyOthersIsItsOwnEntries(t *testing.T) {
	t.Parallel()
	crowded := crowdedDocument()
	if whole, _ := json.Marshal(crowded); len(whole) <= cockpitDocumentLimit {
		t.Fatalf("the fixture's document is %d bytes, not over the bound of %d (the test would be vacuous)", len(whole), cockpitDocumentLimit)
	}
	fake := newFakeCockpitDaemon(t)
	fake.document = crowded
	deps := failingStartSeams(t, exportDependencies(readyRecord(fake.listen()), true, true))
	stdout, err := runExport(t, deps)
	if err != nil {
		t.Fatalf("the export of a machine that shows many others: %v %s", err, stdout[:min(len(stdout), 200)])
	}
	envelope, err := cockpitfleet.DecodeEnvelope(strings.NewReader(stdout), false, exportNow)
	if err != nil || envelope.Machine != "laptop" || len(envelope.Fleet.Worktrees) != 1 || envelope.Dropped != 0 {
		t.Fatalf("err = %v, envelope = %.300s", err, stdout)
	}
	if stdout, err = runExport(t, deps, "--metrics-only"); err != nil {
		t.Fatalf("the metrics-only export: %v", err)
	}
	if only, err := cockpitfleet.DecodeEnvelope(strings.NewReader(stdout), true, exportNow); err != nil || only.Machine != "laptop" || len(only.Metrics.Samples) != 360 {
		t.Fatalf("metrics-only: err = %v, envelope = %.300s", err, stdout)
	}

	old := newFakeCockpitDaemon(t)
	old.legacy, old.document = true, crowded
	stdout, err = runExport(t, failingStartSeams(t, exportDependencies(readyRecord(old.listen()), true, true)))
	requireTypedExportFailure(t, stdout, err, "export_failed", "a crowded daemon that knows no scope")
	small := newFakeCockpitDaemon(t)
	small.legacy = true
	stdout, err = runExport(t, failingStartSeams(t, exportDependencies(readyRecord(small.listen()), true, true)))
	if envelope, decodeErr := cockpitfleet.DecodeEnvelope(strings.NewReader(stdout), false, exportNow); err != nil || decodeErr != nil || len(envelope.Fleet.Worktrees) != 1 || len(envelope.Fleet.Machines) != 1 {
		t.Fatalf("a daemon that knows no scope: %v %v %.300s", err, decodeErr, stdout)
	}
}

// TestCockpitExportCountsWhatTheDaemonLeftOutOfItsAnswer: a daemon that
// answers the scoped read has already left the entries out that break a rule,
// and says how many in a header; the envelope's dropped field and the line on
// stderr carry that count.
func TestCockpitExportCountsWhatTheDaemonLeftOutOfItsAnswer(t *testing.T) {
	t.Parallel()
	fake := newFakeCockpitDaemon(t)
	fake.document = exportDocument()
	fake.document.Worktrees[0].Task = strings.Repeat("a", 300)
	command := newCockpitCmdWithDependencies(&invocation{projectsRoot: "/root"}, failingStartSeams(t, exportDependencies(readyRecord(fake.listen()), true, true)))
	command.SetArgs([]string{"export"})
	command.SilenceUsage, command.SilenceErrors = true, true
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	if err := command.Execute(); err != nil {
		t.Fatalf("err = %v, stdout = %s", err, out.String())
	}
	envelope, err := cockpitfleet.DecodeEnvelope(strings.NewReader(out.String()), false, exportNow)
	if err != nil || envelope.Dropped != 1 || len(envelope.Fleet.Worktrees) != 0 {
		t.Fatalf("err = %v, envelope = %s", err, out.String())
	}
	if want := "wb cockpit export: left out 1 entries the envelope's rules refuse (repositories 0, worktrees 1, pull requests 0, agents 0)\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
}

// TestCockpitExportOfARecordWithAMalformedPortIsAFailedExport: a daemon record
// whose port is not a number names no address. The verb says export_failed and
// exits 1; it does not panic, which a remote reader would take for a wb too old
// to have the verb.
func TestCockpitExportOfARecordWithAMalformedPortIsAFailedExport(t *testing.T) {
	t.Parallel()
	for _, listen := range []string{"127.0.0.1:http", "127.0.0.1:", "127.0.0.1:0", "127.0.0.1:99999", "localhost:80 80", "[::1]:-1"} {
		stdout, err := runExport(t, failingStartSeams(t, exportDependencies(readyRecord(listen), true, true)))
		requireTypedExportFailure(t, stdout, err, "export_failed", listen)
	}
	// A request that cannot be made is a failed export too, whatever let the
	// address through.
	var document cockpitfleet.Document
	header, failure := cockpitExportGet(t.Context(), cockpitExportClient(), url.URL{Scheme: "http", Host: "127.0.0.1:http", Path: cockpit.APIPrefix}, cockpitfleet.FleetRoute, nil, cockpitDocumentLimit, &document)
	if failure != errExportFailed || header != nil {
		t.Fatalf("a request that cannot be made = %q", failure)
	}
}

// TestCockpitExportOfADaemonThatDoesNotAnswerInTimeIsAFailedExport: only a
// connection that cannot be made says no daemon is serving. A daemon that
// accepts the connection and does not answer within the limit, and a read that
// is cancelled, are failed exports.
func TestCockpitExportOfADaemonThatDoesNotAnswerInTimeIsAFailedExport(t *testing.T) {
	t.Parallel()
	fake := newFakeCockpitDaemon(t)
	fake.hang = make(chan struct{})
	t.Cleanup(func() { close(fake.hang) })
	deps := exportDependencies(readyRecord(fake.listen()), true, true)
	deps.client = func() *http.Client {
		client := cockpitExportClient()
		client.Timeout = 50 * time.Millisecond
		return client
	}
	stdout, err := runExport(t, failingStartSeams(t, deps))
	requireTypedExportFailure(t, stdout, err, "export_failed", "a daemon that does not answer in time")

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for name, test := range map[string]struct {
		ctx  context.Context
		err  error
		want exportFailure
	}{
		"a refused connection":        {t.Context(), errors.New("dial tcp 127.0.0.1:1: connect: connection refused"), errDaemonNotRunning},
		"a deadline":                  {t.Context(), context.DeadlineExceeded, errExportFailed},
		"a cancelled request":         {t.Context(), context.Canceled, errExportFailed},
		"a cancelled command":         {cancelled, errors.New("anything"), errExportFailed},
		"a timeout of the client":     {t.Context(), &url.Error{Op: "Get", URL: "http://127.0.0.1", Err: timeoutError{}}, errExportFailed},
		"an error that is no timeout": {t.Context(), &url.Error{Op: "Get", URL: "http://127.0.0.1", Err: errors.New("EOF")}, errDaemonNotRunning},
	} {
		if got := exportTransportFailure(test.ctx, test.err); got != test.want {
			t.Errorf("%s: %q, want %q", name, got, test.want)
		}
	}
}

// timeoutError is an error that says it is a timeout, as a net.Error does.
type timeoutError struct{}

func (timeoutError) Error() string { return "i/o timeout" }
func (timeoutError) Timeout() bool { return true }
