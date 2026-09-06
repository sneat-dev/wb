package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
)

func TestDaemonRequiresLoopbackListener(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8766", "localhost:8766", "[::1]:8766"} {
		if err := requireLoopbackAddress(address); err != nil {
			t.Errorf("%s rejected: %v", address, err)
		}
	}
	for _, address := range []string{":8766", "0.0.0.0:8766", "192.0.2.10:8766", "bad"} {
		if err := requireLoopbackAddress(address); err == nil {
			t.Errorf("%s accepted", address)
		}
	}
}

func TestDaemonStatusWorksWithoutDaemonAndSupportsJSONShortcut(t *testing.T) {
	root := t.TempDir()
	deps := daemonTestDependencies(t, root)
	previousRoot := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previousRoot })
	command := newDaemonStatusCmd(deps)
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

func TestDaemonStatusMarksDeadReadyStateStopped(t *testing.T) {
	root := t.TempDir()
	deps := daemonTestDependencies(t, root)
	state := daemon.NewStarting(nil, daemonDefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner", time.Now())
	state.MarkReady(900, time.Now())
	if err := (daemon.Store{Path: daemonStatePath(root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Status != daemon.StatusStopped || result.State.PID != 0 || result.Reachable {
		t.Fatalf("dead ready daemon status = %#v", result)
	}
	stored, found, err := (daemon.Store{Path: daemonStatePath(root)}).Load()
	if err != nil || !found || stored.Status != daemon.StatusStopped {
		t.Fatalf("persisted stale daemon state = %#v, %t, %v", stored, found, err)
	}
}

func TestDaemonStatusSeparatesReadyStateFromFailedAPIProbe(t *testing.T) {
	root := t.TempDir()
	deps := daemonTestDependencies(t, root)
	state := daemon.NewStarting(nil, daemonDefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner", time.Now())
	state.MarkReady(900, time.Now())
	if err := (daemon.Store{Path: daemonStatePath(root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 900 }
	deps.health = func(context.Context, string) error { return errors.New("connect: operation not permitted") }
	deps.bridgeHealth = func(context.Context, string, string) error { return errors.New("bridge unavailable") }

	result, err := newDaemonController(deps, root).Status(context.Background())
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
	if err := writeDaemonResult(&textOutput, "text", result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"state=ready", "process_manager_running=true", "api_reachable=false", "direct_transport_reachable=false", `direct_transport_error="connect: operation not permitted"`, `api_probe_error="protected file bridge: bridge unavailable"`} {
		if !strings.Contains(textOutput.String(), want) {
			t.Fatalf("text status %q does not contain %q", textOutput.String(), want)
		}
	}
	var jsonOutput bytes.Buffer
	if err := writeDaemonResult(&jsonOutput, "json", result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"process_manager_running": true`, `"reachable": false`, `"direct_transport_reachable": false`, `"direct_transport_error": "connect: operation not permitted"`, `"reachability_error": "protected file bridge: bridge unavailable"`, `"status": "ready"`} {
		if !strings.Contains(jsonOutput.String(), want) {
			t.Fatalf("JSON status %q does not contain %q", jsonOutput.String(), want)
		}
	}
	stored, found, err := (daemon.Store{Path: daemonStatePath(root)}).Load()
	if err != nil || !found || stored.Status != daemon.StatusReady || stored.Queue.Generation != 1 {
		t.Fatalf("probe failure mutated lifecycle state = %#v, %t, %v", stored, found, err)
	}
}

func TestDaemonStatusUsesAuthenticatedFileBridgeAfterDirectTransportDenial(t *testing.T) {
	root := t.TempDir()
	deps := daemonTestDependencies(t, root)
	state := daemon.NewStarting(nil, daemonDefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner", time.Now())
	state.MarkReady(901, time.Now())
	if err := (daemon.Store{Path: daemonStatePath(root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 901 }
	deps.health = func(context.Context, string) error { return syscall.EPERM }
	var probedRoot, probedGeneration string
	deps.bridgeHealth = func(_ context.Context, root, generation string) error {
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
	if err := writeDaemonResult(&output, "text", result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"api_reachable=true", "direct_transport_reachable=false", "api_transport=file_bridge", "direct_transport_error="} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("text status %q does not contain %q", output.String(), want)
		}
	}
	output.Reset()
	if err := writeDaemonResult(&output, "json", result); err != nil {
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

func TestDaemonStatusReportsDirectTransportWithoutBridgeProbe(t *testing.T) {
	root := t.TempDir()
	deps := daemonTestDependencies(t, root)
	state := daemon.NewStarting(nil, daemonDefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner", time.Now())
	state.MarkReady(902, time.Now())
	if err := (daemon.Store{Path: daemonStatePath(root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 902 }
	deps.health = func(context.Context, string) error { return nil }
	deps.bridgeHealth = func(context.Context, string, string) error {
		t.Fatal("direct success unexpectedly probed the file bridge")
		return nil
	}

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reachable || !result.DirectTransportReachable || result.ReachabilityTransport != "direct" || result.DirectTransportError != "" || result.ReachabilityError != "" {
		t.Fatalf("direct status = %#v", result)
	}
}

func TestDaemonStatusDoesNotBridgeDisallowedDirectFailure(t *testing.T) {
	root := t.TempDir()
	deps := daemonTestDependencies(t, root)
	state := daemon.NewStarting(nil, daemonDefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner", time.Now())
	state.MarkReady(903, time.Now())
	if err := (daemon.Store{Path: daemonStatePath(root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 903 }
	deps.health = func(context.Context, string) error { return errors.New("unexpected response identity") }
	deps.bridgeHealth = func(context.Context, string, string) error {
		t.Fatal("disallowed direct failure unexpectedly probed the file bridge")
		return nil
	}

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reachable || result.DirectTransportReachable || result.DirectTransportError != "unexpected response identity" || result.ReachabilityError != result.DirectTransportError {
		t.Fatalf("disallowed fallback status = %#v", result)
	}
}

func TestDaemonStartDoesNotRestartManagedProcessAfterFailedAPIProbe(t *testing.T) {
	root := t.TempDir()
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	first, err := controller.Start(context.Background(), daemonDefaultListen)
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	originalStart := deps.start
	deps.start = func(executable string, args []string, logPath string) (int, error) {
		starts++
		return originalStart(executable, args, logPath)
	}
	deps.health = func(context.Context, string) error { return errors.New("connect: operation not permitted") }

	result, err := newDaemonController(deps, root).Start(context.Background(), daemonDefaultListen)
	if err != nil {
		t.Fatal(err)
	}
	if !result.AlreadyRunning || !result.ProcessManagerRunning || result.Reachable || starts != 0 {
		t.Fatalf("start after blocked probe = %#v, starts=%d", result, starts)
	}
	if result.State.Queue.Generation != first.State.Queue.Generation {
		t.Fatalf("queue generation changed from %d to %d", first.State.Queue.Generation, result.State.Queue.Generation)
	}
}

func TestDaemonStartKeepsOwnerTokenOutOfProcessArguments(t *testing.T) {
	root := t.TempDir()
	deps := daemonTestDependencies(t, root)
	originalStart := deps.start
	deps.start = func(executable string, args []string, logPath string) (int, error) {
		for _, argument := range args {
			if strings.Contains(argument, strings.Repeat("a", 30)) {
				t.Fatalf("owner token leaked in daemon argv: %q", args)
			}
			if argument == "--owner-token" {
				t.Fatalf("owner-token flag leaked in daemon argv: %q", args)
			}
		}
		return originalStart(executable, args, logPath)
	}
	if _, err := newDaemonController(deps, root).Start(context.Background(), daemonDefaultListen); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonStopAndExplicitRestartPreserveQueueHandoff(t *testing.T) {
	root := t.TempDir()
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	first, err := controller.Start(context.Background(), daemonDefaultListen)
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := controller.Stop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State.Status != daemon.StatusStopped || stopped.State.Queue.Generation != first.State.Queue.Generation {
		t.Fatalf("stop = %#v", stopped)
	}
	restarted, err := controller.Restart(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.State.Queue.Generation != first.State.Queue.Generation+1 || restarted.State.Queue.HandoffFrom == nil {
		t.Fatalf("restart = %#v", restarted)
	}
}

func TestDaemonRestartProgressIsPhaseAwareAndBounded(t *testing.T) {
	if daemonRestartProgressInterval >= 10*time.Second {
		t.Fatalf("restart progress interval = %s", daemonRestartProgressInterval)
	}
	ticks := make(chan time.Time, 1)
	stopped := false
	var observedInterval time.Duration
	controller := daemonController{deps: daemonDependencies{restartTicker: func(interval time.Duration) (<-chan time.Time, func()) {
		observedInterval = interval
		return ticks, func() { stopped = true }
	}}}
	messages := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		controller.restartPhase(func(message string) { messages <- message }, "draining daemon pid 900", func() { <-release })
		close(done)
	}()
	if message := <-messages; message != "draining daemon pid 900" {
		t.Fatalf("initial progress = %q", message)
	}
	ticks <- time.Now()
	if message := <-messages; message != "draining daemon pid 900 (still waiting)" {
		t.Fatalf("repeated progress = %q", message)
	}
	close(release)
	<-done
	if !stopped {
		t.Fatal("restart progress ticker was not stopped")
	}
	if observedInterval != daemonRestartProgressInterval {
		t.Fatalf("restart progress ticker interval = %s", observedInterval)
	}
}

func TestDaemonRestartReportsPhasesAndKeepsJSONStdoutClean(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			deps := daemonTestDependencies(t, root)
			if _, err := newDaemonController(deps, root).Start(context.Background(), daemonDefaultListen); err != nil {
				t.Fatal(err)
			}
			previousRoot := projectsRoot
			projectsRoot = root
			t.Cleanup(func() { projectsRoot = previousRoot })
			command := newDaemonRestartCmd(deps)
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

func TestDaemonStartIsIdempotentAndHandoffsChangedInstalledBinary(t *testing.T) {
	root := t.TempDir()
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	first, err := controller.Start(context.Background(), daemonDefaultListen)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Reachable || first.State.Queue.Generation != 1 {
		t.Fatalf("first start = %#v", first)
	}
	second, err := controller.Start(context.Background(), daemonDefaultListen)
	if err != nil {
		t.Fatal(err)
	}
	if !second.AlreadyRunning {
		t.Fatalf("second start must be idempotent: %#v", second)
	}

	newExecutable := filepath.Join(root, "wb-new")
	if err := os.WriteFile(newExecutable, []byte("new installed binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	deps.executable = func() (string, error) { return newExecutable, nil }
	handoff, err := newDaemonController(deps, root).Start(context.Background(), daemonDefaultListen)
	if err != nil {
		t.Fatal(err)
	}
	if !handoff.AutomaticVersionHandoff || handoff.State.Queue.Generation != 2 {
		t.Fatalf("version handoff = %#v", handoff)
	}
	if handoff.State.Queue.HandoffFrom == nil || handoff.State.Queue.HandoffFrom.Executable == newExecutable {
		t.Fatalf("queue handoff source = %#v", handoff.State.Queue.HandoffFrom)
	}
}

func TestDaemonJSONShortcutRejectsConflictingFormat(t *testing.T) {
	if _, err := daemonOutputFormat("yaml", true); err == nil {
		t.Fatal("expected conflicting format to fail")
	}
	format, err := daemonOutputFormat("text", true)
	if err != nil || format != "json" {
		t.Fatalf("shortcut = %q, %v", format, err)
	}
}

func daemonTestDependencies(t *testing.T, root string) daemonDependencies {
	t.Helper()
	executable := filepath.Join(root, "wb")
	if err := os.WriteFile(executable, []byte("old installed binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{}
	pid := 900
	deps := daemonDependencies{
		now:        func() time.Time { return time.Date(2026, 9, 5, 7, 0, 0, 0, time.UTC) },
		executable: func() (string, error) { return executable, nil },
		alive:      func(pid int) bool { return alive[pid] },
		stop:       func(pid int) error { alive[pid] = false; return nil },
		sleep:      func(time.Duration) {},
		version:    func() versionInfo { return versionInfo{Version: "test", Revision: "test-revision"} },
		token:      func() (string, error) { pid++; return strings.Repeat("a", 30) + string(rune(pid)), nil },
		health:     func(context.Context, string) error { return nil },
		rawPolicy: func(string) (bool, string, error) {
			return true, "test-policy", nil
		},
	}
	deps.start = func(_ string, args []string, _ string) (int, error) {
		statePath := ""
		for index := range args {
			if args[index] == "--lifecycle-state" && index+1 < len(args) {
				statePath = args[index+1]
			}
		}
		state, ok, err := (daemon.Store{Path: statePath}).Load()
		if err != nil || !ok || state.OwnerToken == "" {
			t.Fatalf("starting state = %#v, %t, %v", state, ok, err)
		}
		alive[pid] = true
		state.MarkReady(pid, deps.now())
		if err := (daemon.Store{Path: statePath}).Save(state); err != nil {
			t.Fatal(err)
		}
		return pid, nil
	}
	return deps
}
