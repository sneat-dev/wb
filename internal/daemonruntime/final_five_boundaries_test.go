package daemonruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/daemon"
)

func TestFinalLifecycleEffectFailuresPreserveNativeBoundaries(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"lifecycle-owner", "owner-parent", "launchd-label", "dead-stop-retirement"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := cwWtDaemonRoot(t)
			deps := daemonTestDependencies(t, root)
			controller := NewController(deps, root)
			failure := errors.New("native effect boundary refused")
			switch mode {
			case "lifecycle-owner":
				controller.locks.protectPath = func(string) error { return failure }
				release, err := controller.lifecycleLock()
				if release != nil || !errors.Is(err, failure) {
					t.Fatalf("release=%v error=%v", release != nil, err)
				}
				// The failed owner write releases its native lock descriptor.
				controller.locks = nativeControllerLockStages()
				release, err = controller.lifecycleLock()
				if err != nil {
					t.Fatal(err)
				}
				release()
			case "owner-parent":
				dir, err := daemon.RuntimeDir(root)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dir, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := controller.writeLifecycleOwnerPID(900); err == nil {
					t.Fatal("file runtime parent accepted")
				}
			case "launchd-label":
				state := daemon.State{Supervisor: daemon.SupervisorLaunchd}
				refusal := controller.refuseDetachedStartUnderSupervisor(state, true, false, DefaultListen, false)
				if !strings.Contains(refusal, "launchctl kickstart -k") || !strings.Contains(refusal, LaunchdLabel) {
					t.Fatalf("refusal=%s", refusal)
				}
			case "dead-stop-retirement":
				provenance, err := controller.Provenance()
				if err != nil {
					t.Fatal(err)
				}
				state := daemonTestState(t, root, DefaultListen, provenance, "owner", deps.Now())
				state.MarkReady(900, deps.Now())
				if err := controller.store.Save(state); err != nil {
					t.Fatal(err)
				}
				controller.deps.Alive = func(int) bool { return false }
				controller.state.load = func() (daemon.State, bool, error) { return daemon.State{}, false, failure }
				if _, err := controller.stop(context.Background(), state); !errors.Is(err, failure) {
					t.Fatalf("error=%v", err)
				}
				actual, found, err := controller.store.Load()
				if err != nil || !found || actual.Status != daemon.StatusDraining {
					t.Fatalf("state=%+v found=%v error=%v", actual, found, err)
				}
			}
		})
	}
}
