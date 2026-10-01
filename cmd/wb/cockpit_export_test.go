package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
}

func newFakeCockpitDaemon(t *testing.T) *fakeCockpitDaemon {
	t.Helper()
	fake := &fakeCockpitDaemon{}
	fake.document, fake.metrics = exportDocument(), exportMetrics()
	fake.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fake.mu.Lock()
		fake.requests = append(fake.requests, request.Clone(context.Background()))
		fleetStatus, metricsStatus, body, cutShort := fake.fleetStatus, fake.metricsStatus, fake.fleetBody, fake.cutShort
		fake.mu.Unlock()
		status := fleetStatus
		var payload any = fake.document
		switch request.URL.Path {
		case cockpit.APIPrefix + cockpitfleet.FleetRoute:
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

func exportDocument() cockpitfleet.Document {
	local := func(id string) cockpitfleet.Entry {
		return cockpitfleet.Entry{ID: id, Machine: "laptop", MachineID: "mach-local", Route: cockpitfleet.RouteLocal, ObservedAt: exportNow}
	}
	cached := func(id string) cockpitfleet.Entry {
		return cockpitfleet.Entry{ID: id, Machine: "vm", MachineID: "mach-vm", Route: cockpitfleet.RouteCached, ObservedAt: exportNow.Add(-time.Hour)}
	}
	return cockpitfleet.Document{
		SchemaVersion: cockpitfleet.SchemaVersion, SnapshotAt: exportNow, RefreshIntervalSeconds: 60,
		Machines: []cockpitfleet.Machine{{Entry: cached("mach-vm"), WBVersion: "v0.1.0"}, {Entry: local("mach-local"), WBVersion: "v0.2.0", WorktreeCount: 1}},
		Repositories: []cockpitfleet.Repository{
			{Entry: local("repo-1"), Name: "acme/widgets", WorktreeCount: 1}, {Entry: cached("repo-2"), Name: "acme/other"},
		},
		Worktrees: []cockpitfleet.Worktree{
			{Entry: local("wt-1"), Repository: "repo-1", Name: "task-a", Task: "task-a", Branch: "feature/a"},
			{Entry: cached("wt-2"), Repository: "repo-2", Name: "task-b", Task: "task-b", Branch: "feature/b"},
		},
		PullRequests: []cockpitfleet.PullRequest{{Entry: local("pr-1"), Repository: "repo-1", Worktree: "wt-1", Number: 7, State: "open"}},
		Agents:       []cockpitfleet.Agent{{Entry: local("ag-1"), Kind: cockpitfleet.AgentRun, State: "running"}},
	}
}

func exportMetrics() cockpitfleet.MetricsResponse {
	var samples []machinemetrics.Sample
	for index := range 360 {
		load, total, used := float64(index)/10, uint64(1000), uint64(index)
		samples = append(samples, machinemetrics.Sample{Load1: &load, MemoryTotalBytes: &total, MemoryUsedBytes: &used, SampledAt: exportNow.Add(-time.Duration(360-index) * time.Second)})
	}
	return cockpitfleet.MetricsResponse{Machine: "mach-local", Route: cockpitfleet.RouteLocal, Samples: samples}
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
		loadRecord: func(string) (daemon.State, bool, error) { return record, found, nil },
		alive:      func(int) bool { return alive },
		client:     cockpitExportClient,
		now:        func() time.Time { return exportNow },
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
		"no record":                  exportDependencies(daemon.State{}, false, false),
		"a stopped record":           exportDependencies(daemon.State{Status: daemon.StatusStopped, PID: 4242, Listen: "127.0.0.1:1"}, true, true),
		"a record with no process":   exportDependencies(daemon.State{Status: daemon.StatusReady, Listen: "127.0.0.1:1"}, true, true),
		"a recorded process gone":    exportDependencies(readyRecord("127.0.0.1:1"), true, false),
		"a listener that is closed":  exportDependencies(readyRecord(closedListen), true, true),
		"a non-loopback record":      exportDependencies(readyRecord("203.0.113.9:8766"), true, true),
		"an unparseable record host": exportDependencies(readyRecord("not-an-address"), true, true),
	} {
		stdout, err := runExport(t, failingStartSeams(t, deps), "--format", "json")
		requireTypedExportFailure(t, stdout, err, "daemon_not_running", name)
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
	for _, other := range []string{"vm", "task-b", "acme/other", "mach-vm"} {
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

	wantPaths := []string{"GET /api/v1/cockpit/fleet", "GET /api/v1/cockpit/machine-metrics?machine=mach-local"}
	if got := fake.paths(); !reflect.DeepEqual(got, append(append([]string(nil), wantPaths...), wantPaths...)) {
		t.Errorf("requests = %v, want the fleet and the machine's own metrics twice", got)
	}
	for _, request := range fake.requests {
		if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" || request.Header.Get("Origin") != "" || request.Header.Get("X-Forwarded-For") != "" {
			t.Errorf("the verb sent a credential or forwarding header: %v", request.Header)
		}
	}
}

func TestCockpitExportOfADaemonWithNoMachineYetHasNoneMetrics(t *testing.T) {
	t.Parallel()
	fake := newFakeCockpitDaemon(t)
	fake.document = cockpitfleet.Document{SchemaVersion: cockpitfleet.SchemaVersion, RefreshIntervalSeconds: 60, WarmingUp: true}
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

// TestCockpitExportFailuresThatAreNotTyped: a daemon that answers badly is an
// error of its own, never one of the two typed reasons, and prints no envelope.
func TestCockpitExportFailuresThatAreNotTyped(t *testing.T) {
	t.Parallel()
	hostile := exportDocument()
	hostile.Worktrees[0].Task = strings.Repeat("a", 300)
	cases := map[string]func(*fakeCockpitDaemon){
		"a 500 from the fleet route":    func(f *fakeCockpitDaemon) { f.fleetStatus = http.StatusInternalServerError },
		"a 500 from the metrics route":  func(f *fakeCockpitDaemon) { f.metricsStatus = http.StatusInternalServerError },
		"a fleet body with a new field": func(f *fakeCockpitDaemon) { f.fleetBody = `{"schema_version":2,"mystery":1}` },
		"a fleet body that is not JSON": func(f *fakeCockpitDaemon) { f.fleetBody = `<html>` },
		"a fleet body cut short":        func(f *fakeCockpitDaemon) { f.cutShort = true },
		"a fleet body over its bound":   func(f *fakeCockpitDaemon) { f.fleetBody = strings.Repeat(" ", cockpitDocumentLimit+1) },
		"a document that breaks a rule": func(f *fakeCockpitDaemon) { f.document = hostile },
	}
	for name, set := range cases {
		fake := newFakeCockpitDaemon(t)
		set(fake)
		stdout, err := runExport(t, failingStartSeams(t, exportDependencies(readyRecord(fake.listen()), true, true)))
		var coded *exitError
		if err == nil || errors.As(err, &coded) || stdout != "" {
			t.Errorf("%s: err = %v, stdout = %q, want a plain error and no output", name, err, stdout)
		}
	}
}

func TestCockpitExportReportsAnUnreadableDaemonRecord(t *testing.T) {
	t.Parallel()
	deps := failingStartSeams(t, cockpitExportDependencies{
		loadRecord: func(string) (daemon.State, bool, error) { return daemon.State{}, false, errors.New("unreadable") },
	})
	stdout, err := runExport(t, deps)
	if err == nil || !strings.Contains(err.Error(), "daemon record") || stdout != "" {
		t.Fatalf("err = %v, stdout = %q", err, stdout)
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
	if want := []string{"loadRecord", "alive", "client", "now"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("cockpitExportDependencies fields = %v, want exactly %v", names, want)
	}
}

func TestCockpitExportDefaultsReadTheRecordWithoutStarting(t *testing.T) {
	t.Parallel()
	deps := defaultCockpitExportDependencies()
	if deps.loadRecord == nil || deps.alive == nil || deps.client == nil || deps.now == nil {
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
	var coded *exitError
	if err == nil || errors.As(err, &coded) || stdout != "" {
		t.Fatalf("err = %v, stdout = %q", err, stdout)
	}
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
