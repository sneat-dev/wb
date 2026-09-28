//go:build !windows

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
)

// AC: supervisor-is-detected-and-reported (the managed-start path — the shape
// a supervisor unit passing --managed-start would exercise — also records it,
// and the record survives being reconciled to stopped).
//
// This lives in a !windows file, not cmd/wb/daemon_supervisor_test.go,
// because cwWtDaemonRoot (below) exists only under !windows — using it from a
// file with no matching build tag broke the Windows build entirely
// (sneat-dev/wb#622 review item 2).
func TestManagedServeRecordsSupervisorAndSurvivesStop(t *testing.T) {
	root := cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	deps := daemonTestDependencies(t, root)
	deps.getpid = func() int { return 4242 }
	deps.getppid = func() int { return 1 }
	env := map[string]string{"INVOCATION_ID": "managed-invocation", "SYSTEMD_EXEC_PID": "4242"}
	deps.getenv = func(name string) string { return env[name] }
	if err := secureDaemonRuntime(root); err != nil {
		t.Fatal(err)
	}
	statePath := mustDaemonPath(t, daemonStatePath, root)
	starting := daemon.NewStarting(nil, "127.0.0.1:0", daemon.Provenance{}, "cw-wt-token", deps.now())
	if err := (daemon.Store{Path: statePath}).Save(starting); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(250 * time.Millisecond)
		cancel()
	}()
	command := newDaemonServeCmd(&invocation{projectsRoot: root}, deps)
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetContext(ctx)
	var out, errOut strings.Builder
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs([]string{"--listen", "127.0.0.1:0", "--lifecycle-state", statePath})
	if err := command.Execute(); err != nil {
		t.Fatalf("managed serve: %v (stderr=%s)", err, errOut.String())
	}

	state, found, err := (daemon.Store{Path: statePath}).Load()
	if err != nil || !found {
		t.Fatalf("managed state after serve: found=%t err=%v", found, err)
	}
	if state.Status != daemon.StatusStopped {
		t.Fatalf("managed state after serve = %s, want stopped", state.Status)
	}
	if state.Supervisor != daemon.SupervisorSystemd {
		t.Fatalf("managed state supervisor = %q, want systemd", state.Supervisor)
	}
}
