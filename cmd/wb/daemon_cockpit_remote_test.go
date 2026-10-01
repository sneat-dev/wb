package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/discover"
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
// half of cockpit-views#req:remote-http-fetch and of
// cockpit-views#ac:no-route-configured-makes-no-request: a machine is read over
// HTTP only when session_move.targets names it with an http section, its
// credential is that section's token_file or, for the hub this machine is
// enrolled with, remote.token_file; and with no session_move section, a target
// with no http section, or cockpit.remote_http false there is no target and no
// transport at all.
func TestCockpitRemotesComeOnlyFromTheLocalConfiguration(t *testing.T) {
	t.Parallel()
	enabled := wbconfig.DefaultCockpitConfig()
	disabled := enabled
	disabled.RemoteHTTP = false
	if !enabled.RemoteHTTP || !enabled.RemoteSSH {
		t.Fatal("cockpit.remote_http and cockpit.remote_ssh are not on by default")
	}
	sshOff, err := wbconfig.LoadCockpit(cockpitConfigFile(t, "cockpit:\n  remote_ssh: false\n")())
	if err != nil || sshOff.RemoteSSH || !sshOff.RemoteHTTP {
		t.Fatalf("cockpit.remote_ssh: false = %+v, %v", sshOff, err)
	}
	const hubRemote = "remote:\n  provider: hub\n  url: https://Hub.Example:443/\n  token_file: /etc/wb/hub.token\n  machine: laptop\n"
	withHTTP := fmt.Sprintf(sessionMoveHTTPTarget, "https://vm.example", "/etc/wb/vm.token")
	for name, test := range map[string]struct {
		config  wbconfig.CockpitConfig
		yaml    string
		want    map[string]cockpitfleet.HTTPRoute
		logged  string
		noLogOf string
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
		"only an ssh section":                  {config: enabled, yaml: "session_move:\n  targets:\n    vm:\n      default_courier: ssh\n      ssh:\n        host: vm\n"},
		"an ssh target and remote_ssh false":   {config: sshOff, yaml: "cockpit:\n  remote_ssh: false\nsession_move:\n  targets:\n    vm:\n      default_courier: ssh\n      ssh:\n        host: vm\n"},
		"an http target and remote_http false": {config: disabled, yaml: withHTTP},
		"an http target": {config: enabled, yaml: withHTTP, want: map[string]cockpitfleet.HTTPRoute{
			"vm": {URL: "https://vm.example", TokenFile: "/etc/wb/vm.token"},
		}},
		"the enrolled hub needs no token_file": {config: enabled, yaml: hubRemote + `session_move:
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
`, want: map[string]cockpitfleet.HTTPRoute{
			"alpha":       {URL: "https://alpha.example", TokenFile: "/etc/wb/alpha.token"},
			"hub-machine": {URL: "https://hub.example", TokenFile: "/etc/wb/hub.token"},
		}, logged: "elsewhere is not read over http"},
		"no token_file and no hub": {config: enabled, yaml: "session_move:\n  targets:\n    vm:\n      default_courier: ssh\n      ssh:\n        host: vm\n      http:\n        url: https://vm.example\n",
			logged: "vm is not read over http"},
		"an address a credential must not be sent to": {config: enabled, yaml: fmt.Sprintf(sessionMoveHTTPTarget, "http://vm.example", "/etc/wb/vm.token"),
			logged: "other machines are not read live", noLogOf: "/etc/wb/vm.token"},
	} {
		configPath := filepath.Join(t.TempDir(), "absent.yaml")
		if test.yaml != "" {
			configPath = cockpitConfigFile(t, test.yaml)()
		}
		var logs bytes.Buffer
		targets, transports := cockpitRemotes(configPath, test.config, func(format string, args ...any) { _, _ = fmt.Fprintf(&logs, format+"\n", args...) })
		if len(targets) != len(test.want) || (len(test.want) == 0) != (len(transports) == 0) {
			t.Errorf("%s: %d targets and %d transports, want %d targets", name, len(targets), len(transports), len(test.want))
			continue
		}
		for index, target := range targets {
			route, wanted := test.want[target.Machine]
			if !wanted || target.HTTP == nil || *target.HTTP != route {
				t.Errorf("%s: target %s = %+v", name, target.Machine, target.HTTP)
			}
			if index > 0 && targets[index-1].Machine >= target.Machine {
				t.Errorf("%s: the targets are not in order: %s before %s", name, targets[index-1].Machine, target.Machine)
			}
		}
		if len(transports) == 1 && (transports[0].Name != cockpitfleet.TransportHTTP || transports[0].Exporter == nil) {
			t.Errorf("%s: the transport = %+v", name, transports[0])
		}
		if (test.logged == "") != (logs.Len() == 0) || !strings.Contains(logs.String(), test.logged) || (test.noLogOf != "" && strings.Contains(logs.String(), test.noLogOf)) {
			t.Errorf("%s: log = %q, want %q", name, logs.String(), test.logged)
		}
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
		options := cockpitFleetOptions(t.TempDir(), t.TempDir(), cockpitConfigFile(t, yaml)(), config, io.Discard, func() (string, error) { return "laptop", nil })
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
	configPath := memoryHubConfig(t)
	const address = "127.0.0.1:8798"
	mount, err := mountHub(ctx, configPath, address, narrate.Writer{}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	defer func() { _ = mount.Close() }()
	api := mount.handlers()[hub.APIPrefix+"/"]
	get := func(target string, headers ...string) *httptest.ResponseRecorder {
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
	owner := "Bearer " + strings.TrimSpace(string(raw))

	// Until the daemon has bound its snapshotter there is no envelope.
	if early := get(hub.MachineExportPath, "Authorization", owner); early.Code != http.StatusServiceUnavailable {
		t.Fatalf("before the snapshotter is bound = %d %s", early.Code, early.Body.String())
	}
	mount.serveExportOf(refreshedSnapshotter(t, "hub-host"))

	full := get(hub.MachineExportPath, "Authorization", owner)
	if full.Code != http.StatusOK {
		t.Fatalf("the owner's export = %d %s", full.Code, full.Body.String())
	}
	envelope, err := cockpitfleet.DecodeEnvelope(bytes.NewReader(full.Body.Bytes()), false, exportTestNow)
	if err != nil || envelope.Machine != "hub-host" || len(envelope.Fleet.Machines) != 1 || envelope.Fleet.Machines[0].Route != cockpitfleet.RouteLocal || !envelope.ExportedAt.Equal(exportTestNow) {
		t.Fatalf("the envelope = %+v, %v", envelope, err)
	}
	only := get(hub.MachineExportPath+"?metrics_only=1", "Authorization", owner)
	if metrics, err := cockpitfleet.DecodeEnvelope(bytes.NewReader(only.Body.Bytes()), true, exportTestNow); only.Code != http.StatusOK || err != nil || metrics.Fleet != nil || metrics.Metrics == nil || strings.Contains(only.Body.String(), `"fleet"`) {
		t.Fatalf("the metrics-only export = %d %s, %v", only.Code, only.Body.String(), err)
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
	read, err := exporter.Export(ctx, cockpitfleet.RemoteTarget{Machine: "vm", HTTP: &cockpitfleet.HTTPRoute{URL: listener.URL, TokenFile: remote.TokenFile}}, false)
	if err != nil || read.Machine != "hub-host" || read.Fleet == nil {
		t.Fatalf("the client's read of the route = %+v, %v", read, err)
	}

	// A machine whose own entry would not pass the envelope's rules has no export.
	mount.serveExportOf(refreshedSnapshotter(t, strings.Repeat("m", 300)))
	if invalid := get(hub.MachineExportPath, "Authorization", owner); invalid.Code != http.StatusServiceUnavailable || strings.Contains(invalid.Body.String(), "mmmm") {
		t.Errorf("an export that fails its own rules = %d %s, want 503", invalid.Code, invalid.Body.String())
	}
	// A daemon with no hub has nothing to bind and no such route.
	var none *hubMount
	none.serveExportOf(refreshedSnapshotter(t, "hub-host"))
}
