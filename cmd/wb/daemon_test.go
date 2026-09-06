package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
		health:     func(context.Context, string) bool { return true },
	}
	deps.start = func(_ string, args []string, _ string) (int, error) {
		statePath, owner := "", ""
		for index := range args {
			if args[index] == "--lifecycle-state" && index+1 < len(args) {
				statePath = args[index+1]
			}
			if args[index] == "--owner-token" && index+1 < len(args) {
				owner = args[index+1]
			}
		}
		state, ok, err := (daemon.Store{Path: statePath}).Load()
		if err != nil || !ok || state.OwnerToken != owner {
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
