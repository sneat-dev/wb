package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/daemonview"

	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestDaemonRequiresLoopbackListener(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8766", "localhost:8766", "[::1]:8766", "127.0.0.2:8766", "[0:0:0:0:0:0:0:1]:8766"} {
		if err := daemonruntime.RequireLoopbackAddress(address); err != nil {
			t.Errorf("%s rejected: %v", address, err)
		}
	}
	for _, address := range []string{"app.localhost:8766", "localhost.:8766", ":8766", "0.0.0.0:8766", "192.0.2.10:8766", "bad"} {
		if err := daemonruntime.RequireLoopbackAddress(address); err == nil {
			t.Errorf("%s accepted", address)
		}
	}
}

// TestDaemonServesOnlyWhatIsBoundToLoopback: whatever name --listen used, the
// address the listener really holds must be a TCP address on the loopback
// interface; a name that resolved elsewhere, and a non-TCP address, are a usage
// error naming the address.

func TestDaemonStatusWorksWithoutDaemonAndSupportsJSONShortcut(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	projectsRoot := root
	command := daemonCommandForTest("status", &invocation{projectsRoot: projectsRoot}, deps)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"--json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var result daemonResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, output.String())
	}
	if strings.Contains(output.String(), "owner_token") {
		t.Fatalf("private owner token leaked in status JSON: %s", output.String())
	}
	if result.Managed || result.Action != "status" {
		t.Fatalf("status result = %#v", result)
	}
}

func TestDaemonRecoverReturnsJSONForActiveTransitionAndApplyRefuses(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	entered, releaseStart := make(chan struct{}), make(chan struct{})
	startErr := errors.New("private fixture start released")
	deps.Start = func(string, []string, string) (int, error) { close(entered); <-releaseStart; return 0, startErr }
	controller := newDaemonController(deps, root)
	finished := make(chan error, 1)
	go func() { _, err := controller.Start(context.Background(), daemonruntime.DefaultListen); finished <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("native start never held lifecycle transition")
	}
	defer func() {
		close(releaseStart)
		if err := <-finished; !errors.Is(err, startErr) {
			t.Errorf("released start = %v", err)
		}
	}()

	for _, test := range []struct {
		args    []string
		wantErr bool
	}{{args: []string{"--json"}}, {args: []string{"--apply", "--json"}, wantErr: true}} {
		command := daemonCommandForTest("recover", &invocation{projectsRoot: root}, deps)
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(io.Discard)
		command.SetArgs(test.args)
		err := command.Execute()
		if (err != nil) != test.wantErr {
			t.Fatalf("args %v error = %v", test.args, err)
		}
		var result daemonRecoveryResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatalf("args %v output is not JSON: %v; output=%s", test.args, err, output.String())
		}
		if result.Eligible || result.Reason != "active_transition" {
			t.Fatalf("args %v result = %#v", test.args, result)
		}
	}
}

func TestDaemonStatusSeparatesReadyStateFromFailedAPIProbe(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.Alive = func(pid int) bool { return pid == 900 }
	deps.Health = func(context.Context, string) error { return errors.New("connect: operation not permitted") }
	deps.BridgeHealth = func(context.Context, string, string) error { return errors.New("bridge unavailable") }
	controller := newDaemonController(deps, root)
	// The subject here is the separation of a ready record from a failing API
	// probe, so the record must match this build: a provenance mismatch is its
	// own condition and would make the reported state unverified.
	current, err := controller.Provenance()
	if err != nil {
		t.Fatal(err)
	}
	state := daemonTestState(t, root, daemonruntime.DefaultListen, current, "owner", time.Now())
	state.MarkReady(900, time.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}

	result, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Status != daemon.StatusReady || !result.ProcessManagerRunning || result.Reachable {
		t.Fatalf("ready daemon with blocked probe = %#v", result)
	}
	if !strings.Contains(result.DirectTransportError, "operation not permitted") || !strings.Contains(result.ReachabilityError, "bridge unavailable") {
		t.Fatalf("probe errors = direct %q, effective %q", result.DirectTransportError, result.ReachabilityError)
	}
	var textOutput bytes.Buffer
	if err := daemonview.Result(&textOutput, "text", result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"state=ready", "process_manager_running=true", "api_reachable=false", "direct_transport_reachable=false", `direct_transport_error="connect: operation not permitted"`, `api_probe_error="protected file bridge: bridge unavailable"`} {
		if !strings.Contains(textOutput.String(), want) {
			t.Fatalf("text status %q does not contain %q", textOutput.String(), want)
		}
	}
	var jsonOutput bytes.Buffer
	if err := daemonview.Result(&jsonOutput, "json", result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"process_manager_running": true`, `"reachable": false`, `"direct_transport_reachable": false`, `"direct_transport_error": "connect: operation not permitted"`, `"reachability_error": "protected file bridge: bridge unavailable"`, `"status": "ready"`} {
		if !strings.Contains(jsonOutput.String(), want) {
			t.Fatalf("JSON status %q does not contain %q", jsonOutput.String(), want)
		}
	}
	stored, found, err := (daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}).Load()
	if err != nil || !found || stored.Status != daemon.StatusReady || stored.Queue.Generation != 1 {
		t.Fatalf("probe failure mutated lifecycle state = %#v, %t, %v", stored, found, err)
	}
}

func TestDaemonStatusUsesAuthenticatedFileBridgeAfterDirectTransportDenial(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, daemonruntime.DefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner", time.Now())
	state.MarkReady(901, time.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == 901 }
	deps.Health = func(context.Context, string) error { return syscall.EPERM }
	var probedRoot, probedGeneration string
	deps.BridgeHealth = func(_ context.Context, root, generation string) error {
		probedRoot, probedGeneration = root, generation
		return nil
	}

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reachable || result.DirectTransportReachable || result.ReachabilityTransport != "file_bridge" || !strings.Contains(result.DirectTransportError, "operation not permitted") || result.ReachabilityError != "" {
		t.Fatalf("file-bridge status = %#v", result)
	}
	if probedRoot != root || probedGeneration != "1" {
		t.Fatalf("bridge probe = root %q generation %q", probedRoot, probedGeneration)
	}
	var output bytes.Buffer
	if err := daemonview.Result(&output, "text", result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"api_reachable=true", "direct_transport_reachable=false", "api_transport=file_bridge", "direct_transport_error="} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("text status %q does not contain %q", output.String(), want)
		}
	}
	output.Reset()
	if err := daemonview.Result(&output, "json", result); err != nil {
		t.Fatal(err)
	}
	var decoded daemonResult
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("bridge status JSON = %v; output=%s", err, output.String())
	}
	if !decoded.Reachable || decoded.DirectTransportReachable || decoded.ReachabilityTransport != "file_bridge" || !strings.Contains(decoded.DirectTransportError, "operation not permitted") {
		t.Fatalf("decoded bridge status = %#v", decoded)
	}
}

func TestDaemonRestartReportsPhasesAndKeepsJSONStdoutClean(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			root := daemonTestRoot(t)
			deps := daemonTestDependencies(t, root)
			if _, err := newDaemonController(deps, root).Start(context.Background(), daemonruntime.DefaultListen); err != nil {
				t.Fatal(err)
			}
			projectsRoot := root
			command := daemonCommandForTest("restart", &invocation{projectsRoot: projectsRoot}, deps)
			var stdout, stderr bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			command.SetArgs([]string{"--format", format})
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"draining daemon pid", "starting replacement daemon"} {
				if !strings.Contains(stderr.String(), want) {
					t.Fatalf("%s progress %q does not contain %q", format, stderr.String(), want)
				}
			}
			if format == "json" {
				var result daemonResult
				if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Action != "restart" {
					t.Fatalf("JSON restart = %#v, %v; output=%s", result, err, stdout.String())
				}
				return
			}
			if !strings.Contains(stdout.String(), "daemon restart:") {
				t.Fatalf("text result = %q", stdout.String())
			}
		})
	}
}

func daemonTestDependencies(t *testing.T, root string) daemonDependencies {
	t.Helper()
	executable := filepath.Join(root, "wb")
	if err := testenv.WriteExecutableFile(executable, []byte("old installed binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{}
	pid := 900
	deps := daemonDependencies{Dependencies: daemonruntime.Dependencies{UsageError: usageError, GuardTicker: func(interval time.Duration) (<-chan time.Time, func()) {
		ticker := time.NewTicker(interval)
		return ticker.C, ticker.Stop
	},
		Now: func() time.Time { return time.Date(2026, 9, 5, 7, 0, 0, 0, time.UTC) },
		// lockNow defaults to the real, monotonic clock (like production),
		// not the fixed now above: stateLock's contention tests that never
		// override this field must still be bounded by a genuine
		// daemonReadyTimeout deadline rather than spinning forever, so a
		// test that forgets to release the lock fails in seconds instead of
		// hanging until the suite's own timeout (sneat-dev/wb#700 review).
		LockNow:    time.Now,
		Executable: func() (string, error) { return executable, nil },
		Alive:      func(pid int) bool { return alive[pid] },
		Stop:       func(pid int, _ daemon.Supervisor, _ string) error { alive[pid] = false; return nil },
		Sleep:      func(time.Duration) {},
		Version:    func() versionInfo { return versionInfo{Version: "test", Revision: "test-revision"} },
		Token:      func() (string, error) { pid++; return strings.Repeat("a", 30) + string(rune(pid)), nil },
		Health:     func(context.Context, string) error { return nil },
		// Point the hub lookup at a path inside this test's own root, so a
		// status assertion never depends on whether the machine running the
		// suite happens to self-host bench.
		HubConfigPath: func() string { return filepath.Join(root, "wb.yaml") },
		RawPolicy: func(string) (bool, string, error) {
			return true, "test-policy", nil
		},
		Getpid:  func() int { return 4242 },
		Getppid: func() int { return 1 },
		// A fixed, always-unknown process start time by default: no test
		// double should depend on the real process table for correctness, and
		// a hard-coded fake PID (900-series, in these fixtures) must never be
		// checked against whatever real process happens to hold that number
		// on the machine running the suite (sneat-dev/wb#622 review minor).
		ProcessStartTime: func(int) (time.Time, bool) { return time.Time{}, false },
		// The recorded supervisor is presumed still present unless a test
		// deliberately exercises the stale-record escape.
		SupervisorPresent: func(daemon.Supervisor, string) (bool, string) { return true, "" }}}
	deps.Start = func(_ string, args []string, _ string) (int, error) {
		// The supervisor unit no longer pins the lifecycle state path: it
		// passes --managed-start and the daemon resolves its own runtime
		// directory, so this fake resolves it the same way.
		for _, argument := range args {
			if argument == "--lifecycle-state" {
				t.Fatalf("daemon start pinned a resolved lifecycle path: %v", args)
			}
		}
		state, ok, err := (daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}).Load()
		if err != nil || !ok || state.OwnerToken == "" {
			t.Fatalf("starting state = %#v, %t, %v", state, ok, err)
		}
		alive[pid] = true
		return pid, nil
	}
	return deps
}

// The following tests exercise writeLifecycleOwnerPIDInjected's
// filewrite.Injector-reachable error branches (task-9 PR-2): the happy
// path is already covered above, but reaching a create, chmod, write,
// sync, close, or rename failure deterministically needs the injector.

// assertNoLeftoverDaemonLifecycleOwnerTempFile asserts
// writeLifecycleOwnerPIDInjected's defer os.Remove(temporaryName) ran: no
// ".daemon-lifecycle-owner-*" staging file survives a close or rename
// failure (task-9 PR-2 review, B2 mutation evidence: deleting that defer
// survived every test that only asserted the returned error).
