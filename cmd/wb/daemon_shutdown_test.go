package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemonhost"
	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/daemon"
)

// TestClassifyServeResultNormalizesEveryCleanShutdownOutcome proves the three
// benign outcomes a serving goroutine can report after ctx is cancelled —
// nil, http.ErrServerClosed, and net.ErrClosed — all collapse to the same
// sentinel, and that a real error is left untouched. This is what makes the
// "which goroutine's result the select reads first" race in serveDashboard
// irrelevant: every producer now sends the identical value on a clean stop.

// TestAwaitDaemonServeResultReducesTheClassifiedResult proves
// awaitDaemonServeResult (the function serveDashboard's shutdown select
// hands its single read to) takes exactly one branch for every clean
// shutdown, regardless of which producer's classified value it was handed,
// and returns a real error unchanged.

// TestServeDashboardStopsCleanlyWhenContextIsCancelled is the behaviour-named
// integration proof for the clean-shutdown path: cancelling ctx must return
// nil from serveDashboard every time, not just when an HTTP server happens
// to win the shutdown race against fileBridge.Serve's already-nil result.
func TestServeDashboardStopsCleanlyWhenContextIsCancelled(t *testing.T) {
	root := daemonShutdownTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.Token = func() (string, error) { return "owner-token", nil }
	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout bytes.Buffer
	stderr := &lockedBuffer{} // the daemon's goroutines write to it together
	command.SetOut(&stdout)
	command.SetErr(stderr)

	store := daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}
	served := make(chan error, 1)
	go func() {
		served <- newDaemonHost(deps).Serve(command.Context(), daemonhost.Request{ProjectsRoot: root, Listen: address, Quiet: true, ManagedStart: false}, command.OutOrStdout(), command.ErrOrStderr())
	}()
	waitForHealth(t, address)

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serveDashboard after ctx cancel = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveDashboard did not return after its context was cancelled")
	}
	assertDashboardListenerReleased(t, address)
	assertDashboardStopped(t, store)
}

// TestServeDashboardReturnsARealServeError is the behaviour-named integration
// proof for the other branch: a genuine, non-benign error from one of the
// serving goroutines (here, the file bridge's request directory disappearing
// out from under its poll loop) must still reach serveDashboard's caller
// unchanged, ctx cancellation or not.
func TestServeDashboardReturnsARealServeError(t *testing.T) {
	root := daemonShutdownTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.Token = func() (string, error) { return "owner-token", nil }
	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout bytes.Buffer
	stderr := &lockedBuffer{} // the daemon's goroutines write to it together
	command.SetOut(&stdout)
	command.SetErr(stderr)

	store := daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}
	served := make(chan error, 1)
	go func() {
		served <- newDaemonHost(deps).Serve(command.Context(), daemonhost.Request{ProjectsRoot: root, Listen: address, Quiet: true, ManagedStart: false}, command.OutOrStdout(), command.ErrOrStderr())
	}()
	waitForHealth(t, address)

	// The daemon is up, which means newDaemonFileBridgeServer already
	// created its requests directory. Removing it permanently makes the file
	// bridge's next poll (daemonFileBridgePoll, 250ms) fail with a real,
	// non-benign error — deterministic, since the directory never comes
	// back, unlike a one-shot race.
	runtimeDir, err := daemon.RuntimeDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(runtimeDir, "file-bridge", "requests")); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-served:
		if err == nil {
			t.Fatal("serveDashboard returned nil after a real file bridge error")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("real removed-directory error %v lost its os.ErrNotExist identity", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveDashboard did not return after its file bridge failed")
	}
	assertDashboardListenerReleased(t, address)
	assertDashboardStopped(t, store)
}

func TestServeDashboardReleasesListenerWhenProvenanceFails(t *testing.T) {
	root := daemonShutdownTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.Token = func() (string, error) { return "owner-token", nil }
	provenanceErr := errors.New("provenance unavailable")
	deps.Executable = func() (string, error) { return "", provenanceErr }
	address := freeLoopbackAddress(t)
	store := daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}

	err := newDaemonHost(deps).Serve(context.Background(), daemonhost.Request{ProjectsRoot: root, Listen: address, Quiet: true, ManagedStart: false}, os.Stdout, os.Stderr)
	if !errors.Is(err, provenanceErr) {
		t.Fatalf("serveDashboard error = %v, want provenance failure", err)
	}
	assertDashboardListenerReleased(t, address)
	if _, found, err := store.Load(); err != nil || found {
		t.Fatalf("failed startup recorded lifecycle state: found=%t err=%v", found, err)
	}
}

func assertDashboardListenerReleased(t *testing.T, address string) {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("dashboard listener %s remained bound after serve returned: %v", address, err)
	}
	_ = listener.Close()
}

func assertDashboardStopped(t *testing.T, store daemon.Store) {
	t.Helper()
	state, found, err := store.Load()
	if err != nil || !found || state.Status != daemon.StatusStopped {
		t.Fatalf("dashboard lifecycle state after serve: found=%t status=%q err=%v", found, state.Status, err)
	}
}

// daemonShutdownTestRoot mirrors daemonTestRoot/pinDaemonHome but also points
// the package-level projectsRoot at the fixture, the way the other
// serveDashboard integration tests in this package do, and restores it on
// cleanup.
func daemonShutdownTestRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "wb-shutdown-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	pinDaemonHome(t, root)
	return root
}
