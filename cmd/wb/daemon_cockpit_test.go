package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/daemon"
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
	response, body = get(address, "/api/v1/cockpit/fleet")
	if response.StatusCode != http.StatusNotFound || !strings.Contains(body, `"error"`) {
		t.Fatalf("unknown cockpit api route = %s %q, want a JSON 404", response.Status, body)
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
