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
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestLifecycleLockRefusalsPrecedeEveryDurableOperation(t *testing.T) {
	t.Parallel()
	failure := errors.New("private lock effect refused")
	for _, operation := range []string{"mark-owned", "mark-unchanged", "start", "restart", "stop", "launch-first", "launch-ready", "recover"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			root := cwWtDaemonRoot(t)
			deps := daemonTestDependencies(t, root)
			deps.Alive = func(pid int) bool { return pid == 900 || pid == 901 }
			controller := NewController(deps, root)
			provenance, err := controller.Provenance()
			if err != nil {
				t.Fatal(err)
			}
			state := daemonTestState(t, root, DefaultListen, provenance, "owner", deps.Now())
			state.MarkReady(900, deps.Now())
			if operation == "recover" {
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
			locks := 0
			nativeLock := controller.locks.lock
			controller.locks.lock = func(file *os.File) (bool, error) {
				if operation == "recover" && !strings.HasSuffix(file.Name(), "daemon.state.lock") {
					return nativeLock(file)
				}
				locks++
				if operation != "launch-ready" || locks == 2 {
					return false, failure
				}
				return nativeLock(file)
			}
			ctx := context.Background()
			switch operation {
			case "mark-owned":
				err = controller.MarkStoppedIfOwned("owner")
			case "mark-unchanged":
				_, err = controller.markStoppedIfUnchanged(state, 900, "owner")
			case "start":
				_, err = controller.Start(ctx, DefaultListen)
			case "restart":
				_, err = controller.RestartWithProgress(ctx, false, nil, false)
			case "stop":
				_, err = controller.Stop(ctx)
			case "launch-first", "launch-ready":
				_, err = controller.launch(ctx, nil, DefaultListen, provenance, "start", false)
			case "recover":
				_, err = controller.RecoverLifecycleLock(ctx, true)
			}
			if !errors.Is(err, failure) {
				t.Fatalf("error=%v locks=%d", err, locks)
			}
		})
	}
}

func TestNativeStartupRecordsCannotBeSilentlyReplacedDuringLaunch(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"owner-changed", "pid-changed", "token-refused", "reason-retained"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := cwWtDaemonRoot(t)
			deps := daemonTestDependencies(t, root)
			controller := NewController(deps, root)
			provenance, err := controller.Provenance()
			if err != nil {
				t.Fatal(err)
			}
			now := deps.Now()
			controller.deps.Now = func() time.Time { return now }
			controller.deps.Sleep = func(d time.Duration) { now = now.Add(d) }
			controller.deps.Bounds = func() LifecycleBounds { b := DefaultLifecycleBounds(); b.Ready = time.Millisecond; return b }
			failure := errors.New("token source refused")
			if mode == "token-refused" {
				controller.deps.Token = func() (string, error) { return "", failure }
			}
			controller.deps.Start = func(string, []string, string) (int, error) {
				actual, found, err := controller.store.Load()
				if err != nil || !found {
					t.Fatalf("starting record=%t,%v", found, err)
				}
				switch mode {
				case "owner-changed":
					actual.OwnerToken = "other-owner"
				case "pid-changed":
					actual.PID = 999
				case "reason-retained":
					actual.StoppedReason = "native bind refusal"
					controller.deps.Alive = func(int) bool { return false }
				}
				if err := controller.store.Save(actual); err != nil {
					t.Fatal(err)
				}
				return 901, nil
			}
			result, err := controller.launch(context.Background(), nil, DefaultListen, provenance, "start", false)
			if err == nil {
				t.Fatal("startup boundary succeeded")
			}
			switch mode {
			case "token-refused":
				if !errors.Is(err, failure) {
					t.Fatalf("error=%v", err)
				}
			case "owner-changed":
				if !strings.Contains(err.Error(), "ownership changed") {
					t.Fatalf("error=%v", err)
				}
			case "pid-changed":
				if !strings.Contains(err.Error(), "unexpected process 999") {
					t.Fatalf("error=%v", err)
				}
			case "reason-retained":
				if !strings.Contains(err.Error(), "native bind refusal") || !result.Managed {
					t.Fatalf("result=%+v error=%v", result, err)
				}
			}
		})
	}
}

func TestRecoveryReadAndFinalOwnerRefusalsKeepPartialNativeReceipts(t *testing.T) {
	t.Parallel()
	failure := errors.New("recovery store stage refused")
	for _, mode := range []string{"load", "before-state", "ready-save", "drain-save", "final-owner", "live-provenance", "unsafe-open"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := cwWtDaemonRoot(t)
			deps := daemonTestDependencies(t, root)
			controller := NewController(deps, root)
			if err := secureDaemonRuntime(root); err != nil {
				t.Fatal(err)
			}
			lockPath := mustDaemonPath(t, daemonLifecycleLockPath, root)
			if err := os.WriteFile(lockPath, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := controller.writeLifecycleOwnerPID(700); err != nil {
				t.Fatal(err)
			}
			provenance, err := controller.Provenance()
			if err != nil {
				t.Fatal(err)
			}
			state := daemonTestState(t, root, DefaultListen, provenance, "owner", deps.Now())
			state.Status = daemon.StatusStopped
			if mode == "ready-save" || mode == "live-provenance" {
				state.Status = daemon.StatusStarting
				state.PID = 900
				controller.deps.Alive = func(pid int) bool { return pid == 900 }
			}
			if mode == "drain-save" {
				state.Status = daemon.StatusDraining
			}
			if mode != "before-state" {
				if err := controller.store.Save(state); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "load":
				controller.state.load = func() (daemon.State, bool, error) { return daemon.State{}, false, failure }
			case "ready-save", "drain-save":
				controller.state.save = func(daemon.State) error { return failure }
			case "before-state", "final-owner":
				controller.locks.protectPath = func(string) error { return failure }
			case "live-provenance":
				controller.deps.Executable = func() (string, error) { return "", failure }
			case "unsafe-open":
				if err := os.Chmod(lockPath, 0666); err != nil {
					t.Fatal(err)
				}
			}
			result, err := controller.RecoverLifecycleLock(context.Background(), true)
			if err == nil || result.Applied {
				t.Fatalf("recovery=%+v error=%v", result, err)
			}
			if mode != "unsafe-open" && !errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}
			if result.LockPath != "" && result.OwnerPath != filepath.Join(filepath.Dir(result.LockPath), daemonLifecycleOwnerFile) {
				t.Fatalf("mixed recovery paths=%+v", result)
			}
		})
	}
}

func TestLifecycleSecondaryFailuresRetainActualPriorState(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"stable-load", "managed-provenance", "restart-provenance", "recycled-save", "replace-load", "supervised-refusal", "replacement-native-generation", "launch-location", "owner-directory"} {
		t.Run(mode, func(t *testing.T) {
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
			failure := errors.New("secondary native stage refused")
			controller.deps.Alive = func(pid int) bool { return pid == 900 }
			switch mode {
			case "stable-load":
				controller.state.load = func() (daemon.State, bool, error) { return daemon.State{}, false, failure }
				_, err = controller.stableLifecycleState()
			case "managed-provenance":
				controller.deps.Executable = func() (string, error) { return "", failure }
				_, err = controller.withCurrentProvenance(Result{Managed: true})
			case "restart-provenance":
				controller.deps.Executable = func() (string, error) { return "", failure }
				_, err = controller.RestartWithProgress(context.Background(), false, nil, false)
			case "recycled-save":
				controller.deps.ProcessStartTime = func(int) (time.Time, bool) { return deps.Now().Add(time.Hour), true }
				state.ProcessStartedAt = deps.Now()
				controller.state.save = func(daemon.State) error { return failure }
				_, err = controller.stop(context.Background(), state)
			case "replace-load":
				controller.deps.Alive = func(int) bool { return false }
				load := controller.state.load
				calls := 0
				controller.state.load = func() (daemon.State, bool, error) {
					calls++
					if calls == 2 {
						return daemon.State{}, false, failure
					}
					return load()
				}
				_, err = controller.stopAndReplace(context.Background(), state, DefaultListen, provenance, "restart", nil)
			case "supervised-refusal":
				state.Supervisor = daemon.SupervisorSystemd
				state.Provenance.Executable = filepath.Join(root, "other")
				if writeErr := testenv.WriteExecutableFile(state.Provenance.Executable, []byte("another binary"), 0700); writeErr != nil {
					t.Fatal(writeErr)
				}
				_, err = controller.stopAndReplace(context.Background(), state, DefaultListen, provenance, "restart", nil)
				if err == nil || !strings.Contains(err.Error(), "refusing to touch") {
					t.Fatalf("error=%v", err)
				}
				return
			case "replacement-native-generation":
				controller.deps.ProcessStartTime = nil
				result, waitErr := controller.waitForSupervisorReplacement(context.Background(), daemon.SupervisorSystemd, provenance, "restart")
				if waitErr != nil || !result.ReadyVerified {
					t.Fatalf("result=%+v error=%v", result, waitErr)
				}
				return
			case "launch-location":
				controller.root = filepath.Join(root, "blocking", "child")
				if writeErr := os.WriteFile(filepath.Join(root, "blocking"), nil, 0600); writeErr != nil {
					t.Fatal(writeErr)
				}
				_, err = controller.launch(context.Background(), nil, DefaultListen, provenance, "start", false)
				if err == nil {
					t.Fatal("invalid root accepted")
				}
				return
			case "owner-directory":
				controller.root = filepath.Join(root, "blocking", "child")
				if writeErr := os.WriteFile(filepath.Join(root, "blocking"), nil, 0600); writeErr != nil {
					t.Fatal(writeErr)
				}
				err = controller.writeLifecycleOwnerPID(900)
				if err == nil {
					t.Fatal("invalid owner parent accepted")
				}
				return
			}
			if !errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
