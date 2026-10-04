//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/sneat-dev/wb/internal/daemon"
)

func TestCwWtDaemonManagedServeLifecycle(t *testing.T) {
	root := cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	deps := daemonTestDependencies(t, root)

	release, err := newDaemonController(deps, root).AcquireStateLock()
	if err != nil {
		t.Fatal(err)
	}
	release()
	statePath := mustDaemonPath(t, daemonruntime.StatePath, root)
	starting := daemon.NewStartingAt(nil, "127.0.0.1:0", daemon.Provenance{}, "cw-wt-token", "", "", deps.Now())
	if err := (daemon.Store{Path: statePath}).Save(starting); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Sample the durable state while the managed daemon is serving, so the
	// recorded startup pid is observable before it is reconciled to stopped.
	pidCh := make(chan int, 1)
	go func() {
		time.Sleep(250 * time.Millisecond)
		sampled, _, err := (daemon.Store{Path: statePath}).Load()
		if err != nil {
			pidCh <- -1
			return
		}
		pidCh <- sampled.PID
	}()
	go func() {
		time.Sleep(700 * time.Millisecond)
		cancel()
	}()
	command := daemonCommandForTest("serve", &invocation{projectsRoot: root}, deps)
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

	select {
	case pid := <-pidCh:
		if pid != os.Getpid() {
			t.Fatalf("serving managed state pid = %d, want %d", pid, os.Getpid())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the managed serve never published a state sample")
	}

	// The managed startup is reconciled to stopped when the context ends.
	state, found, err := (daemon.Store{Path: statePath}).Load()
	if err != nil || !found {
		t.Fatalf("managed state after serve: found=%t err=%v", found, err)
	}
	if state.Status != daemon.StatusStopped {
		t.Fatalf("managed state after serve = %s, want stopped", state.Status)
	}
	if state.OwnerToken != "cw-wt-token" {
		t.Fatalf("managed state owner token = %q", state.OwnerToken)
	}
}

func TestCwWtDaemonManagedServeRefusesSupersededOwnership(t *testing.T) {
	root := cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	deps := daemonTestDependencies(t, root)
	release, err := newDaemonController(deps, root).AcquireStateLock()
	if err != nil {
		t.Fatal(err)
	}
	release()
	statePath := mustDaemonPath(t, daemonruntime.StatePath, root)
	// A ready state is not a starting state, so the managed start is refused
	// before the listener is even considered.
	ready := daemon.NewStartingAt(nil, "127.0.0.1:0", daemon.Provenance{}, "cw-wt-token", "", "", deps.Now())
	ready.MarkReady(1, deps.Now())
	if err := (daemon.Store{Path: statePath}).Save(ready); err != nil {
		t.Fatal(err)
	}
	command := daemonCommandForTest("serve", &invocation{}, deps)
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetContext(context.Background())
	var out, errOut strings.Builder
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs([]string{"--listen", "127.0.0.1:0", "--lifecycle-state", statePath})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "no longer owns a starting lifecycle state") {
		t.Fatalf("managed serve with a ready state = %v", err)
	}
}
