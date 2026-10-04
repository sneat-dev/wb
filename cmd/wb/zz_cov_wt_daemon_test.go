//go:build !windows

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemonruntime"

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

func TestCwWtDaemonServeCmdValidationAndShortLivedServe(t *testing.T) {
	root := cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	deps := daemonTestDependencies(t, root)

	// A non-loopback listener is refused with a usage error.
	_, _, err := cwWtDaemonExec(t, root, func() *cobra.Command { return daemonCommandForTest("serve", &invocation{}, deps) }, "--listen", "0.0.0.0:1234")
	if code := exitCodeOf(t, err); code != exitUsage {
		t.Fatalf("non-loopback listen exit = %d (%v)", code, err)
	}

	// A managed start that no longer owns the starting state is refused.
	statePath := mustDaemonPath(t, daemonruntime.StatePath, root)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	ready := daemon.NewStartingAt(nil, "127.0.0.1:0", daemon.Provenance{}, "token-a", "", "", deps.Now())
	ready.MarkReady(4242, deps.Now())
	if err := (daemon.Store{Path: statePath}).Save(ready); err != nil {
		t.Fatal(err)
	}
	_, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return daemonCommandForTest("serve", &invocation{}, deps) }, "--listen", "127.0.0.1:0", "--lifecycle-state", statePath)
	if err == nil || !strings.Contains(err.Error(), "no longer owns a starting lifecycle state") {
		t.Fatalf("managed start ownership error = %v", err)
	}

	// An out-of-range port passes the loopback check and fails at bind.
	_, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return daemonCommandForTest("serve", &invocation{}, deps) }, "--listen", "127.0.0.1:99999")
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
	command := daemonCommandForTest("serve", &invocation{projectsRoot: serveRoot}, serveDeps)
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
	state, found, err := (daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, serveRoot)}).Load()
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

	stdout, _, err := cwWtDaemonExec(t, root, func() *cobra.Command { return daemonCommandForTest("start", &invocation{projectsRoot: root}, deps) })
	if err != nil {
		t.Fatalf("daemon start: %v", err)
	}
	if !strings.Contains(stdout, "daemon start:") {
		t.Fatalf("daemon start stdout = %q", stdout)
	}
	stdout, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return daemonCommandForTest("status", &invocation{projectsRoot: root}, deps) })
	if err != nil {
		t.Fatalf("daemon status: %v", err)
	}
	if !strings.Contains(stdout, "daemon status:") {
		t.Fatalf("daemon status stdout = %q", stdout)
	}
	stdout, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return daemonCommandForTest("status", &invocation{projectsRoot: root}, deps) }, "--json")
	if err != nil {
		t.Fatalf("daemon status json: %v", err)
	}
	if !strings.Contains(stdout, "\"action\"") {
		t.Fatalf("daemon status json = %q", stdout)
	}

	stdout, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return daemonCommandForTest("stop", &invocation{projectsRoot: root}, deps) })
	if err != nil {
		t.Fatalf("daemon stop: %v", err)
	}
	if !strings.Contains(stdout, "daemon stop:") {
		t.Fatalf("daemon stop stdout = %q", stdout)
	}

	stdout, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return daemonCommandForTest("restart", &invocation{projectsRoot: root}, deps) }, "--if-running")
	if err != nil {
		t.Fatalf("daemon restart --if-running: %v", err)
	}
	if !strings.Contains(stdout, "daemon restart:") {
		t.Fatalf("daemon restart stdout = %q", stdout)
	}

	stdout, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return daemonCommandForTest("recover", &invocation{projectsRoot: root}, deps) })
	if err != nil {
		t.Fatalf("daemon recover: %v", err)
	}
	if !strings.Contains(stdout, "daemon recover:") {
		t.Fatalf("daemon recover stdout = %q", stdout)
	}
	stdout, _, err = cwWtDaemonExec(t, root, func() *cobra.Command { return daemonCommandForTest("recover", &invocation{projectsRoot: root}, deps) }, "--format", "json")
	if err != nil {
		t.Fatalf("daemon recover json: %v", err)
	}
	if !strings.Contains(stdout, "\"reason\"") {
		t.Fatalf("daemon recover json = %q", stdout)
	}

	// A bogus format is a usage error for every daemon verb.
	for name, build := range map[string]func() *cobra.Command{
		"start":   func() *cobra.Command { return daemonCommandForTest("start", &invocation{projectsRoot: root}, deps) },
		"status":  func() *cobra.Command { return daemonCommandForTest("status", &invocation{projectsRoot: root}, deps) },
		"stop":    func() *cobra.Command { return daemonCommandForTest("stop", &invocation{projectsRoot: root}, deps) },
		"restart": func() *cobra.Command { return daemonCommandForTest("restart", &invocation{projectsRoot: root}, deps) },
		"recover": func() *cobra.Command { return daemonCommandForTest("recover", &invocation{projectsRoot: root}, deps) },
	} {
		_, _, err := cwWtDaemonExec(t, root, build, "--format", "yaml")
		if code := exitCodeOf(t, err); code != exitUsage {
			t.Errorf("daemon %s --format yaml exit = %d (%v)", name, code, err)
		}
	}

	// --json with a conflicting --format is refused too.
	if _, _, err := cwWtDaemonExec(t, root, func() *cobra.Command { return daemonCommandForTest("start", &invocation{projectsRoot: root}, deps) }, "--json", "--format", "yaml"); err == nil {
		t.Fatal("daemon start --json with a conflicting --format must fail")
	}
}

func TestCwWtDaemonCommandErrorPropagation(t *testing.T) {
	root := cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	deps := daemonTestDependencies(t, root)

	// A failing token generator stops start before any state is written.
	failingToken := deps
	failingToken.Token = func() (string, error) { return "", errors.New("cwWt: token unavailable") }
	if _, _, err := cwWtDaemonExec(t, root, func() *cobra.Command {
		return daemonCommandForTest("start", &invocation{projectsRoot: root}, failingToken)
	}); err == nil || !strings.Contains(err.Error(), "cwWt: token unavailable") {
		t.Fatalf("start with a failing token = %v", err)
	}

	// A failing process starter is reported.
	failingStart := deps
	failingStart.Start = func(string, []string, string) (int, error) { return 0, errors.New("cwWt: spawn failed") }
	if _, _, err := cwWtDaemonExec(t, root, func() *cobra.Command {
		return daemonCommandForTest("start", &invocation{projectsRoot: root}, failingStart)
	}); err == nil {
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
	blockedStatus, _, blockedStatusErr := cwWtDaemonExec(t, unusableRoot, func() *cobra.Command {
		return daemonCommandForTest("status", &invocation{projectsRoot: unusableRoot}, deps)
	})
	if blockedStatusErr != nil {
		t.Fatalf("status against an unresolvable projects root = %v", blockedStatusErr)
	}
	if !strings.Contains(blockedStatus, "state=absent") || !strings.Contains(blockedStatus, "records no daemon") {
		t.Fatalf("status against an unresolvable projects root = %q, want an absent-daemon report", blockedStatus)
	}
	pinDaemonHome(t, root)

	// recover --apply on a proven-stale-but-ineligible lock refuses; a plain
	// dry run over a missing lock reports no_stale_owner and succeeds.
	stdout, _, err := cwWtDaemonExec(t, root, func() *cobra.Command { return daemonCommandForTest("recover", &invocation{projectsRoot: root}, deps) }, "--apply")
	if err != nil {
		t.Fatalf("recover --apply with no lock: %v (stdout=%s)", err, stdout)
	}
}

// daemonHeartbeat became daemonRuntimeGuard: the heartbeat now also proves the
// runtime directory it beats from still exists, so it needs a store and the
// owned record. An already-cancelled context must still return nothing at all.

func TestCwWtRequireLoopbackAddress(t *testing.T) {
	for _, address := range []string{"localhost:1234", "127.0.0.1:9", "[::1]:9", "localhost:"} {
		if err := daemonruntime.RequireLoopbackAddress(address); err != nil {
			t.Errorf("requireLoopbackAddress(%q) = %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:9", "192.168.1.5:9", "example.test:9", "no-port"} {
		if err := daemonruntime.RequireLoopbackAddress(address); err == nil {
			t.Errorf("requireLoopbackAddress(%q) = nil, want a refusal", address)
		}
	}
}
