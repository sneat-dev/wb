package daemonruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
)

func TestLifecycleRefusalsPreserveIdentityAndDurableDrainReceipt(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"ready-health-refusal", "foreign-home", "recycled-pid", "drain-timeout", "unfenced-recovery", "recovery-provenance-refusal", "recovery-token-refusal"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := cwWtDaemonRoot(t)
			deps := daemonTestDependencies(t, root)
			now := deps.Now()
			deps.Now = func() time.Time { return now }
			deps.Sleep = func(d time.Duration) { now = now.Add(d) }
			deps.Bounds = func() LifecycleBounds {
				b := DefaultLifecycleBounds()
				b.Stop = time.Millisecond
				b.Ready = time.Millisecond
				return b
			}
			alive := true
			deps.Alive = func(pid int) bool { return alive && pid == 900 }
			stops := 0
			deps.Stop = func(int, daemon.Supervisor, string) error { stops++; return nil }
			controller := NewController(deps, root)
			provenance, err := controller.Provenance()
			if err != nil {
				t.Fatal(err)
			}
			state := daemonTestState(t, root, DefaultListen, provenance, "owner", now.Add(-time.Minute))
			state.MarkReady(900, now)
			failure := errors.New("private lifecycle observer refused")
			switch mode {
			case "ready-health-refusal":
				deps.Health = func(context.Context, string) error { return failure }
			case "foreign-home":
				state.WBHome = filepath.Join(root, "other-home")
			case "recycled-pid":
				state.ProcessStartedAt = now.Add(-time.Minute)
				deps.ProcessStartTime = func(int) (time.Time, bool) { return now, true }
			case "unfenced-recovery", "recovery-provenance-refusal", "recovery-token-refusal":
				alive = false
				state.Status = daemon.StatusStarting
				state.PID = 0
				state.UpdatedAt = now.Add(-time.Minute)
				deps.Health = func(context.Context, string) error { return failure }
				if mode == "unfenced-recovery" {
					state.Provenance.SHA256 = "foreign"
				}
				if mode == "recovery-provenance-refusal" {
					deps.Executable = func() (string, error) { return "", failure }
				}
				if mode == "recovery-token-refusal" {
					deps.Token = func() (string, error) { return "", failure }
				}
			}
			if strings.Contains(mode, "recovery") {
				if err := secureDaemonRuntime(root); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(mustDaemonPath(t, daemonLifecycleLockPath, root), nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := controller.writeLifecycleOwnerPID(700); err != nil {
					t.Fatal(err)
				}
			}
			controller.deps = deps
			if err := controller.store.Save(state); err != nil {
				t.Fatal(err)
			}
			var result Result
			switch mode {
			case "ready-health-refusal":
				result, err = controller.Start(context.Background(), DefaultListen)
				if err != nil || result.Reachable || result.DirectTransportError != failure.Error() {
					t.Fatalf("result=%+v error=%v", result, err)
				}
			case "foreign-home":
				result, err = controller.Start(context.Background(), DefaultListen)
				if err == nil || result.Identity != identityForeignHome {
					t.Fatalf("result=%+v error=%v", result, err)
				}
			case "recycled-pid":
				result, err = controller.stop(context.Background(), state)
				if err != nil || stops != 0 || result.Identity != identityProcessRecycled {
					t.Fatalf("result=%+v error=%v stops=%d", result, err, stops)
				}
			case "drain-timeout":
				_, err = controller.stop(context.Background(), state)
				stored, _, loadErr := controller.store.Load()
				if err == nil || !strings.Contains(err.Error(), "remains draining") || stored.Status != daemon.StatusDraining || stops != 1 || loadErr != nil {
					t.Fatalf("error=%v state=%+v stops=%d load=%v", err, stored, stops, loadErr)
				}
			default:
				recovered, recoverErr := controller.RecoverLifecycleLock(context.Background(), true)
				if mode == "unfenced-recovery" {
					if recoverErr != nil || recovered.Reason != "unfenced_startup" || recovered.Applied {
						t.Fatalf("recovery=%+v error=%v", recovered, recoverErr)
					}
				} else if !errors.Is(recoverErr, failure) {
					t.Fatalf("recovery error=%v", recoverErr)
				}
				stored, _, loadErr := controller.store.Load()
				if loadErr != nil || stored.Status != daemon.StatusStarting {
					t.Fatalf("refusal changed startup=%+v err=%v", stored, loadErr)
				}
			}
		})
	}
}

func TestLifecyclePrivateFileAndGenerationBoundaries(t *testing.T) {
	t.Parallel()
	root := cwWtDaemonRoot(t)
	controller := NewController(daemonTestDependencies(t, root), root)
	if err := secureDaemonRuntime(root); err != nil {
		t.Fatal(err)
	}
	path := mustDaemonPath(t, daemonLifecycleOwnerPath, root)
	if err := os.Symlink(filepath.Join(root, "absent"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.lifecycleOwnerPID(nil); err == nil {
		t.Fatal("symlink owner accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(root, "closed-lock")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, _, err := lifecycleLockPID(file); err == nil {
		t.Fatal("closed lock accepted")
	}
	reader, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if _, _, err := lifecycleLockPID(reader); err == nil {
		t.Fatal("directory ownership read accepted")
	}
	controller.deps.ProcessStartTime = nil
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{}, "owner", controller.deps.Now())
	state.PID = -1
	if _, err := controller.stop(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if DaemonAddressInUse(errors.New("ordinary")) {
		t.Fatal("ordinary failure classified as address in use")
	}
	started, _ := daemon.ProcessStartTime(os.Getpid())
	if got := ProcessStartedAt(os.Getpid()); !got.Equal(started) {
		t.Fatalf("actual generation=%v, want %v", got, started)
	}
	if err := RuntimeIntact(daemon.Store{Path: filepath.Join(root, "missing.state")}); err == nil {
		t.Fatal("missing state accepted")
	}
	blocker := filepath.Join(root, "not-directory")
	if err := os.WriteFile(blocker, []byte("receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RuntimeIntact(daemon.Store{Path: filepath.Join(blocker, "state")}); err == nil {
		t.Fatal("non-directory runtime accepted")
	}
}
