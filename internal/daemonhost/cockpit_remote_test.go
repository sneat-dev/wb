package daemonhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

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
