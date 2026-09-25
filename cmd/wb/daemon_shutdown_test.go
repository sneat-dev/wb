package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/daemon"
)

// TestClassifyServeResultNormalizesEveryCleanShutdownOutcome proves the three
// benign outcomes a serving goroutine can report after ctx is cancelled —
// nil, http.ErrServerClosed, and net.ErrClosed — all collapse to the same
// sentinel, and that a real error is left untouched. This is what makes the
// "which goroutine's result the select reads first" race in serveDashboard
// irrelevant: every producer now sends the identical value on a clean stop.
func TestClassifyServeResultNormalizesEveryCleanShutdownOutcome(t *testing.T) {
	realErr := errors.New("listener accept failed")
	cases := map[string]struct {
		in   error
		want error
	}{
		"nil, as fileBridge.Serve reports on ctx.Done":                 {in: nil, want: errCleanDaemonShutdown},
		"http.ErrServerClosed, as server.Serve reports after Shutdown": {in: http.ErrServerClosed, want: errCleanDaemonShutdown},
		"net.ErrClosed, as Serve reports after listener.Close":         {in: net.ErrClosed, want: errCleanDaemonShutdown},
		"a real serve error passes through unchanged":                  {in: realErr, want: realErr},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			got := classifyServeResult(testCase.in)
			if !errors.Is(got, testCase.want) || (testCase.want == realErr && got != realErr) {
				t.Fatalf("classifyServeResult(%v) = %v, want %v", testCase.in, got, testCase.want)
			}
		})
	}
}

// TestAwaitDaemonServeResultReducesTheClassifiedResult proves
// awaitDaemonServeResult (the function serveDashboard's shutdown select
// hands its single read to) takes exactly one branch for every clean
// shutdown, regardless of which producer's classified value it was handed,
// and returns a real error unchanged.
func TestAwaitDaemonServeResultReducesTheClassifiedResult(t *testing.T) {
	if err := awaitDaemonServeResult(classifyServeResult(nil)); err != nil {
		t.Fatalf("clean shutdown (nil) = %v, want nil", err)
	}
	if err := awaitDaemonServeResult(classifyServeResult(http.ErrServerClosed)); err != nil {
		t.Fatalf("clean shutdown (http.ErrServerClosed) = %v, want nil", err)
	}
	real := errors.New("daemon file bridge request backlog exceeds 1024 entries")
	if err := awaitDaemonServeResult(classifyServeResult(real)); !errors.Is(err, real) {
		t.Fatalf("real error = %v, want %v", err, real)
	}
}

// TestServeDashboardStopsCleanlyWhenContextIsCancelled is the behaviour-named
// integration proof for the clean-shutdown path: cancelling ctx must return
// nil from serveDashboard every time, not just when an HTTP server happens
// to win the shutdown race against fileBridge.Serve's already-nil result.
func TestServeDashboardStopsCleanlyWhenContextIsCancelled(t *testing.T) {
	root := daemonShutdownTestRoot(t)
	deps := daemonTestDependencies(t, root)
	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)

	store := daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}
	served := make(chan error, 1)
	go func() {
		served <- serveDashboard(&invocation{projectsRoot: root}, command, deps, address, store, "owner-token", true, false)
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
}

// TestServeDashboardReturnsARealServeError is the behaviour-named integration
// proof for the other branch: a genuine, non-benign error from one of the
// serving goroutines (here, the file bridge's request directory disappearing
// out from under its poll loop) must still reach serveDashboard's caller
// unchanged, ctx cancellation or not.
func TestServeDashboardReturnsARealServeError(t *testing.T) {
	root := daemonShutdownTestRoot(t)
	deps := daemonTestDependencies(t, root)
	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)

	store := daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}
	served := make(chan error, 1)
	go func() {
		served <- serveDashboard(&invocation{projectsRoot: root}, command, deps, address, store, "owner-token", true, false)
	}()
	waitForHealth(t, address)

	// The daemon is up, which means newDaemonFileBridgeServer already
	// created its requests directory. Removing it permanently makes the file
	// bridge's next poll (daemonFileBridgePoll, 250ms) fail with a real,
	// non-benign error — deterministic, since the directory never comes
	// back, unlike a one-shot race.
	base, err := daemonFileBridgeDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(base, "requests")); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-served:
		if err == nil {
			t.Fatal("serveDashboard returned nil after a real file bridge error")
		}
		if errors.Is(err, errCleanDaemonShutdown) {
			t.Fatalf("real error %v was misclassified as a clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveDashboard did not return after its file bridge failed")
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
