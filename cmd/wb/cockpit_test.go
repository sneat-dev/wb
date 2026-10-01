package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/daemon"
)

func cockpitTestDependencies(t *testing.T, opened *[]string, mints *int) cockpitCommandDependencies {
	t.Helper()
	return cockpitCommandDependencies{
		open:       func(target string) error { *opened = append(*opened, target); return nil },
		isTerminal: func(any) bool { return true },
		configPath: func() string { return filepath.Join(t.TempDir(), "wb.yaml") },
		local: func(_ context.Context, _ daemonDependencies, _, _ string, mint bool) (cockpitLocalSession, error) {
			session := cockpitLocalSession{Listen: "127.0.0.1:8766"}
			if mint {
				*mints++
				session.Code, session.Path = "abc123", cockpit.LoginPath
			}
			return session, nil
		},
	}
}

func runCockpit(t *testing.T, inv *invocation, deps cockpitCommandDependencies, args ...string) (string, string, error) {
	t.Helper()
	command := newCockpitCmdWithDependencies(inv, deps)
	command.SetArgs(args)
	command.SilenceUsage, command.SilenceErrors = true, true
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

func TestCockpitLocalOpensTheLoginURL(t *testing.T) {
	var opened []string
	var mints int
	stdout, _, err := runCockpit(t, &invocation{projectsRoot: "/root"}, cockpitTestDependencies(t, &opened, &mints))
	if err != nil {
		t.Fatal(err)
	}
	want := "http://127.0.0.1:8766/cockpit/session/login?code=abc123"
	if len(opened) != 1 || opened[0] != want || mints != 1 || stdout != "cockpit: "+want+"\n" {
		t.Fatalf("opened = %v, mints = %d, stdout = %q", opened, mints, stdout)
	}
}

func TestCockpitPrintsAStartWarningToStderr(t *testing.T) {
	var opened []string
	var mints int
	deps := cockpitTestDependencies(t, &opened, &mints)
	inner := deps.local
	deps.local = func(ctx context.Context, d daemonDependencies, root, listen string, mint bool) (cockpitLocalSession, error) {
		session, err := inner(ctx, d, root, listen, mint)
		session.Warning = "provenance mismatch"
		return session, err
	}
	_, stderr, err := runCockpit(t, &invocation{}, deps)
	if err != nil || !strings.Contains(stderr, "provenance mismatch") {
		t.Fatalf("err = %v, stderr = %q", err, stderr)
	}
}

func TestCockpitBrowserOpensOnlyForAnInteractiveTerminal(t *testing.T) {
	for name, test := range map[string]struct {
		inv      *invocation
		terminal bool
		args     []string
		want     int
	}{
		"terminal and interactive":    {&invocation{}, true, nil, 1},
		"stdout is not a terminal":    {&invocation{}, false, nil, 0},
		"non-interactive":             {&invocation{nonInteractive: true}, true, nil, 0},
		"hosted terminal interactive": {&invocation{}, true, []string{"--hosted"}, 1},
		"hosted non-interactive":      {&invocation{nonInteractive: true}, true, []string{"--hosted"}, 0},
		"hosted stdout is not a tty":  {&invocation{}, false, []string{"--hosted"}, 0},
	} {
		var opened []string
		var mints int
		deps := cockpitTestDependencies(t, &opened, &mints)
		deps.isTerminal = func(any) bool { return test.terminal }
		if len(test.args) > 0 {
			deps.local = func(context.Context, daemonDependencies, string, string, bool) (cockpitLocalSession, error) {
				return cockpitLocalSession{}, errors.New("unexpected daemon contact")
			}
		}
		stdout, _, err := runCockpit(t, test.inv, deps, test.args...)
		if err != nil || len(opened) != test.want || !strings.HasPrefix(stdout, "cockpit: http") {
			t.Fatalf("%s: err = %v, opened = %v, stdout = %q", name, err, opened, stdout)
		}
	}
}

func TestCockpitOpenFailureStillPrintsTheURL(t *testing.T) {
	var opened []string
	var mints int
	deps := cockpitTestDependencies(t, &opened, &mints)
	deps.open = func(string) error { return errors.New("no display") }
	stdout, stderr, err := runCockpit(t, &invocation{}, deps)
	if err != nil || !strings.HasPrefix(stdout, "cockpit: http://127.0.0.1:8766/cockpit/session/login?code=abc123") || !strings.Contains(stderr, "no display") {
		t.Fatalf("err = %v, stdout = %q, stderr = %q", err, stdout, stderr)
	}
}

func TestCockpitUsesTheCanonicalIPv6Origin(t *testing.T) {
	var opened []string
	var mints int
	deps := cockpitTestDependencies(t, &opened, &mints)
	deps.local = func(context.Context, daemonDependencies, string, string, bool) (cockpitLocalSession, error) {
		return cockpitLocalSession{Listen: "[::1]:9000", Code: "c", Path: cockpit.LoginPath}, nil
	}
	if _, _, err := runCockpit(t, &invocation{}, deps); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0] != "http://[::1]:9000/cockpit/session/login?code=c" {
		t.Fatalf("opened = %v", opened)
	}
}

func TestCockpitJSONCarriesNoCodeAndOpensNothing(t *testing.T) {
	for _, flag := range []string{"--format=json", "--json"} {
		var opened []string
		var mints int
		stdout, _, err := runCockpit(t, &invocation{}, cockpitTestDependencies(t, &opened, &mints), flag)
		if err != nil {
			t.Fatal(err)
		}
		var result cockpitOpenResult
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatal(err)
		}
		if len(opened) != 0 || mints != 0 || result.Opened || result.Scope != "local" || result.URL != "http://127.0.0.1:8766/cockpit/" ||
			strings.Contains(stdout, "?") || strings.Contains(stdout, "code") {
			t.Fatalf("opened = %v, mints = %d, stdout = %q", opened, mints, stdout)
		}
	}
}

func TestCockpitHostedUsesTheConfiguredURLAndStartsNoDaemon(t *testing.T) {
	var opened []string
	var mints int
	deps := cockpitTestDependencies(t, &opened, &mints)
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte("cockpit:\n  hosted_url: https://cockpit.example.test/wb/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps.configPath = func() string { return path }
	deps.local = func(context.Context, daemonDependencies, string, string, bool) (cockpitLocalSession, error) {
		return cockpitLocalSession{}, errors.New("unexpected daemon start")
	}
	stdout, _, err := runCockpit(t, &invocation{}, deps, "--hosted")
	if err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0] != "https://cockpit.example.test/wb/" || stdout != "cockpit: https://cockpit.example.test/wb/\n" {
		t.Fatalf("opened = %v, stdout = %q", opened, stdout)
	}
	stdout, _, err = runCockpit(t, &invocation{}, deps, "--hosted", "--json")
	var result cockpitOpenResult
	if err != nil || json.Unmarshal([]byte(stdout), &result) != nil {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}
	if result != (cockpitOpenResult{URL: "https://cockpit.example.test/wb/", Scope: "hosted"}) || len(opened) != 1 {
		t.Fatalf("result = %+v, opened = %v", result, opened)
	}
}

func TestCockpitHostedRejectsABadConfiguration(t *testing.T) {
	var opened []string
	var mints int
	deps := cockpitTestDependencies(t, &opened, &mints)
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte("cockpit:\n  hosted_url: not a url\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps.configPath = func() string { return path }
	stdout, _, err := runCockpit(t, &invocation{}, deps, "--hosted")
	if err == nil || !strings.Contains(err.Error(), "cockpit configuration") || stdout != "" || len(opened) != 0 {
		t.Fatalf("err = %v, stdout = %q, opened = %v", err, stdout, opened)
	}
}

func TestCockpitErrorsPrintNoPartialURL(t *testing.T) {
	var opened []string
	var mints int
	deps := cockpitTestDependencies(t, &opened, &mints)
	deps.local = func(context.Context, daemonDependencies, string, string, bool) (cockpitLocalSession, error) {
		return cockpitLocalSession{}, errors.New("daemon down")
	}
	stdout, _, err := runCockpit(t, &invocation{}, deps)
	if err == nil || !strings.Contains(err.Error(), "daemon down") || stdout != "" || len(opened) != 0 {
		t.Fatalf("err = %v, stdout = %q, opened = %v", err, stdout, opened)
	}
	deps.local = func(context.Context, daemonDependencies, string, string, bool) (cockpitLocalSession, error) {
		return cockpitLocalSession{Listen: "no-port"}, nil
	}
	stdout, _, err = runCockpit(t, &invocation{}, deps)
	if err == nil || !strings.Contains(err.Error(), "no-port") || stdout != "" {
		t.Fatalf("err = %v, stdout = %q", err, stdout)
	}
}

func TestCockpitRejectsAConflictingFormat(t *testing.T) {
	var opened []string
	var mints int
	_, _, err := runCockpit(t, &invocation{}, cockpitTestDependencies(t, &opened, &mints), "--json", "--format=yaml")
	if err == nil || !strings.Contains(err.Error(), "--json") || mints != 0 {
		t.Fatalf("err = %v", err)
	}
}

func TestCockpitFlagsAreExactlyTheCapabilityRowFlags(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "ai", "capabilities.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Capabilities []struct {
			Surfaces struct {
				Runtime struct {
					Commands []struct {
						Path  string   `json:"path"`
						Flags []string `json:"flags"`
					} `json:"commands"`
				} `json:"runtime"`
			} `json:"surfaces"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, capability := range manifest.Capabilities {
		for _, command := range capability.Surfaces.Runtime.Commands {
			if command.Path == "wb cockpit" {
				want = command.Flags
			}
		}
	}
	var got []string
	newCockpitCmd(&invocation{}).Flags().VisitAll(func(flag *pflag.Flag) { got = append(got, "--"+flag.Name) })
	if len(want) == 0 || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("command flags = %v, capability row flags = %v", got, want)
	}
}

func TestCockpitLocalFromDaemonStartsWithoutMintingForJSON(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.localClient = func(string, string) (*http.Client, error) { return nil, errors.New("must not connect") }
	session, err := cockpitLocalFromDaemon(context.Background(), deps, root, daemonDefaultListen, false)
	if err != nil || session.Listen == "" || session.Code != "" {
		t.Fatalf("session = %+v, err = %v", session, err)
	}
}

// TestCockpitLocalFromDaemonMintsOverTheOwnerChannel drives the command's real
// client path against the real login-code handler behind the daemon's own
// owner-token check. The test server stands in for the unix socket; nothing
// reaches a live daemon.
func TestCockpitLocalFromDaemonMintsOverTheOwnerChannel(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	// Start the fake daemon once to learn the stored owner token.
	controller := newDaemonController(deps, root)
	if _, err := controller.Start(context.Background(), daemonDefaultListen); err != nil {
		t.Fatal(err)
	}
	state, _, err := controller.store.Load()
	if err != nil || state.OwnerToken == "" {
		t.Fatalf("state = %+v, err = %v", state, err)
	}
	var presented []string
	server := cockpit.New(cockpit.Options{CanonicalHost: "127.0.0.1"})
	mux := http.NewServeMux()
	mux.Handle(cockpit.LoginCodeRPCPath, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		presented = append(presented, request.Header.Get("Authorization"))
		authenticatedDaemonHandler(state.OwnerToken, server.LoginCodeHandler()).ServeHTTP(writer, request)
	}))
	mux.Handle(cockpit.PagePrefix, server.Mounts()[cockpit.PagePrefix])
	test := httptest.NewServer(mux)
	defer test.Close()
	deps.localClient = func(_, ownerToken string) (*http.Client, error) {
		return &http.Client{Transport: daemonAuthenticatedTransport{token: ownerToken, base: cockpitRewriteTransport{target: test.URL}}}, nil
	}
	command := newCockpitCmdWithDependencies(&invocation{projectsRoot: root}, cockpitCommandDependencies{
		daemon: deps, isTerminal: func(any) bool { return false }, local: cockpitLocalFromDaemon,
	})
	command.SetArgs(nil)
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(presented) != 1 || presented[0] != "Bearer "+state.OwnerToken {
		t.Fatalf("presented = %v, want the stored owner token once", presented)
	}
	login, err := url.Parse(strings.TrimSpace(strings.TrimPrefix(stdout.String(), "cockpit: ")))
	if err != nil || login.Path != cockpit.LoginPath || login.Query().Get("code") == "" {
		t.Fatalf("stdout = %q, err = %v", stdout.String(), err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(test.URL + login.Path + "?code=" + login.Query().Get("code"))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || len(response.Cookies()) == 0 {
		t.Fatalf("login exchange = %s, cookies = %v", response.Status, response.Cookies())
	}
}

func TestCockpitLocalFromDaemonRefusesBadMintResponses(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	status, body := http.StatusInternalServerError, `{"error":"boom"}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(body))
	}))
	defer server.Close()
	deps.localClient = func(string, string) (*http.Client, error) {
		return &http.Client{Transport: cockpitRewriteTransport{target: server.URL}}, nil
	}
	if _, err := cockpitLocalFromDaemon(context.Background(), deps, root, daemonDefaultListen, true); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the daemon's refusal", err)
	}
	status, body = http.StatusOK, `{}`
	if _, err := cockpitLocalFromDaemon(context.Background(), deps, root, daemonDefaultListen, true); err == nil || !strings.Contains(err.Error(), "no code") {
		t.Fatalf("err = %v, want a no-code error", err)
	}
	status, body = http.StatusOK, `{"code":"c","path":"/elsewhere"}`
	if _, err := cockpitLocalFromDaemon(context.Background(), deps, root, daemonDefaultListen, true); err == nil || !strings.Contains(err.Error(), "/elsewhere") {
		t.Fatalf("err = %v, want a login-path error", err)
	}
	deps.localClient = func(string, string) (*http.Client, error) { return nil, errors.New("no socket") }
	if _, err := cockpitLocalFromDaemon(context.Background(), deps, root, daemonDefaultListen, true); err == nil || !strings.Contains(err.Error(), "no socket") {
		t.Fatalf("err = %v", err)
	}
}

func TestCockpitOwnerClientUsesTheDefaultLocalClient(t *testing.T) {
	deps := daemonTestDependencies(t, daemonTestRoot(t))
	deps.localClient = nil
	ready := func() (daemon.State, bool, error) { return daemon.State{Status: daemon.StatusReady}, true, nil }
	// Whether the platform has a unix-socket client or not, the call must
	// return rather than panic, and never dial anything here.
	_, _ = cockpitOwnerClient(daemonResult{ProcessManagerRunning: true}, ready, deps, t.TempDir())
}

func TestCockpitOwnerClientRefusesAnUnreadyDaemon(t *testing.T) {
	deps := daemonTestDependencies(t, daemonTestRoot(t))
	load := func(state daemon.State, found bool, err error) func() (daemon.State, bool, error) {
		return func() (daemon.State, bool, error) { return state, found, err }
	}
	for name, test := range map[string]struct {
		result daemonResult
		load   func() (daemon.State, bool, error)
	}{
		"load error":      {daemonResult{ProcessManagerRunning: true}, load(daemon.State{}, false, errors.New("unreadable"))},
		"no state":        {daemonResult{ProcessManagerRunning: true}, load(daemon.State{}, false, nil)},
		"not ready":       {daemonResult{ProcessManagerRunning: true}, load(daemon.State{}, true, nil)},
		"no process held": {daemonResult{}, load(daemon.State{Status: daemon.StatusReady}, true, nil)},
	} {
		if client, err := cockpitOwnerClient(test.result, test.load, deps, "/root"); err == nil || client != nil {
			t.Fatalf("%s: client = %v, err = %v", name, client, err)
		}
	}
}

func TestCockpitLocalFromDaemonReportsStartFailures(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.executable = func() (string, error) { return "", errors.New("no executable") }
	if _, err := cockpitLocalFromDaemon(context.Background(), deps, root, daemonDefaultListen, true); err == nil || !strings.Contains(err.Error(), "start local daemon") {
		t.Fatalf("err = %v", err)
	}
}

// cockpitRewriteTransport sends every request to a test server, standing in
// for the daemon's unix socket.
type cockpitRewriteTransport struct{ target string }

func (transport cockpitRewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme, clone.URL.Host = "http", strings.TrimPrefix(transport.target, "http://")
	return http.DefaultTransport.RoundTrip(clone)
}

type cockpitFailingWriter struct{}

func (cockpitFailingWriter) Write([]byte) (int, error) { return 0, errors.New("stdout closed") }

func TestCockpitReportsAStdoutWriteFailure(t *testing.T) {
	var opened []string
	var mints int
	command := newCockpitCmdWithDependencies(&invocation{}, cockpitTestDependencies(t, &opened, &mints))
	command.SilenceUsage, command.SilenceErrors = true, true
	command.SetOut(cockpitFailingWriter{})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "stdout closed") || len(opened) != 0 {
		t.Fatalf("err = %v, opened = %v", err, opened)
	}
}

func TestCockpitListenFlag(t *testing.T) {
	// Default unchanged: the stock address reaches the daemon start.
	var opened []string
	var mints int
	deps := cockpitTestDependencies(t, &opened, &mints)
	var gotListen string
	deps.local = func(_ context.Context, _ daemonDependencies, _, listen string, mint bool) (cockpitLocalSession, error) {
		gotListen = listen
		if listen == "" {
			listen = daemonDefaultListen
		}
		return cockpitLocalSession{Listen: listen, Code: map[bool]string{true: "c1"}[mint], Path: cockpit.LoginPath}, nil
	}
	// No flag: the command leaves the choice to the daemon lookup (empty), which defaults below.
	if _, _, err := runCockpit(t, &invocation{}, deps, "--json"); err != nil || gotListen != "" {
		t.Fatalf("unset listen = %q, err = %v", gotListen, err)
	}
	// A custom loopback address reaches Start and builds the printed URL.
	stdout, _, err := runCockpit(t, &invocation{}, deps, "--listen", "127.0.0.1:43211")
	if err != nil || gotListen != "127.0.0.1:43211" || stdout != "cockpit: http://127.0.0.1:43211/cockpit/session/login?code=c1\n" {
		t.Fatalf("listen = %q, stdout = %q, err = %v", gotListen, stdout, err)
	}
	// JSON output carries the right origin.
	stdout, _, err = runCockpit(t, &invocation{}, deps, "--listen=127.0.0.1:43211", "--json")
	var result cockpitOpenResult
	if jsonErr := json.Unmarshal([]byte(stdout), &result); err != nil || jsonErr != nil || result.URL != "http://127.0.0.1:43211/cockpit/" {
		t.Fatalf("stdout = %q, err = %v, %v", stdout, err, jsonErr)
	}
	// A non-loopback address is refused before anything starts.
	gotListen = ""
	deps.local = func(context.Context, daemonDependencies, string, string, bool) (cockpitLocalSession, error) {
		t.Fatal("daemon contacted for a non-loopback address")
		return cockpitLocalSession{}, nil
	}
	for _, bad := range []string{"0.0.0.0:9000", "example.com:80", "nope", ""} {
		if _, _, err := runCockpit(t, &invocation{}, deps, "--listen", bad); err == nil || !strings.Contains(err.Error(), "--listen") {
			t.Fatalf("%q: err = %v", bad, err)
		}
	}
}

func TestCockpitLocalFromDaemonStartsOnTheRequestedListen(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	session, err := cockpitLocalFromDaemon(context.Background(), deps, root, "127.0.0.1:43999", false)
	if err != nil || session.Listen != "127.0.0.1:43999" {
		t.Fatalf("session = %+v, err = %v", session, err)
	}
}

// startedDaemon starts a fake daemon on listen and returns the dependencies and
// a counter of further start calls.
func startedDaemon(t *testing.T, listen string) (daemonDependencies, string, *int) {
	t.Helper()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	if _, err := newDaemonController(deps, root).Start(context.Background(), listen); err != nil {
		t.Fatal(err)
	}
	starts := 0
	inner := deps.start
	deps.start = func(executable string, args []string, log string) (int, error) {
		starts++
		return inner(executable, args, log)
	}
	return deps, root, &starts
}

func TestCockpitNeverReplacesARunningDaemonOnAnotherAddress(t *testing.T) {
	deps, root, starts := startedDaemon(t, "127.0.0.1:43001")
	_, err := cockpitLocalFromDaemon(context.Background(), deps, root, "127.0.0.1:43002", false)
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:43001") || !strings.Contains(err.Error(), "--listen 127.0.0.1:43001") || *starts != 0 {
		t.Fatalf("err = %v, starts = %d", err, *starts)
	}
	if state, _, _ := newDaemonController(deps, root).store.Load(); state.Listen != "127.0.0.1:43001" {
		t.Fatalf("the running daemon's record changed: %+v", state)
	}
}

func TestCockpitReusesARunningDaemonWhereverItListens(t *testing.T) {
	deps, root, starts := startedDaemon(t, "127.0.0.1:43001")
	session, err := cockpitLocalFromDaemon(context.Background(), deps, root, "", false)
	if err != nil || session.Listen != "127.0.0.1:43001" || *starts != 0 {
		t.Fatalf("session = %+v, err = %v, starts = %d", session, err, *starts)
	}
	session, err = cockpitLocalFromDaemon(context.Background(), deps, root, "127.0.0.1:43001", false)
	if err != nil || session.Listen != "127.0.0.1:43001" || *starts != 0 {
		t.Fatalf("same address: session = %+v, err = %v, starts = %d", session, err, *starts)
	}
}

func TestCockpitStartsOnTheRequestedOrDefaultAddressWhenNothingRuns(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	session, err := cockpitLocalFromDaemon(context.Background(), deps, root, "", false)
	if err != nil || session.Listen != daemonDefaultListen {
		t.Fatalf("default: session = %+v, err = %v", session, err)
	}
	// The recorded daemon is no longer alive: a stale record does not block another address.
	state, _, err := newDaemonController(deps, root).store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.stop(state.PID, daemon.SupervisorNone, ""); err != nil {
		t.Fatal(err)
	}
	session, err = cockpitLocalFromDaemon(context.Background(), deps, root, "127.0.0.1:43003", false)
	if err != nil || session.Listen != "127.0.0.1:43003" {
		t.Fatalf("stale record: session = %+v, err = %v", session, err)
	}
}

func TestCockpitReportsAnUnreadableDaemonRecord(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	path := mustDaemonPath(t, daemonStatePath, root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cockpitLocalFromDaemon(context.Background(), deps, root, "", false); err == nil || !strings.Contains(err.Error(), "daemon record") {
		t.Fatalf("err = %v", err)
	}
}
