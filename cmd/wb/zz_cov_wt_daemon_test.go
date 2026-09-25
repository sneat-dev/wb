//go:build !windows

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/testenv"
)

// cwWtDaemonRoot makes a short temp root under /tmp: the daemon local
// transport is a unix socket whose path length is bounded, so t.TempDir()'s
// long path can exceed the platform limit.
func cwWtDaemonRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "wb-cwwt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	// The daemon now resolves its runtime directory through WB's home resolver,
	// whose default is the developer's real WB home. Pin it inside the fixture
	// or these tests would create sockets and lifecycle records outside it.
	pinDaemonHome(t, root)
	return root
}

// cwWtDaemonExec runs a daemon subcommand in-process against a fixture root.
func cwWtDaemonExec(t *testing.T, root string, build func() *cobra.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	testenv.Isolate(t)

	command := build()
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetContext(context.Background())
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs(args)
	err = command.Execute()
	return out.String(), errOut.String(), err
}

func TestCwWtDefaultDaemonDependenciesClosures(t *testing.T) {
	root := cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	deps := defaultDaemonDependencies()

	if got := deps.now(); got.IsZero() {
		t.Fatal("default now() returned the zero time")
	}
	if _, err := deps.executable(); err != nil {
		t.Fatalf("default executable(): %v", err)
	}
	if got := deps.version(); got.Version == "" {
		t.Fatal("default version() returned no version")
	}
	if token, err := deps.token(); err != nil || len(token) != 32 {
		t.Fatalf("default token() = (%q, %v)", token, err)
	}
	if pid := deps.alive(-1); pid {
		t.Fatal("a negative pid must not be reported alive")
	}
	if path := deps.hubConfigPath(); path == "" {
		t.Fatal("default hubConfigPath() returned an empty path")
	}

	// The restart ticker delivers and stops cleanly.
	ticks, stopTicker := deps.restartTicker(time.Millisecond)
	select {
	case <-ticks:
	case <-time.After(2 * time.Second):
		t.Fatal("restart ticker did not tick")
	}
	stopTicker()

	// The raw-execution policy closure resolves its path and loads the policy.
	allowed, path, err := deps.rawPolicy(root)
	if err != nil {
		t.Fatalf("default rawPolicy(): %v", err)
	}
	if allowed || path == "" {
		t.Fatalf("default rawPolicy() = (%t, %q)", allowed, path)
	}

	// A local HTTP client can be built for the fixture root.
	client, err := deps.localClient(root, "token")
	if err == nil {
		if client == nil {
			t.Fatal("default localClient() returned a nil client with no error")
		}
		client.CloseIdleConnections()
	}
}

func TestCwWtDaemonHealthyAndOwnedHealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/health":
			response.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(response, `{"daemon_pid":%d,"scheduler_generation":7}`, os.Getpid())
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	listen := strings.TrimPrefix(server.URL, "http://")

	if err := daemonHealthy(context.Background(), listen); err != nil {
		t.Fatalf("daemonHealthy: %v", err)
	}
	if err := daemonOwnedHealthy(context.Background(), listen, os.Getpid(), 7); err != nil {
		t.Fatalf("daemonOwnedHealthy: %v", err)
	}
	if err := daemonOwnedHealthy(context.Background(), listen, os.Getpid(), 8); err == nil {
		t.Fatal("a generation mismatch must fail")
	}
	if err := daemonOwnedHealthy(context.Background(), listen, os.Getpid()+1, 7); err == nil {
		t.Fatal("a pid mismatch must fail")
	}

	// A non-200 health endpoint is reported.
	bad := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "nope", http.StatusInternalServerError)
	}))
	defer bad.Close()
	badListen := strings.TrimPrefix(bad.URL, "http://")
	if err := daemonHealthy(context.Background(), badListen); err == nil || !strings.Contains(err.Error(), "health endpoint returned") {
		t.Fatalf("non-200 health = %v", err)
	}
	if err := daemonOwnedHealthy(context.Background(), badListen, 1, 1); err == nil {
		t.Fatal("owned health against a non-200 endpoint must fail")
	}

	// An undecodable body is reported.
	broken := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("not json"))
	}))
	defer broken.Close()
	if err := daemonOwnedHealthy(context.Background(), strings.TrimPrefix(broken.URL, "http://"), 1, 1); err == nil {
		t.Fatal("an undecodable health body must fail")
	}

	// A connection that cannot be established is reported.
	if err := daemonHealthy(context.Background(), "127.0.0.1:1"); err == nil {
		t.Fatal("an unreachable health endpoint must fail")
	}
	// An unparsable URL is reported by request construction.
	if err := daemonHealthy(context.Background(), "bad host:port"); err == nil {
		t.Fatal("an invalid health URL must fail")
	}
}

func TestCwWtDaemonServeCmdValidationAndShortLivedServe(t *testing.T) {
	root := cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	deps := daemonTestDependencies(t, root)

	// A non-loopback listener is refused with a usage error.
	_, _, err := cwWtDaemonExec(t, root, func() *cobra.Command { return newDaemonServeCmd(&invocation{}, deps) }, "--listen", "0.0.0.0:1234")
	if code := exitCodeOf(t, err); code != exitUsage {
		t.Fatalf("non-loopback listen exit = %d (%v)", code, err)
	}

	// A managed start that no longer owns the starting state is refused.
	statePath := mustDaemonPath(t, daemonStatePath, root)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	ready := daemon.NewStarting(nil, "127.0.0.1:0", daemon.Provenance{}, "token-a", deps.now())
	ready.MarkReady(4242, deps.now())
	if err := (daemon.Store{Path: statePath}).Save(ready); err != nil {
		t.Fatal(err)
	}
	_, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return newDaemonServeCmd(&invocation{}, deps) }, "--listen", "127.0.0.1:0", "--lifecycle-state", statePath)
	if err == nil || !strings.Contains(err.Error(), "no longer owns a starting lifecycle state") {
		t.Fatalf("managed start ownership error = %v", err)
	}

	// An out-of-range port passes the loopback check and fails at bind.
	_, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return newDaemonServeCmd(&invocation{}, deps) }, "--listen", "127.0.0.1:99999")
	if err == nil || !strings.Contains(err.Error(), "listen for WB daemon") {
		t.Fatalf("invalid listen port error = %v", err)
	}

	// A full, short-lived serve: bind an ephemeral loopback port, then cancel.
	serveRoot := cwWtDaemonRoot(t)
	serveDeps := daemonTestDependencies(t, serveRoot)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	command := newDaemonServeCmd(&invocation{projectsRoot: serveRoot}, serveDeps)
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetContext(ctx)
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs([]string{"--listen", "127.0.0.1:0"})
	if err := command.Execute(); err != nil {
		t.Fatalf("short-lived serve: %v (stderr=%s)", err, errOut.String())
	}

	// The serve wrote a ready lifecycle state and then reconciled it stopped.
	state, found, err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, serveRoot)}).Load()
	if err != nil || !found {
		t.Fatalf("lifecycle state after serve: found=%t err=%v", found, err)
	}
	if state.OwnerToken == "" {
		t.Fatal("the serve did not record an owner token")
	}
}

func TestCwWtDaemonStartStatusStopRestartRecoverInProcess(t *testing.T) {
	root := cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	deps := daemonTestDependencies(t, root)

	stdout, _, err := cwWtDaemonExec(t, root, func() *cobra.Command { return newDaemonStartCmd(&invocation{projectsRoot: root}, deps) })
	if err != nil {
		t.Fatalf("daemon start: %v", err)
	}
	if !strings.Contains(stdout, "daemon start:") {
		t.Fatalf("daemon start stdout = %q", stdout)
	}
	stdout, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return newDaemonStatusCmd(&invocation{projectsRoot: root}, deps) })
	if err != nil {
		t.Fatalf("daemon status: %v", err)
	}
	if !strings.Contains(stdout, "daemon status:") {
		t.Fatalf("daemon status stdout = %q", stdout)
	}
	stdout, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return newDaemonStatusCmd(&invocation{projectsRoot: root}, deps) }, "--json")
	if err != nil {
		t.Fatalf("daemon status json: %v", err)
	}
	if !strings.Contains(stdout, "\"action\"") {
		t.Fatalf("daemon status json = %q", stdout)
	}

	stdout, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return newDaemonStopCmd(&invocation{projectsRoot: root}, deps) })
	if err != nil {
		t.Fatalf("daemon stop: %v", err)
	}
	if !strings.Contains(stdout, "daemon stop:") {
		t.Fatalf("daemon stop stdout = %q", stdout)
	}

	stdout, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return newDaemonRestartCmd(&invocation{projectsRoot: root}, deps) }, "--if-running")
	if err != nil {
		t.Fatalf("daemon restart --if-running: %v", err)
	}
	if !strings.Contains(stdout, "daemon restart:") {
		t.Fatalf("daemon restart stdout = %q", stdout)
	}

	stdout, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return newDaemonRecoverCmd(&invocation{projectsRoot: root}, deps) })
	if err != nil {
		t.Fatalf("daemon recover: %v", err)
	}
	if !strings.Contains(stdout, "daemon recover:") {
		t.Fatalf("daemon recover stdout = %q", stdout)
	}
	stdout, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return newDaemonRecoverCmd(&invocation{projectsRoot: root}, deps) }, "--format", "json")
	if err != nil {
		t.Fatalf("daemon recover json: %v", err)
	}
	if !strings.Contains(stdout, "\"reason\"") {
		t.Fatalf("daemon recover json = %q", stdout)
	}

	// A bogus format is a usage error for every daemon verb.
	for name, build := range map[string]func() *cobra.Command{
		"start":   func() *cobra.Command { return newDaemonStartCmd(&invocation{projectsRoot: root}, deps) },
		"status":  func() *cobra.Command { return newDaemonStatusCmd(&invocation{projectsRoot: root}, deps) },
		"stop":    func() *cobra.Command { return newDaemonStopCmd(&invocation{projectsRoot: root}, deps) },
		"restart": func() *cobra.Command { return newDaemonRestartCmd(&invocation{projectsRoot: root}, deps) },
		"recover": func() *cobra.Command { return newDaemonRecoverCmd(&invocation{projectsRoot: root}, deps) },
	} {
		_, _, err := cwWtDaemonExec(t, root, build, "--format", "yaml")
		if code := exitCodeOf(t, err); code != exitUsage {
			t.Errorf("daemon %s --format yaml exit = %d (%v)", name, code, err)
		}
	}

	// --json with a conflicting --format is refused too.
	if _, _, err := cwWtDaemonExec(t, root, func() *cobra.Command { return newDaemonStartCmd(&invocation{projectsRoot: root}, deps) }, "--json", "--format", "yaml"); err == nil {
		t.Fatal("daemon start --json with a conflicting --format must fail")
	}
}

func TestCwWtDaemonCommandErrorPropagation(t *testing.T) {
	root := cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	deps := daemonTestDependencies(t, root)

	// A failing token generator stops start before any state is written.
	failingToken := deps
	failingToken.token = func() (string, error) { return "", errors.New("cwWt: token unavailable") }
	if _, _, err := cwWtDaemonExec(t, root, func() *cobra.Command { return newDaemonStartCmd(&invocation{projectsRoot: root}, failingToken) }); err == nil || !strings.Contains(err.Error(), "cwWt: token unavailable") {
		t.Fatalf("start with a failing token = %v", err)
	}

	// A failing process starter is reported.
	failingStart := deps
	failingStart.start = func(string, []string, string) (int, error) { return 0, errors.New("cwWt: spawn failed") }
	if _, _, err := cwWtDaemonExec(t, root, func() *cobra.Command { return newDaemonStartCmd(&invocation{projectsRoot: root}, failingStart) }); err == nil {
		t.Fatal("start with a failing spawn must fail")
	}

	// A status probe that cannot read the lifecycle state is reported. The
	// daemon derives its runtime directory from the projects root's state home
	// now, so the unusable thing has to be a root that cannot be resolved: a
	// root beneath a regular file is not merely absent.
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A status probe against an unresolvable projects root reports rather than
	// fails: Status deliberately treats a location it cannot resolve as one
	// more thing to report instead of a hard error.
	unusableRoot := filepath.Join(blocker, "projects")
	pinDaemonHome(t, unusableRoot)
	blockedStatus, _, blockedStatusErr := cwWtDaemonExec(t, unusableRoot, func() *cobra.Command { return newDaemonStatusCmd(&invocation{projectsRoot: root}, deps) })
	if blockedStatusErr != nil {
		t.Fatalf("status against an unresolvable projects root = %v", blockedStatusErr)
	}
	if !strings.Contains(blockedStatus, "state=absent") || !strings.Contains(blockedStatus, "records no daemon") {
		t.Fatalf("status against an unresolvable projects root = %q, want an absent-daemon report", blockedStatus)
	}
	pinDaemonHome(t, root)

	// recover --apply on a proven-stale-but-ineligible lock refuses; a plain
	// dry run over a missing lock reports no_stale_owner and succeeds.
	stdout, _, err := cwWtDaemonExec(t, root, func() *cobra.Command { return newDaemonRecoverCmd(&invocation{projectsRoot: root}, deps) }, "--apply")
	if err != nil {
		t.Fatalf("recover --apply with no lock: %v (stdout=%s)", err, stdout)
	}
}

// daemonHeartbeat became daemonRuntimeGuard: the heartbeat now also proves the
// runtime directory it beats from still exists, so it needs a store and the
// owned record. An already-cancelled context must still return nothing at all.
func TestCwWtDaemonRuntimeGuardStopsOnCancellation(t *testing.T) {
	root := cwWtDaemonRoot(t)
	store := daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}
	owned := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "cwWt", Version: "cwWt"}, "cw-wt-token", time.Now().UTC())

	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	var guardErr error
	go func() {
		defer close(done)
		guardErr = daemonRuntimeGuard(&out, ctx, "127.0.0.1:0", store, owned, "cw-wt-token")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemonRuntimeGuard did not return after cancellation")
	}
	if guardErr != nil {
		t.Fatalf("daemonRuntimeGuard after cancellation = %v", guardErr)
	}
	if out.Len() != 0 {
		t.Fatalf("daemonRuntimeGuard wrote %q after immediate cancellation", out.String())
	}
}

func TestCwWtRequireLoopbackAddress(t *testing.T) {
	for _, address := range []string{"localhost:1234", "127.0.0.1:9", "[::1]:9", "localhost:"} {
		if err := requireLoopbackAddress(address); err != nil {
			t.Errorf("requireLoopbackAddress(%q) = %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:9", "192.168.1.5:9", "example.test:9", "no-port"} {
		if err := requireLoopbackAddress(address); err == nil {
			t.Errorf("requireLoopbackAddress(%q) = nil, want a refusal", address)
		}
	}
}
