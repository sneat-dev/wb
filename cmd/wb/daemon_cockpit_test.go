package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemonhost"
	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/daemon"
)

// TestCockpitIsMountedOnTheLoopbackListenerWithoutAHub serves the real daemon
// with no hub: section and requests Cockpit over TCP with chosen Host headers
// (cockpit#ac:foreign-host-is-refused, cockpit#ac:unbuilt-application-says-so,
// cockpit#ac:legacy-dashboard-is-retired).
func TestCockpitIsMountedOnTheLoopbackListenerWithoutAHub(t *testing.T) {
	root := daemonShutdownTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.Token = func() (string, error) { return "owner-token", nil }
	deps.HubConfigPath = cockpitConfigFile(t, "cockpit:\n  refresh_interval: 45s\n  anonymous_metadata: false\n")
	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout bytes.Buffer
	stderr := &lockedBuffer{} // the daemon's goroutines write to it together
	command.SetOut(&stdout)
	command.SetErr(stderr)

	served := make(chan error, 1)
	go func() {
		served <- newDaemonHost(deps).Serve(command.Context(), daemonhost.Request{ProjectsRoot: root, Listen: address, Quiet: true, ManagedStart: false}, command.OutOrStdout(), command.ErrOrStderr())
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("serveDashboard: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("serveDashboard did not return after its context was cancelled")
		}
	})
	waitForHealth(t, address)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(host, path string) (*http.Response, string) {
		t.Helper()
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+address+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = host
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response, string(body)
	}

	port := address[strings.LastIndex(address, ":")+1:]

	response, body := get(address, "/cockpit/")
	if response.StatusCode != http.StatusOK || (!strings.Contains(body, "pnpm install && pnpm build") && !strings.Contains(body, "<")) {
		t.Fatalf("/cockpit/ = %s %q, want the not-built page naming the build command", response.Status, body)
	}
	response, body = get("attacker.example:"+port, "/api/v1/cockpit/fleet")
	if response.StatusCode != http.StatusMisdirectedRequest || !strings.Contains(body, "misdirected request") || strings.Contains(body, "fleet") {
		t.Fatalf("foreign host = %s %q, want 421 and no fleet data", response.Status, body)
	}
	response, body = get("localhost:"+port, "/cockpit/")
	if want := "http://127.0.0.1:" + port + "/cockpit/"; response.StatusCode != http.StatusTemporaryRedirect || response.Header.Get("Location") != want {
		t.Fatalf("loopback alias = %s %q %q, want a redirect to %s", response.Status, response.Header.Get("Location"), body, want)
	}
	response, body = get(address, "/api/v1/cockpit/nothing-here")
	if response.StatusCode != http.StatusNotFound || !strings.Contains(body, `"error"`) {
		t.Fatalf("unknown cockpit api route = %s %q, want a JSON 404", response.Status, body)
	}
	// The fleet read model is a metadata route, so with anonymous_metadata off
	// a request with no session is refused like the session route is, and so
	// is the README route, which is an owner route.
	for _, path := range []string{"/api/v1/cockpit/fleet", cockpitfleet.ReadmePath + "?repository=repo-x"} {
		if response, body = get(address, path); response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s with no session = %s %q, want 401", path, response.Status, body)
		}
	}
	response, body = get("attacker.example:"+port, "/")
	if response.StatusCode != http.StatusMisdirectedRequest || strings.Contains(body, "cockpit") {
		t.Fatalf("/ on a foreign host = %s %q, want 421 and no redirect", response.Status, body)
	}
	// The listener's root is Cockpit's mount; the retired pages and their
	// overview route are gone (cockpit#ac:legacy-dashboard-is-retired).
	if response, _ = get(address, "/"); response.StatusCode != http.StatusFound || response.Header.Get("Location") != cockpit.PagePrefix {
		t.Fatalf("/ = %s %q, want a redirect to %s", response.Status, response.Header.Get("Location"), cockpit.PagePrefix)
	}
	for _, retired := range []string{"/metrics", "/coverage", "/api/v1/overview", "/dashboard-assets/index.js", "/dashboard-assets/metrics.js"} {
		if response, body = get(address, retired); response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s = %s %q, want 404: the page is retired", retired, response.Status, body)
		}
	}

	// The owner session (cockpit#ac:login-code-is-single-use,
	// cockpit#ac:proxied-request-needs-a-session): a login code is minted only
	// over the owner-token socket, and exchanged on the loopback listener.
	const sessionPath = "/api/v1/cockpit/session"
	response, body = get(address, sessionPath)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session with anonymous_metadata off = %s %q, want 401", response.Status, body)
	}
	// mint calls the login-code route on the socket; an empty token sends no
	// Authorization header at all. sessionKey is the session key of the last
	// answer, which only the owner channel is ever told.
	var sessionKey string
	mint := func(method, token string) (int, string) {
		t.Helper()
		socket, err := daemonruntime.LocalHTTPClient(root, token)
		if err != nil {
			t.Fatal(err)
		}
		if token == "" {
			path, pathErr := daemon.SocketPath(root)
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			socket.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			}}
		}
		request, err := http.NewRequestWithContext(context.Background(), method, daemonruntime.RPCBaseURL+cockpit.LoginCodeRPCPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := socket.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var minted struct {
			Code string `json:"code"`
			Key  string `json:"key"`
			Path string `json:"path"`
		}
		_ = json.NewDecoder(response.Body).Decode(&minted)
		sessionKey = minted.Key
		return response.StatusCode, minted.Path + "?code=" + minted.Code
	}
	for name, test := range map[string]struct {
		method, token string
		status        int
	}{
		"the wrong token":            {http.MethodPost, "not-the-owner-token", http.StatusUnauthorized},
		"no Authorization header":    {http.MethodPost, "", http.StatusUnauthorized},
		"a GET with the wrong token": {http.MethodGet, "not-the-owner-token", http.StatusUnauthorized},
		"a GET with the owner token": {http.MethodGet, "owner-token", http.StatusMethodNotAllowed},
	} {
		if status, login := mint(test.method, test.token); status != test.status || login != "?code=" || sessionKey != "" {
			t.Fatalf("minting with %s = %d %q, want %d, no code and no key", name, status, login, test.status)
		}
	}
	status, login := mint(http.MethodPost, "owner-token")
	if status != http.StatusOK || !strings.HasPrefix(login, cockpit.LoginPath+"?code=") || len(login) < len(cockpit.LoginPath)+40 || len(sessionKey) < 43 {
		t.Fatalf("minting with the owner token = %d and a login path of %d characters", status, len(login))
	}
	// The mint route is not on the loopback listener, with or without the
	// owner token: the path falls to the dashboard, which refuses it.
	for _, authorization := range []string{"", "Bearer owner-token"} {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+address+cockpit.LoginCodeRPCPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		unauthenticated, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		leaked, _ := io.ReadAll(unauthenticated.Body)
		_ = unauthenticated.Body.Close()
		if unauthenticated.StatusCode < http.StatusBadRequest || strings.Contains(string(leaked), `"code"`) {
			t.Fatalf("POST to the mint path on the loopback listener = %s %q, want a refusal and no code", unauthenticated.Status, leaked)
		}
	}
	response, _ = get(address, login)
	cookies := response.Cookies()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/cockpit/" || len(cookies) != 1 || cookies[0].Name != "wb_cockpit_session_"+port {
		t.Fatalf("login = %s to %q with %d cookies", response.Status, response.Header.Get("Location"), len(cookies))
	}
	if response, _ = get(address, login); response.StatusCode != http.StatusUnauthorized || len(response.Cookies()) != 0 {
		t.Fatalf("replayed login = %s, want 401 and no cookie", response.Status)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+address+sessionPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookies[0])
	// The cookie alone is what another server on the loopback host could
	// replay: with anonymous_metadata off it is refused like no session
	// (cockpit#ac:replayed-cookie-is-not-the-owner).
	replayed, err := client.Do(request.Clone(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	_ = replayed.Body.Close()
	if replayed.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session with the cookie and no session key = %s, want 401", replayed.Status)
	}
	request.Header.Set(cockpit.SessionKeyHeader, sessionKey)
	owned, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	ownedBody, _ := io.ReadAll(owned.Body)
	_ = owned.Body.Close()
	if owned.StatusCode != http.StatusOK || !strings.Contains(string(ownedBody), `"principal":"owner"`) || !strings.Contains(string(ownedBody), `"repo.content.read"`) {
		t.Fatalf("session with the cookie and the session key = %s %q", owned.Status, ownedBody)
	}

	// The daemon started the fleet snapshotter with its own context: the
	// first snapshot completes on its own and lists this machine, and the
	// README route answers an unknown repository with a 404, not a path.
	owner := func(path string) (int, string) {
		t.Helper()
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+address+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(cookies[0])
		request.Header.Set(cockpit.SessionKeyHeader, sessionKey)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(body)
	}
	// The daemon's log route asks Cockpit who the owner is
	// (cockpit#ac:daemon-log-needs-an-owner-session): with
	// no session it is refused before anything is read, and with the session
	// the request gets past the gate. The invalid tail keeps this test from
	// reading the machine's real daemon log, which is where the path points on
	// macOS.
	if response, body = get(address, "/api/v1/log?tail=x"); response.StatusCode != http.StatusUnauthorized || !strings.Contains(body, `"error":"owner_session_required"`) || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("/api/v1/log with no session = %s %q, want 401 owner_session_required", response.Status, body)
	}
	if status, body := owner("/api/v1/log?tail=x"); status != http.StatusBadRequest || !strings.Contains(body, `"error":"invalid_tail"`) {
		t.Fatalf("/api/v1/log with the owner session = %d %q, want it past the owner check (400 invalid_tail)", status, body)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		status, fleetBody := owner("/api/v1/cockpit/fleet")
		if status == http.StatusOK && strings.Contains(fleetBody, `"warming_up":false`) && strings.Contains(fleetBody, `"route":"local"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon's fleet snapshot did not complete: %d %q", status, fleetBody)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status, body := owner(cockpitfleet.ReadmePath + "?repository=repo-x"); status != http.StatusNotFound || !strings.Contains(body, "unknown_repository") {
		t.Fatalf("README of an unknown repository = %d %q, want 404", status, body)
	}
}

// TestCockpitFleetOptionsReadThisMachineAndTheConfiguredRemote covers what
// the daemon hands the fleet snapshotter: the host's name and no other
// machines without a remote section, the configured machine name and a
// provider with one, "local" when the host has no name, the refresh interval,
// and a log that writes to the daemon's stderr.

// TestCockpitFleetOptionsConfigureTheCodeIndexProviderOnlyWhenNamed covers the
// provider the daemon hands the snapshotter: none by default, which the
// document reports, and CodeGrapher, following the configured indexer, when
// cockpit.code_index_provider names it. Nothing here runs it.

// TestCockpitFleetOptionsObservePullRequestsThroughTheWatcherWithTheConfiguredLimit
// pins that the daemon hands the snapshotter the production watcher and
// cockpit.pull_request_limit. Nothing here observes a pull request.

// TestCockpitRegisterFleetServesTheWarmingDocumentBeforeTheFirstSnapshot
// registers the fleet routes on a server and requests them before the
// snapshotter has started: an empty warming-up document and a 401 for the
// README without a session.

// cockpitConfigFile writes a wb.yaml and returns the path function the daemon's
// dependencies take.
func cockpitConfigFile(t *testing.T, content string) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return func() string { return path }
}

func TestCockpitInvalidConfigurationStopsTheDaemonFromServing(t *testing.T) {
	root := daemonShutdownTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.Token = func() (string, error) { return "owner-token", nil }
	deps.HubConfigPath = cockpitConfigFile(t, "cockpit:\n  hosted_url: not-a-url\n")
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	err := newDaemonHost(deps).Serve(command.Context(), daemonhost.Request{ProjectsRoot: root, Listen: freeLoopbackAddress(t), Quiet: true, ManagedStart: false}, command.OutOrStdout(), command.ErrOrStderr())
	if err == nil || !strings.Contains(err.Error(), "cockpit configuration") || !strings.Contains(err.Error(), "cockpit.hosted_url") {
		t.Fatalf("serveDashboard = %v, want an error naming the cockpit section", err)
	}
}

// TestDaemonRefusesToServeOnAListenerBoundOutsideLoopback: whatever the
// --listen name resolved to, a listener that holds a non-loopback address is
// closed unserved and the start fails with a usage error naming it.
func TestDaemonRefusesToServeOnAListenerBoundOutsideLoopback(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.Token = func() (string, error) { return "owner-token", nil }
	real, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &fakeBoundListener{Listener: real, bound: &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 8766}}
	deps.listen = func(string, string) (net.Listener, error) { return wrapped, nil }
	command := &cobra.Command{}
	command.SetContext(context.Background())
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	err = newDaemonHost(deps).Serve(command.Context(), daemonhost.Request{ProjectsRoot: root, Listen: "localhost:8766", Quiet: true, ManagedStart: false}, command.OutOrStdout(), command.ErrOrStderr())
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage || !strings.Contains(err.Error(), "192.0.2.10:8766") {
		t.Fatalf("serveDashboard = %v, want a usage error naming the bound address", err)
	}
	if _, acceptErr := real.Accept(); acceptErr == nil {
		t.Fatal("the listener was left open")
	}
}

// fakeBoundListener reports another address than the one it holds.
type fakeBoundListener struct {
	net.Listener
	bound net.Addr
}

func (listener *fakeBoundListener) Addr() net.Addr { return listener.bound }

// TestCockpitServerKeepsSessionsInMemorySoARestartEndsThem pins what the
// daemon builds: a session established on one run's server is unknown to the
// next run's (cockpit#ac:session-ends-on-logout-and-restart).

// TestCockpitLoginCodeRouteIsRefusedByTheFileBridge pins that the file
// bridge, which dispatches into the same mux as the unix socket, forwards
// only the DaemonService procedures it lists.
