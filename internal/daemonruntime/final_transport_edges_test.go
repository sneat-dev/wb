package daemonruntime

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
)

func TestNativeTransportFailuresRemainErrors(t *testing.T) {
	t.Parallel()
	if err := daemonOwnedHealthy(context.Background(), "127.0.0.1:0", 12, 1); err == nil {
		t.Fatal("closed endpoint accepted")
	}
	root := cwWtDaemonRoot(t)
	if _, err := daemonFileBridgeKey(root, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := daemonFileBridgeHealthy(ctx, root, "generation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("bridge error=%v", err)
	}

}

func TestOperationClientUsesActualDefaultLocalTransportWhenBindingAbsent(t *testing.T) {
	t.Parallel()
	root := cwWtDaemonRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	provenance, err := controller.Provenance()
	if err != nil {
		t.Fatal(err)
	}
	state := daemonTestState(t, root, DefaultListen, provenance, "owner", deps.Now())
	state.MarkReady(900, deps.Now())
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}
	controller.deps.Alive = func(pid int) bool { return pid == 900 }
	controller.deps.LocalClient = nil
	now := time.Now()
	failure := errors.New("protected bridge refused")
	_, err = operationClientWithProbe(context.Background(), controller, nil, operationProbe{
		now:          func() time.Time { now = now.Add(2 * time.Second); return now },
		after:        func(time.Duration) <-chan time.Time { ch := make(chan time.Time, 1); ch <- now; return ch },
		bridgeClient: func(string, string) (*http.Client, error) { return nil, failure },
	})
	if err == nil {
		t.Fatal("missing private socket accepted")
	}
}

func TestNativeGuardLoadsActualOwnedRecordBeforeStopping(t *testing.T) {
	t.Parallel()
	root := cwWtDaemonRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	provenance, err := controller.Provenance()
	if err != nil {
		t.Fatal(err)
	}
	state := daemonTestState(t, root, DefaultListen, provenance, "owner", deps.Now())
	state.MarkReady(os.Getpid(), deps.Now())
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(controller.store.Path)
	moved := directory + "-moved"
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, directory); err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time, 1)
	ticks <- time.Now()
	stopped := false
	var out strings.Builder
	err = RuntimeGuard(&out, context.Background(), DefaultListen, controller.store, state, "owner", func(time.Duration) (<-chan time.Time, func()) { return ticks, func() { stopped = true } })
	if err == nil || !stopped || !strings.Contains(out.String(), "no longer a real directory") {
		t.Fatalf("error=%v stop=%v out=%s", err, stopped, out.String())
	}
	actual, found, err := controller.store.Load()
	if err != nil || !found || actual.Status != daemon.StatusStopped || actual.StoppedReason == "" {
		t.Fatalf("state=%+v found=%v error=%v", actual, found, err)
	}
}
