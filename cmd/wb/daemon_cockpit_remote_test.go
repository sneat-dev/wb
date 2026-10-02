package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotessh"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// emptyMachine is the collectors of a machine with no repositories, sessions,
// runs or pull requests: enough for a snapshot, and nothing that runs Git or
// reads a real directory.
type emptyMachine struct{}

func (emptyMachine) Repositories(context.Context) ([]discover.Repo, error) { return nil, nil }
func (emptyMachine) Sessions(context.Context) ([]session.View, error)      { return nil, nil }
func (emptyMachine) Runs(context.Context) ([]agents.Result, error)         { return nil, nil }
func (emptyMachine) PullRequests(context.Context) ([]worktrees.RegisteredPullRequestBinding, error) {
	return nil, nil
}

func emptyMachineCollectors() cockpitfleet.Collectors {
	return cockpitfleet.Collectors{Repositories: emptyMachine{}, Sessions: emptyMachine{}, Runs: emptyMachine{}, PullRequests: emptyMachine{}}
}

var exportTestNow = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// refreshedSnapshotter is a fleet snapshotter of a machine called name that has
// taken one snapshot, on a fixed clock.
func refreshedSnapshotter(t *testing.T, name string) *cockpitfleet.Snapshotter {
	t.Helper()
	snapshotter := cockpitfleet.New(cockpitfleet.Options{
		Machine: name, Version: "v1.2.3", Collectors: emptyMachineCollectors(), Now: func() time.Time { return exportTestNow },
	})
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	return snapshotter
}

const sessionMoveHTTPTarget = `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: vm
      http:
        url: %s
        token_file: %s
`

// TestCockpitRemotesComeOnlyFromTheLocalConfiguration proves the configuration
// half of cockpit-views#req:remote-http-fetch, #req:remote-ssh-fetch and of
// cockpit-views#ac:no-route-configured-makes-no-request: a machine is read over
// HTTP only when session_move.targets names it with an http section (its
// credential is that section's token_file or, for the hub this machine is
// enrolled with, remote.token_file) and cockpit.remote_http is on, and over SSH
// only when it has an ssh section and cockpit.remote_ssh is on; HTTP is asked
// before SSH; with no session_move section, a target with neither section, or
// both switches off there is no target and no transport at all. The SSH routes
// for an owner's "Copy command" entries are every ssh section, whatever the
// switches say.
func TestCockpitRemotesComeOnlyFromTheLocalConfiguration(t *testing.T) {
	t.Parallel()
	enabled := wbconfig.DefaultCockpitConfig()
	httpOff := enabled
	httpOff.RemoteHTTP = false
	if !enabled.RemoteHTTP || !enabled.RemoteSSH {
		t.Fatal("cockpit.remote_http and cockpit.remote_ssh are not on by default")
	}
	sshOff, err := wbconfig.LoadCockpit(cockpitConfigFile(t, "cockpit:\n  remote_ssh: false\n")())
	if err != nil || sshOff.RemoteSSH || !sshOff.RemoteHTTP {
		t.Fatalf("cockpit.remote_ssh: false = %+v, %v", sshOff, err)
	}
	bothOff := sshOff
	bothOff.RemoteHTTP = false
	const hubRemote = "remote:\n  provider: hub\n  url: https://Hub.Example:443/\n  token_file: /etc/wb/hub.token\n  machine: laptop\n"
	const onlySSH = "session_move:\n  targets:\n    vm:\n      default_courier: ssh\n      ssh:\n        host: vm.example\n        user: alex\n        wb_path: /usr/local/bin/wb\n"
	withHTTP := fmt.Sprintf(sessionMoveHTTPTarget, "https://vm.example", "/etc/wb/vm.token")
	type routes = map[string]cockpitfleet.SSHRoute
	vmHTTP := map[string]cockpitfleet.HTTPRoute{"vm": {URL: "https://vm.example", TokenFile: "/etc/wb/vm.token"}}
	vmSSH, fullSSH := routes{"vm": {Host: "vm"}}, routes{"vm": {Host: "vm.example", User: "alex", WBPath: "/usr/local/bin/wb"}}
	for name, test := range map[string]struct {
		config     wbconfig.CockpitConfig
		yaml       string
		http       map[string]cockpitfleet.HTTPRoute
		ssh        routes
		copyRoutes routes
		transports []string
		logged     string
		noLogOf    string
	}{
		"no session_move section": {config: enabled, yaml: "cockpit:\n  refresh_interval: 45s\n"},
		"no file at all":          {config: enabled, yaml: ""},
		"only a synchestra section": {config: enabled, yaml: `session_move:
  targets:
    vm:
      default_courier: synchestra
      synchestra:
        runner: vm
`},
		"only an ssh section":                  {config: enabled, yaml: onlySSH, ssh: fullSSH, copyRoutes: fullSSH, transports: []string{cockpitfleet.TransportSSH}},
		"an ssh target and remote_ssh false":   {config: sshOff, yaml: onlySSH, copyRoutes: fullSSH},
		"an http target and remote_http false": {config: httpOff, yaml: withHTTP, ssh: vmSSH, copyRoutes: vmSSH, transports: []string{cockpitfleet.TransportSSH}},
		"both sections and both switches off":  {config: bothOff, yaml: withHTTP, copyRoutes: vmSSH},
		"both sections and remote_ssh false":   {config: sshOff, yaml: withHTTP, http: vmHTTP, copyRoutes: vmSSH, transports: []string{cockpitfleet.TransportHTTP}},
		"both sections": {config: enabled, yaml: withHTTP, http: vmHTTP, ssh: vmSSH, copyRoutes: vmSSH,
			transports: []string{cockpitfleet.TransportHTTP, cockpitfleet.TransportSSH}},
		"the enrolled hub needs no token_file": {config: sshOff, yaml: hubRemote + `session_move:
  targets:
    hub-machine:
      default_courier: ssh
      ssh:
        host: hub
      http:
        url: https://hub.example
    elsewhere:
      default_courier: ssh
      ssh:
        host: elsewhere
      http:
        url: https://elsewhere.example
    alpha:
      default_courier: ssh
      ssh:
        host: alpha
      http:
        url: https://alpha.example
        token_file: /etc/wb/alpha.token
    plain:
      default_courier: ssh
      ssh:
        host: plain
`, http: map[string]cockpitfleet.HTTPRoute{
			"alpha":       {URL: "https://alpha.example", TokenFile: "/etc/wb/alpha.token"},
			"hub-machine": {URL: "https://hub.example", TokenFile: "/etc/wb/hub.token"},
		}, copyRoutes: routes{"hub-machine": {Host: "hub"}, "elsewhere": {Host: "elsewhere"}, "alpha": {Host: "alpha"}, "plain": {Host: "plain"}},
			transports: []string{cockpitfleet.TransportHTTP}, logged: "elsewhere is not read over http"},
		"no token_file and no hub": {config: enabled, yaml: "session_move:\n  targets:\n    vm:\n      default_courier: ssh\n      ssh:\n        host: vm\n      http:\n        url: https://vm.example\n",
			ssh: vmSSH, copyRoutes: vmSSH, transports: []string{cockpitfleet.TransportSSH}, logged: "vm is not read over http"},
		"an address a credential must not be sent to": {config: enabled, yaml: fmt.Sprintf(sessionMoveHTTPTarget, "http://vm.example", "/etc/wb/vm.token"),
			logged: "other machines are not read live", noLogOf: "/etc/wb/vm.token"},
		"an ssh host that could be read as an option": {config: enabled, yaml: "session_move:\n  targets:\n    vm:\n      default_courier: ssh\n      ssh:\n        host: -oProxyCommand=x\n",
			logged: "other machines are not read live"},
	} {
		configPath := filepath.Join(t.TempDir(), "absent.yaml")
		if test.yaml != "" {
			configPath = cockpitConfigFile(t, test.yaml)()
		}
		var logs bytes.Buffer
		targets, transports, copyRoutes := cockpitRemotes(configPath, test.config, func(format string, args ...any) { _, _ = fmt.Fprintf(&logs, format+"\n", args...) }, cockpitSSH{})
		gotHTTP, gotSSH := map[string]cockpitfleet.HTTPRoute{}, routes{}
		for index, target := range targets {
			if target.HTTP != nil {
				gotHTTP[target.Machine] = *target.HTTP
			}
			if target.SSH != nil {
				gotSSH[target.Machine] = *target.SSH
			}
			if target.HTTP == nil && target.SSH == nil {
				t.Errorf("%s: target %s has no route", name, target.Machine)
			}
			if index > 0 && targets[index-1].Machine >= target.Machine {
				t.Errorf("%s: the targets are not in order: %s before %s", name, targets[index-1].Machine, target.Machine)
			}
		}
		if !maps.Equal(gotHTTP, test.http) || !maps.Equal(gotSSH, test.ssh) || !maps.Equal(copyRoutes, test.copyRoutes) {
			t.Errorf("%s: http %+v, ssh %+v, copy routes %+v; want %+v, %+v, %+v", name, gotHTTP, gotSSH, copyRoutes, test.http, test.ssh, test.copyRoutes)
		}
		var names []string
		for _, transport := range transports {
			names = append(names, transport.Name)
			if transport.Exporter == nil {
				t.Errorf("%s: the %s transport has no exporter", name, transport.Name)
			}
		}
		if !slices.Equal(names, test.transports) {
			t.Errorf("%s: the transports = %v, want %v", name, names, test.transports)
		}
		if (test.logged == "") != (logs.Len() == 0) || !strings.Contains(logs.String(), test.logged) || (test.noLogOf != "" && strings.Contains(logs.String(), test.noLogOf)) {
			t.Errorf("%s: log = %q, want %q", name, logs.String(), test.logged)
		}
	}
}

// countingRunner is a remotessh.Runner that starts nothing: it records the
// executable and arguments of every call and fails as an ssh that cannot reach
// its host does.
type countingRunner struct {
	mu    sync.Mutex
	calls [][]string
	ran   chan struct{}
}

type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitStatus) ExitCode() int { return int(e) }

func (r *countingRunner) Run(_ context.Context, executable string, args []string, _ []byte, _, _ io.Writer) error {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string{executable}, args...))
	r.mu.Unlock()
	select {
	case r.ran <- struct{}{}:
	default:
	}
	return exitStatus(255)
}

func (r *countingRunner) all() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// TestADaemonStartsNoSSHProcessUnlessAMachineHasAnSSHRouteAndTheSwitchIsOn runs
// the fleet options the daemon builds over a counting runner
// (cockpit-views#ac:no-route-configured-makes-no-request,
// #ac:ssh-argument-vector-contains-only-configured-values): with no session_move
// section, a target with no ssh section, or cockpit.remote_ssh false, the
// started snapshotter never calls the runner; with the route configured it runs
// the resolved ssh with exactly the arguments of the configuration.
func TestADaemonStartsNoSSHProcessUnlessAMachineHasAnSSHRouteAndTheSwitchIsOn(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const withSSH = "session_move:\n  targets:\n    vm:\n      default_courier: ssh\n      ssh:\n        host: vm.example\n        user: alex\n        wb_path: /usr/local/bin/wb\n"
	sshOff := wbconfig.DefaultCockpitConfig()
	sshOff.RemoteSSH = false
	var hubRequests atomic.Int64
	failingHub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hubRequests.Add(1)
		writer.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(failingHub.Close)
	tokenFile := filepath.Join(t.TempDir(), "vm.token")
	if err := os.WriteFile(tokenFile, []byte("the-vm-credential\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := func(yaml string, config wbconfig.CockpitConfig) (runner *countingRunner, lookups *atomic.Int64, refresh, remote chan time.Time, stop func()) {
		runner, lookups = &countingRunner{ran: make(chan struct{}, 1)}, &atomic.Int64{}
		ssh := cockpitSSH{runner: runner, find: func() (string, error) {
			lookups.Add(1)
			return executable, nil
		}}
		options := cockpitFleetOptionsWith(t.TempDir(), t.TempDir(), cockpitConfigFile(t, yaml)(), config, io.Discard, func() (string, error) { return "laptop", nil }, ssh)
		options.Collectors, options.Sampler = emptyMachineCollectors(), nil
		refresh, remote = make(chan time.Time), make(chan time.Time)
		options.Tick = func(time.Duration) (<-chan time.Time, func()) { return refresh, func() {} }
		options.RemoteTick = func(time.Duration) (<-chan time.Time, func()) { return remote, func() {} }
		return runner, lookups, refresh, remote, cockpitfleet.New(options).Start(t.Context())
	}
	for name, test := range map[string]struct {
		yaml   string
		config wbconfig.CockpitConfig
	}{
		"no session_move section":   {"cockpit:\n  refresh_interval: 60s\n", wbconfig.DefaultCockpitConfig()},
		"only a synchestra section": {"session_move:\n  targets:\n    vm:\n      default_courier: synchestra\n      synchestra:\n        runner: vm\n", wbconfig.DefaultCockpitConfig()},
		"remote_ssh false":          {withSSH, sshOff},
		// The machine has both sections: its hub fails in a way that would fall
		// back to ssh, and with the switch off nothing is run.
		"remote_ssh false and an http section that fails": {fmt.Sprintf(sessionMoveHTTPTarget, failingHub.URL, tokenFile), sshOff},
	} {
		runner, lookups, refresh, remote, stop := start(test.yaml, test.config)
		refresh <- exportTestNow
		if strings.Contains(test.yaml, "http:") {
			// Only a daemon with a machine to read runs the loop that takes this tick.
			remote <- exportTestNow
			for deadline := time.Now().Add(10 * time.Second); hubRequests.Load() == 0 && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
		}
		refresh <- exportTestNow.Add(time.Minute)
		stop()
		if calls := runner.all(); len(calls) != 0 || lookups.Load() != 0 {
			t.Fatalf("%s: the daemon ran %v and looked ssh up %d times", name, calls, lookups.Load())
		}
	}
	if hubRequests.Load() == 0 {
		t.Fatal("the failing hub was never asked: the http-and-no-ssh case proved nothing")
	}
	runner, _, _, remote, stop := start(withSSH, wbconfig.DefaultCockpitConfig())
	remote <- exportTestNow
	select {
	case <-runner.ran:
	case <-time.After(10 * time.Second):
		t.Fatal("the configured ssh route was never read")
	}
	stop()
	want := []string{
		executable, "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "ClearAllForwardings=yes",
		"-o", "ControlMaster=no", "-o", "RemoteCommand=none", "-o", "PermitLocalCommand=no", "-o", "LogLevel=ERROR",
		"-l", "alex", "--", "vm.example", "/usr/local/bin/wb", "cockpit", "export", "--format", "json",
	}
	if calls := runner.all(); len(calls) == 0 || !slices.Equal(calls[0], want) {
		t.Fatalf("the daemon ran %v, want %v", calls, want)
	}
}

// TestTheDaemonsOwnSSHSeamsAreTheTrustedSearchAndTheGroupKillingRunner holds the
// production seams: nothing else decides which ssh runs or how it is ended.
func TestTheDaemonsOwnSSHSeamsAreTheTrustedSearchAndTheGroupKillingRunner(t *testing.T) {
	t.Parallel()
	ssh := daemonCockpitSSH()
	if ssh.find == nil || ssh.runner != (remotessh.GroupRunner{}) {
		t.Fatalf("daemonCockpitSSH = %+v", ssh)
	}
	// The search runs nothing: it only looks at files. What it finds is the
	// system's ssh, or one on the PATH that passes remotessh.ResolveTrusted.
	if found, err := ssh.find(); err == nil && !filepath.IsAbs(found) {
		t.Fatalf("the daemon's ssh = %q", found)
	}
	// With no session_move section the daemon's options hold no machine to read
	// and no transport, so nothing is ever run with those seams.
	options := cockpitFleetOptions(t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "absent.yaml"), wbconfig.DefaultCockpitConfig(), io.Discard, func() (string, error) { return "laptop", nil })
	if len(options.Remotes) != 0 || len(options.Transports) != 0 || len(options.SSHRoutes) != 0 || options.Machine != "laptop" {
		t.Fatalf("the daemon's options with no configuration = %+v", options)
	}
}

// TestATargetAtThisDaemonsOwnAddressKeepsOnlyItsSSHRoute: the http route that
// leads back to this daemon is dropped; the target stays when it has an ssh
// route and goes when it has none.
func TestATargetAtThisDaemonsOwnAddressKeepsOnlyItsSSHRoute(t *testing.T) {
	t.Parallel()
	own := &cockpitfleet.HTTPRoute{URL: "http://localhost:8766", TokenFile: "/etc/wb/t"}
	var logs bytes.Buffer
	kept := withoutOwnAddress([]cockpitfleet.RemoteTarget{
		{Machine: "both", HTTP: own, SSH: &cockpitfleet.SSHRoute{Host: "both.example"}},
		{Machine: "http-only", HTTP: own},
		{Machine: "ssh-only", SSH: &cockpitfleet.SSHRoute{Host: "ssh.example"}},
	}, "127.0.0.1:8766", func(format string, args ...any) { _, _ = fmt.Fprintf(&logs, format+"\n", args...) })
	if len(kept) != 2 || kept[0].Machine != "both" || kept[0].HTTP != nil || kept[0].SSH == nil || kept[1].Machine != "ssh-only" {
		t.Fatalf("kept = %+v", kept)
	}
	if strings.Count(logs.String(), "is not read over http") != 2 || strings.Contains(logs.String(), "localhost") {
		t.Fatalf("log = %q", logs.String())
	}
}

// TestADaemonWithNoHTTPRouteSendsNothingAndOneWithARouteReadsIt runs the fleet
// options the daemon builds, on a fake hub: with cockpit.remote_http false, or
// with no http section, the started snapshotter sends no request; with the
// route configured it reads the hub's export route with the configured bearer
// (which proves the first three are not passing for want of a reader).
func TestADaemonWithNoHTTPRouteSendsNothingAndOneWithARouteReadsIt(t *testing.T) {
	t.Parallel()
	var requests atomic.Int64
	arrived := make(chan string, 16)
	hubServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		arrived <- request.Method + " " + request.URL.Path + " " + request.Header.Get("Authorization")
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(hubServer.Close)
	tokenFile := filepath.Join(t.TempDir(), "vm.token")
	if err := os.WriteFile(tokenFile, []byte("the-vm-credential\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withHTTP := fmt.Sprintf(sessionMoveHTTPTarget, hubServer.URL, tokenFile)
	disabled := wbconfig.DefaultCockpitConfig()
	disabled.RemoteHTTP = false
	start := func(yaml string, config wbconfig.CockpitConfig) (refresh, remote chan time.Time, stop func()) {
		options := cockpitFleetOptionsWith(t.TempDir(), t.TempDir(), cockpitConfigFile(t, yaml)(), config, io.Discard, func() (string, error) { return "laptop", nil }, cockpitSSH{})
		// The collectors and the sampler are replaced: a unit test reads no real
		// directory and runs no Git. The targets and transports are the daemon's.
		options.Collectors, options.Sampler = emptyMachineCollectors(), nil
		refresh, remote = make(chan time.Time), make(chan time.Time)
		options.Tick = func(time.Duration) (<-chan time.Time, func()) { return refresh, func() {} }
		options.RemoteTick = func(time.Duration) (<-chan time.Time, func()) { return remote, func() {} }
		return refresh, remote, cockpitfleet.New(options).Start(t.Context())
	}
	for name, test := range map[string]struct {
		yaml   string
		config wbconfig.CockpitConfig
	}{
		"no session_move section":   {"cockpit:\n  refresh_interval: 60s\n", wbconfig.DefaultCockpitConfig()},
		"only a synchestra section": {"session_move:\n  targets:\n    vm:\n      default_courier: synchestra\n      synchestra:\n        runner: vm\n", wbconfig.DefaultCockpitConfig()},
		"remote_http false":         {withHTTP, disabled},
	} {
		refresh, _, stop := start(test.yaml, test.config)
		// Two passes of the daemon: the tick is taken only after the pass before it.
		refresh <- exportTestNow
		refresh <- exportTestNow.Add(time.Minute)
		stop()
		if got := requests.Load(); got != 0 {
			t.Fatalf("%s: the daemon sent %d requests", name, got)
		}
	}
	_, remote, stop := start(withHTTP, wbconfig.DefaultCockpitConfig())
	remote <- exportTestNow
	select {
	case got := <-arrived:
		if got != "GET "+hub.MachineExportPath+" Bearer the-vm-credential" {
			t.Errorf("the configured route was read as %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Error("the configured route was never read")
	}
	stop()
}

// exportTestMount is a real daemon-hosted hub mount on a memory store, the
// handler of its API, the owner's machine credential as an Authorization value
// and the path of its token file. The rate limit's clock moves a second with
// every request, so the limit never interferes with a test that is not about it.
func exportTestMount(t *testing.T, address string) (mount *hubMount, get func(target string, headers ...string) *httptest.ResponseRecorder, owner, tokenFile string) {
	t.Helper()
	configPath := memoryHubConfig(t)
	var ticks atomic.Int64
	tuning := &hubTuning{Now: func() time.Time { return exportTestNow.Add(time.Duration(ticks.Add(1)) * time.Second) }}
	mount, err := mountHub(context.Background(), configPath, address, narrate.Writer{}, tuning)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	t.Cleanup(func() { _ = mount.Close() })
	api := mount.handlers()[hub.APIPrefix+"/"]
	get = func(target string, headers ...string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		for index := 0; index < len(headers); index += 2 {
			request.Header.Set(headers[index], headers[index+1])
		}
		recorder := httptest.NewRecorder()
		api.ServeHTTP(recorder, request)
		return recorder
	}
	remote, err := remotestate.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(remote.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	return mount, get, "Bearer " + strings.TrimSpace(string(raw)), remote.TokenFile
}

// TestMachineExportRouteServesThisMachineToItsOwnersCredentialOnly proves
// cockpit-views#ac:hub-export-route-requires-a-machine-bearer on a real
// daemon-hosted hub mount with real credentials: the machine credential the
// hub enrolled for its owner receives this machine's envelope (and the
// metrics-only one without a fleet), which the strict decoder accepts; a
// credential of another identity is refused with 403; an unknown bearer, a
// session cookie and no credential with 401; the route is outside the Cockpit
// API, whose Host guard still answers 421; and the HTTP client of the fleet
// reads the route end to end.
func TestMachineExportRouteServesThisMachineToItsOwnersCredentialOnly(t *testing.T) {
	ctx := context.Background()
	const address = "127.0.0.1:8798"
	mount, get, owner, tokenFile := exportTestMount(t, address)
	api := mount.handlers()[hub.APIPrefix+"/"]
	mount.serveExportOf(refreshedSnapshotter(t, "hub-host"), wbconfig.DefaultCockpitConfig())

	full := get(hub.MachineExportPath, "Authorization", owner)
	if full.Code != http.StatusOK || full.Header().Get("ETag") == "" || full.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("the owner's export = %d %v %s", full.Code, full.Header(), full.Body.String())
	}
	envelope, err := cockpitfleet.DecodeEnvelope(bytes.NewReader(full.Body.Bytes()), false, exportTestNow)
	if err != nil || envelope.Machine != "hub-host" || len(envelope.Fleet.Machines) != 1 || envelope.Fleet.Machines[0].Route != cockpitfleet.RouteLocal || !envelope.ExportedAt.Equal(exportTestNow) {
		t.Fatalf("the envelope = %+v, %v", envelope, err)
	}
	only := get(hub.MachineExportPath+"?metrics_only=1", "Authorization", owner)
	if metrics, err := cockpitfleet.DecodeEnvelope(bytes.NewReader(only.Body.Bytes()), true, exportTestNow); only.Code != http.StatusOK || err != nil || metrics.Fleet != nil || metrics.Metrics == nil || strings.Contains(only.Body.String(), `"fleet"`) {
		t.Fatalf("the metrics-only export = %d %s, %v", only.Code, only.Body.String(), err)
	}
	// The body is prepared once and served by the shared writer: gzip with its
	// own ETag when asked for, and 304 to a reader that already holds it.
	zipped := get(hub.MachineExportPath, "Authorization", owner, "Accept-Encoding", "gzip")
	if zipped.Code != http.StatusOK || zipped.Header().Get("Content-Encoding") != "gzip" || !strings.HasSuffix(zipped.Header().Get("ETag"), `-gzip"`) {
		t.Errorf("the gzip export = %d %v", zipped.Code, zipped.Header())
	}
	same := get(hub.MachineExportPath, "Authorization", owner, "If-None-Match", full.Header().Get("ETag"))
	if same.Code != http.StatusNotModified || same.Body.Len() != 0 {
		t.Errorf("a revalidation = %d with %d bytes, want 304", same.Code, same.Body.Len())
	}
	// A credentialed export is never to be kept by a client or an intermediary,
	// whichever shape, encoding or status answers.
	for name, response := range map[string]*httptest.ResponseRecorder{"full": full, "metrics only": only, "gzip": zipped, "not modified": same} {
		if got := response.Header().Values("Cache-Control"); len(got) != 1 || got[0] != "no-store" {
			t.Errorf("the %s export has Cache-Control %q, want no-store", name, got)
		}
	}

	stranger, err := mount.Enrollment.Enroll(ctx, hub.Viewer{Authenticated: true, IdentityID: "someone-else"}, hub.MachineEnrollmentRequest{Name: "their-laptop"})
	if err != nil {
		t.Fatal(err)
	}
	// A peer credential (peer:session alone) cannot be minted on a memory-engine
	// hub; hub.TestMachineExportRouteRefusesEveryOtherCaller proves its refusal
	// with real bearer resolution.
	for name, test := range map[string]struct {
		headers []string
		status  int
	}{
		"another identity":          {[]string{"Authorization", "Bearer " + stranger.Token}, http.StatusForbidden},
		"an unknown bearer":         {[]string{"Authorization", "Bearer " + strings.Repeat("x", 43)}, http.StatusUnauthorized},
		"a session cookie":          {[]string{"Cookie", "wb_cockpit_session_8798=owner-session; wb_cockpit_session=owner-session"}, http.StatusUnauthorized},
		"no credential":             {nil, http.StatusUnauthorized},
		"the loopback origin alone": {[]string{"Origin", "http://" + address}, http.StatusUnauthorized},
	} {
		for _, target := range []string{hub.MachineExportPath, hub.MachineExportPath + "?metrics_only=1"} {
			response := get(target, test.headers...)
			if response.Code != test.status || strings.Contains(response.Body.String(), "schema_version") || strings.Contains(response.Body.String(), "hub-host") {
				t.Errorf("%s: %s = %d %s, want %d and nothing of the envelope", name, target, response.Code, response.Body.String(), test.status)
			}
		}
	}

	// The route is the hub's, not the Cockpit's: the Cockpit routes on the same
	// listener still refuse a foreign Host before any handler runs, and the
	// Cockpit API has no such route.
	if hub.MachineExportPath != cockpitfleet.MachineExportPath || strings.HasPrefix(hub.MachineExportPath, cockpit.APIPrefix) {
		t.Errorf("the route is %s, the client's %s", hub.MachineExportPath, cockpitfleet.MachineExportPath)
	}
	server := newCockpitServer(address, wbconfig.DefaultCockpitConfig())
	registerCockpitFleet(server, cockpitfleet.Options{Collectors: emptyMachineCollectors()})
	mounts := server.MountsWith(mount.handlers())
	foreign := httptest.NewRequest(http.MethodGet, cockpit.APIPrefix+cockpitfleet.FleetRoute, nil)
	foreign.Host = "vm.example"
	foreign.Header.Set("Authorization", owner)
	refused := httptest.NewRecorder()
	mounts[cockpit.APIPrefix].ServeHTTP(refused, foreign)
	if refused.Code != http.StatusMisdirectedRequest || strings.Contains(refused.Body.String(), "schema_version") {
		t.Errorf("a Cockpit route with Host: vm.example = %d %s, want 421", refused.Code, refused.Body.String())
	}
	local := httptest.NewRequest(http.MethodGet, cockpit.APIPrefix+"machines/export", nil)
	local.Host = address
	local.Header.Set("Authorization", owner)
	absent := httptest.NewRecorder()
	mounts[cockpit.APIPrefix].ServeHTTP(absent, local)
	if absent.Code == http.StatusOK || strings.Contains(absent.Body.String(), "hub-host") {
		t.Errorf("the Cockpit API serves an export: %d %s", absent.Code, absent.Body.String())
	}

	// The fleet's HTTP client reads the route end to end, on a loopback listener.
	listener := httptest.NewServer(api)
	defer listener.Close()
	exporter := cockpitfleet.NewHTTPExporter(func() time.Time { return exportTestNow })
	read, err := exporter.Export(ctx, cockpitfleet.RemoteTarget{Machine: "vm", HTTP: &cockpitfleet.HTTPRoute{URL: listener.URL, TokenFile: tokenFile}}, false)
	if err != nil || read.Machine != "hub-host" || read.Fleet == nil {
		t.Fatalf("the client's read of the route = %+v, %v", read, err)
	}
	// A daemon with no hub has nothing to bind and no such route.
	var none *hubMount
	none.serveExportOf(refreshedSnapshotter(t, "hub-host"), wbconfig.DefaultCockpitConfig())
}

// TestMachineExportRouteSaysWhyThereIsNoEnvelope proves the typed reasons of
// cockpit-views#req:hub-export-route on the real mount, and what the fleet's
// HTTP client makes of each: a daemon that has not bound its snapshotter, or
// whose first pass has not ended, answers 503 warming_up, which the client
// takes as no failure at all; a machine with cockpit.anonymous_metadata false
// answers 403 export_refused to its own owner's credential, for both shapes,
// which the client shows as export_refused without trying another transport;
// and an envelope that fails its own rules answers 503 export_failed, which
// the client shows as bad_payload, never as the transport being unavailable.
// No reason carries anything of the machine.
func TestMachineExportRouteSaysWhyThereIsNoEnvelope(t *testing.T) {
	const address = "127.0.0.1:8797"
	mount, get, owner, tokenFile := exportTestMount(t, address)
	listener := httptest.NewServer(mount.handlers()[hub.APIPrefix+"/"])
	defer listener.Close()
	exporter := cockpitfleet.NewHTTPExporter(func() time.Time { return exportTestNow })
	read := func(metricsOnly bool) error {
		_, err := exporter.Export(context.Background(), cockpitfleet.RemoteTarget{Machine: "vm", HTTP: &cockpitfleet.HTTPRoute{URL: listener.URL, TokenFile: tokenFile}}, metricsOnly)
		return err
	}
	requireReason := func(name, target string, status int, code string) {
		t.Helper()
		response := get(target, "Authorization", owner)
		if response.Code != status || strings.TrimSpace(response.Body.String()) != `{"error":"`+code+`"}` || response.Header().Get("ETag") != "" {
			t.Fatalf("%s: %s = %d %s, want %d %s", name, target, response.Code, response.Body.String(), status, code)
		}
	}

	requireReason("no snapshotter bound", hub.MachineExportPath, http.StatusServiceUnavailable, "warming_up")
	if err := read(false); !errors.Is(err, cockpitfleet.ErrRemoteWarmingUp) {
		t.Errorf("the client's read of an unbound daemon = %v, want warming up", err)
	}

	// A snapshotter that has taken no snapshot is warming up: no partial fleet is
	// served, and its metrics-only export is.
	warming := cockpitfleet.New(cockpitfleet.Options{Machine: "hub-host", Version: "v1.2.3", Collectors: emptyMachineCollectors(), Now: func() time.Time { return exportTestNow }})
	mount.serveExportOf(warming, wbconfig.DefaultCockpitConfig())
	requireReason("a warming daemon", hub.MachineExportPath, http.StatusServiceUnavailable, "warming_up")
	if err := read(false); !errors.Is(err, cockpitfleet.ErrRemoteWarmingUp) {
		t.Errorf("the client's read of a warming daemon = %v, want warming up", err)
	}
	if only := get(hub.MachineExportPath+"?metrics_only=1", "Authorization", owner); only.Code != http.StatusOK {
		t.Errorf("the metrics-only export of a warming daemon = %d %s", only.Code, only.Body.String())
	}

	// The machine does not export its metadata: no transport overrides that.
	private := wbconfig.DefaultCockpitConfig()
	private.AnonymousMetadata = false
	mount.serveExportOf(refreshedSnapshotter(t, "hub-host"), private)
	for _, target := range []string{hub.MachineExportPath, hub.MachineExportPath + "?metrics_only=1"} {
		requireReason("anonymous_metadata false", target, http.StatusForbidden, "export_refused")
	}
	for _, metricsOnly := range []bool{false, true} {
		var failure *cockpitfleet.RemoteError
		if err := read(metricsOnly); !errors.As(err, &failure) || failure.Code != cockpitfleet.RemoteErrorExportRefused || failure.Fallback {
			t.Errorf("the client's read of a machine that refuses = %v, want export_refused and no fallback", err)
		}
	}

	// A machine whose own entry would not pass the envelope's rules has no export.
	mount.serveExportOf(refreshedSnapshotter(t, strings.Repeat("m", 300)), wbconfig.DefaultCockpitConfig())
	requireReason("an invalid envelope", hub.MachineExportPath, http.StatusServiceUnavailable, "export_failed")
	var failure *cockpitfleet.RemoteError
	if err := read(false); !errors.As(err, &failure) || failure.Code != cockpitfleet.RemoteErrorBadPayload || failure.Fallback {
		t.Errorf("the client's read of an envelope that fails its rules = %v, want bad_payload and no fallback", err)
	}
}

// TestATargetThatIsThisDaemonsOwnAddressIsNotRead proves the self-fetch guard:
// a target whose http url is this daemon's own listener, by its address or by
// any loopback name on its port, is dropped with a diagnostic that names the
// configured key and not the url; other targets are kept.
func TestATargetThatIsThisDaemonsOwnAddressIsNotRead(t *testing.T) {
	t.Parallel()
	target := func(machine, address string) cockpitfleet.RemoteTarget {
		return cockpitfleet.RemoteTarget{Machine: machine, HTTP: &cockpitfleet.HTTPRoute{URL: address, TokenFile: "/etc/wb/" + machine + ".token"}}
	}
	targets := []cockpitfleet.RemoteTarget{
		target("same", "http://127.0.0.1:8766"), target("by-name", "http://localhost:8766/"), target("by-v6", "http://[::1]:8766"),
		target("other-port", "http://127.0.0.1:8767"), target("vm", "https://vm.example"), target("vm-port", "https://vm.example:8766"),
		{Machine: "ssh-only"},
	}
	var logs bytes.Buffer
	logf := func(format string, args ...any) { _, _ = fmt.Fprintf(&logs, format+"\n", args...) }
	kept := withoutOwnAddress(targets, "127.0.0.1:8766", logf)
	var names []string
	for _, item := range kept {
		names = append(names, item.Machine)
	}
	if strings.Join(names, ",") != "other-port,vm,vm-port,ssh-only" {
		t.Errorf("kept %v", names)
	}
	for _, dropped := range []string{"same", "by-name", "by-v6"} {
		if !strings.Contains(logs.String(), "cockpit fleet: "+dropped+" is not read over http: its http url is this daemon's own address") {
			t.Errorf("no diagnostic for %s: %q", dropped, logs.String())
		}
	}
	if strings.Contains(logs.String(), "8766") || strings.Contains(logs.String(), "token") {
		t.Errorf("the diagnostic carries an address or a path: %q", logs.String())
	}
	if all := withoutOwnAddress(targets, "not-an-address", logf); len(all) != len(targets) {
		t.Errorf("with no usable address %d of %d targets are kept", len(all), len(targets))
	}
}
