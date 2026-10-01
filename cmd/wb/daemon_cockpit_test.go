package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// TestCockpitIsMountedOnTheLoopbackListenerWithoutAHub serves the real daemon
// with no hub: section and requests Cockpit over TCP with chosen Host headers
// (cockpit#ac:foreign-host-is-refused, cockpit#ac:unbuilt-application-says-so,
// cockpit#ac:dashboard-command-is-unchanged).
func TestCockpitIsMountedOnTheLoopbackListenerWithoutAHub(t *testing.T) {
	root := daemonShutdownTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.hubConfigPath = cockpitConfigFile(t, "cockpit:\n  refresh_interval: 45s\n  anonymous_metadata: false\n")
	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)

	served := make(chan error, 1)
	go func() {
		served <- serveDashboard(&invocation{projectsRoot: root}, command, deps, address, daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}, "owner-token", true, false)
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
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "WB operations") {
		t.Fatalf("/ on a foreign host = %s %q, want it unchanged", response.Status, body)
	}
	response, body = get(address, "/metrics")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "WB Metrics") {
		t.Fatalf("/metrics = %s %q", response.Status, body)
	}
	response, body = get(address, "/api/v1/overview")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"schema_version"`) {
		t.Fatalf("/api/v1/overview = %s %q", response.Status, body)
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
	// Authorization header at all.
	mint := func(method, token string) (int, string) {
		t.Helper()
		socket, err := daemonLocalHTTPClient(root, token)
		if err != nil {
			t.Fatal(err)
		}
		if token == "" {
			socket.Transport = socket.Transport.(daemonAuthenticatedTransport).base
		}
		request, err := http.NewRequestWithContext(context.Background(), method, daemonRPCBaseURL+cockpit.LoginCodeRPCPath, nil)
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
			Path string `json:"path"`
		}
		_ = json.NewDecoder(response.Body).Decode(&minted)
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
		if status, login := mint(test.method, test.token); status != test.status || login != "?code=" {
			t.Fatalf("minting with %s = %d %q, want %d and no code", name, status, login, test.status)
		}
	}
	status, login := mint(http.MethodPost, "owner-token")
	if status != http.StatusOK || !strings.HasPrefix(login, cockpit.LoginPath+"?code=") || len(login) < len(cockpit.LoginPath)+40 {
		t.Fatalf("minting with the owner token = %d and a login path of %d characters", status, len(login))
	}
	// The mint route is not on the loopback listener, with or without the
	// owner token: the path falls to the dashboard, which refuses a POST.
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
		if unauthenticated.StatusCode != http.StatusMethodNotAllowed || strings.Contains(string(leaked), `"code"`) {
			t.Fatalf("POST to the mint path on the loopback listener = %s %q, want 405 and no code", unauthenticated.Status, leaked)
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
	owned, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	ownedBody, _ := io.ReadAll(owned.Body)
	_ = owned.Body.Close()
	if owned.StatusCode != http.StatusOK || !strings.Contains(string(ownedBody), `"principal":"owner"`) || !strings.Contains(string(ownedBody), `"repo.content.read"`) {
		t.Fatalf("session with the cookie = %s %q", owned.Status, ownedBody)
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
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(body)
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
func TestCockpitFleetOptionsReadThisMachineAndTheConfiguredRemote(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	host := func() (string, error) { return "the-host", nil }
	config := wbconfig.DefaultCockpitConfig()
	config.RefreshInterval = 90 * time.Second
	var logs bytes.Buffer

	bare := cockpitFleetOptions(root, home, filepath.Join(t.TempDir(), "absent.yaml"), config, &logs, host)
	if bare.Machine != "the-host" || bare.Collectors.Remote != nil || bare.Interval != 90*time.Second || bare.Collectors.Repositories == nil {
		t.Errorf("options with no remote section = %+v", bare)
	}
	bare.Logf("refresh failed: %v", "boom")
	if got := logs.String(); got != "wb: refresh failed: boom\n" {
		t.Errorf("log = %q", got)
	}
	nameless := cockpitFleetOptions(root, home, filepath.Join(t.TempDir(), "absent.yaml"), config, &logs, func() (string, error) { return "", io.EOF })
	if nameless.Machine != "local" {
		t.Errorf("machine without a host name = %q, want local", nameless.Machine)
	}
	remote := cockpitFleetOptions(root, home, cockpitConfigFile(t, "remote:\n  provider: git\n  repo: acme/wb-state\n  machine: laptop-1\n")(), config, &logs, host)
	if remote.Machine != "laptop-1" || remote.Collectors.Remote == nil {
		t.Errorf("options with a remote section = machine %q, remote %v", remote.Machine, remote.Collectors.Remote)
	}

	// A hub provider and an unlocatable store know no other machines, and the
	// daemon's log says so, once, while the options are built.
	logs.Reset()
	hub := cockpitFleetOptions(root, home, cockpitConfigFile(t, "remote:\n  provider: hub\n  url: https://hub.example\n  token_file: /tmp/token\n  machine: laptop-2\n")(), config, &logs, host)
	if hub.Machine != "laptop-2" || hub.Collectors.Remote != nil || !strings.Contains(logs.String(), "not read from the hub remote provider") {
		t.Errorf("options with a hub provider = machine %q, remote %v, log %q", hub.Machine, hub.Collectors.Remote, logs.String())
	}
	logs.Reset()
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	unlocatable := cockpitFleetOptions(file, home, cockpitConfigFile(t, "remote:\n  provider: git\n  repo: acme/wb-state\n  machine: laptop-3\n")(), config, &logs, host)
	if unlocatable.Collectors.Remote != nil || !strings.Contains(logs.String(), "cannot be located") {
		t.Errorf("options with an unlocatable store = remote %v, log %q", unlocatable.Collectors.Remote, logs.String())
	}
}

// TestCockpitFleetSnapshotThroughTheDaemonsWiringReadsOtherMachinesWithoutNetworkOrWrites
// builds the snapshotter from the daemon's own options for a remote section
// whose local state clone exists and whose origin could not be reached: it reads
// the other machine from the clone and changes nothing under it.
func TestCockpitFleetSnapshotThroughTheDaemonsWiringReadsOtherMachinesWithoutNetworkOrWrites(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(root, "github.com", "acme", "wb-state")
	published := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	data, err := remotestate.Encode(remotestate.Snapshot{SchemaVersion: remotestate.SchemaVersion, Login: "alice", Machine: "desk", PublishedAt: published, WBVersion: "v0.9.0"})
	if err != nil {
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(clone, "machines", "alice", "desk", "snapshot.yaml")
	if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--initial-branch=main"}, {"remote", "add", "origin", "git@github.com:acme/wb-state.git"}, {"add", "."}, {"commit", "-m", "snapshot"}} {
		command := exec.CommandContext(t.Context(), "git", args...)
		command.Dir = clone
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	states := func() map[string]string {
		found := map[string]string{}
		_ = filepath.WalkDir(clone, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr == nil {
				info, _ := entry.Info()
				found[path] = info.ModTime().String() + info.Mode().String() + strconv.FormatInt(info.Size(), 10)
			}
			return nil
		})
		return found
	}
	before := states()
	configPath := cockpitConfigFile(t, "remote:\n  provider: git\n  repo: acme/wb-state\n  machine: laptop-1\n")()
	options := cockpitFleetOptions(root, t.TempDir(), configPath, wbconfig.DefaultCockpitConfig(), io.Discard, func() (string, error) { return "host", nil })
	snapshotter := cockpitfleet.New(options)
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	machines := func() []cockpitfleet.Machine {
		body, _ := snapshotter.Body()
		var document cockpitfleet.Document
		if err := json.Unmarshal(body, &document); err != nil {
			t.Fatal(err)
		}
		return document.Machines
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(machines()) != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the other machine was not read: %+v", machines())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if desk := machines()[0]; desk.Machine != "desk" || desk.Route != cockpitfleet.RouteCached || !desk.ObservedAt.Equal(published) {
		t.Errorf("machines = %+v", machines())
	}
	after := states()
	for path, state := range before {
		if after[path] != state {
			t.Errorf("%s changed during a snapshot", path)
		}
	}
	if len(after) != len(before) {
		t.Errorf("%d files appeared under the state clone", len(after)-len(before))
	}
}

// TestCockpitRegisterFleetServesTheWarmingDocumentBeforeTheFirstSnapshot
// registers the fleet routes on a server and requests them before the
// snapshotter has started: an empty warming-up document and a 401 for the
// README without a session.
func TestCockpitRegisterFleetServesTheWarmingDocumentBeforeTheFirstSnapshot(t *testing.T) {
	t.Parallel()
	const address = "127.0.0.1:8766"
	server := newCockpitServer(address, wbconfig.DefaultCockpitConfig())
	snapshotter := registerCockpitFleet(server, cockpitfleet.Options{})
	api := server.Mounts()[cockpit.APIPrefix]
	for target, want := range map[string]int{"/api/v1/cockpit/fleet": http.StatusOK, cockpitfleet.ReadmePath + "?repository=x": http.StatusUnauthorized} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Host = address
		recorder := httptest.NewRecorder()
		api.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Errorf("%s = %d %s, want %d", target, recorder.Code, recorder.Body.String(), want)
		}
	}
	if body, _ := snapshotter.Body(); !strings.Contains(string(body), `"warming_up":true`) {
		t.Error("a snapshotter that has not started is not warming up")
	}
}

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
	deps.hubConfigPath = cockpitConfigFile(t, "cockpit:\n  hosted_url: not-a-url\n")
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	err := serveDashboard(&invocation{projectsRoot: root}, command, deps, freeLoopbackAddress(t), daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}, "owner-token", true, false)
	if err == nil || !strings.Contains(err.Error(), "cockpit configuration") || !strings.Contains(err.Error(), "cockpit.hosted_url") {
		t.Fatalf("serveDashboard = %v, want an error naming the cockpit section", err)
	}
}

// TestCockpitServerKeepsSessionsInMemorySoARestartEndsThem pins what the
// daemon builds: a session established on one run's server is unknown to the
// next run's (cockpit#ac:session-ends-on-logout-and-restart).
func TestCockpitServerKeepsSessionsInMemorySoARestartEndsThem(t *testing.T) {
	t.Parallel()
	const address = "127.0.0.1:8766"
	session := func(server *cockpit.Server, cookie *http.Cookie) string {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/cockpit/session", nil)
		request.Host = address
		if cookie != nil {
			request.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		server.Mounts()[cockpit.APIPrefix].ServeHTTP(recorder, request)
		return recorder.Body.String()
	}
	config := wbconfig.DefaultCockpitConfig()
	first := newCockpitServer(address, config)
	issued, err := first.MintLoginCode()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, cockpit.LoginPath+"?code="+issued.Code, nil)
	request.Host = address
	recorder := httptest.NewRecorder()
	first.Mounts()[cockpit.PagePrefix].ServeHTTP(recorder, request)
	cookies := recorder.Result().Cookies()
	if recorder.Code != http.StatusSeeOther || len(cookies) != 1 {
		t.Fatalf("login = %d with %d cookies", recorder.Code, len(cookies))
	}
	if body := session(first, cookies[0]); !strings.Contains(body, `"principal":"owner"`) {
		t.Fatalf("session on the run that set it = %q", body)
	}
	restarted := newCockpitServer(address, config)
	if body := session(restarted, cookies[0]); !strings.Contains(body, `"principal":"anonymous-local"`) || strings.Contains(body, "repo.content.read") {
		t.Fatalf("session after a restart = %q, want anonymous-local", body)
	}
}

// TestCockpitLoginCodeRouteIsRefusedByTheFileBridge pins that the file
// bridge, which dispatches into the same mux as the unix socket, forwards
// only the DaemonService procedures it lists.
func TestCockpitLoginCodeRouteIsRefusedByTheFileBridge(t *testing.T) {
	t.Parallel()
	for _, procedure := range []string{cockpit.LoginCodeRPCPath, cockpit.LoginCodeRPCPath + "/", peersRPCPrefix + "invite"} {
		if _, _, err := daemonFilePrepareRequest(procedure, nil, "request-id"); err == nil || !strings.Contains(err.Error(), "refused an unknown RPC procedure") {
			t.Errorf("the file bridge prepared %s: %v", procedure, err)
		}
	}
}
