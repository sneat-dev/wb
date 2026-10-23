//go:build !windows

package daemonruntime

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestControllerLockStagesRetainPrivateNativeFilesAndErrorIdentity(t *testing.T) {
	t.Parallel()
	failure := errors.New("native descriptor stage failed")
	for _, operation := range []string{"state", "lifecycle", "owner"} {
		for _, stage := range []string{"inspect", "protect", "lock"} {
			if operation == "owner" && stage != "inspect" {
				continue
			}
			t.Run(operation+"/"+stage, func(t *testing.T) {
				t.Parallel()
				root := cwWtDaemonRoot(t)
				controller := NewController(daemonTestDependencies(t, root), root)
				if operation == "owner" {
					if err := controller.writeLifecycleOwnerPID(42); err != nil {
						t.Fatal(err)
					}
				}
				switch stage {
				case "inspect":
					controller.locks.inspect = func(int, *unix.Stat_t) error { return failure }
				case "protect":
					controller.locks.protect = func(*os.File) error { return failure }
				case "lock":
					controller.locks.lock = func(*os.File) (bool, error) { return false, failure }
				}
				var err error
				switch operation {
				case "state":
					_, err = controller.AcquireStateLock()
				case "lifecycle":
					_, _, _, err = controller.openLifecycleLock(true)
				case "owner":
					_, err = controller.lifecycleOwnerPID(nil)
				}
				if !errors.Is(err, failure) {
					t.Fatalf("error=%v", err)
				}
				// A failed inspection/protection/lock must not create an ownership
				// receipt; an already-written owner remains the same actual private file.
				ownerPath := mustDaemonPath(t, daemonLifecycleOwnerPath, root)
				if operation == "owner" {
					data, err := os.ReadFile(ownerPath)
					if err != nil || string(data) != "pid=42\n" {
						t.Fatalf("owner=%q, %v", data, err)
					}
				} else if _, err := os.Lstat(ownerPath); !os.IsNotExist(err) {
					t.Fatalf("premature owner receipt: %v", err)
				}
			})
		}
	}
}

func TestOwnerProtectionRefusalRemovesUnpublishedTemporary(t *testing.T) {
	t.Parallel()
	root := cwWtDaemonRoot(t)
	controller := NewController(daemonTestDependencies(t, root), root)
	failure := errors.New("native owner protection refused")
	controller.locks.protectPath = func(string) error { return failure }
	if err := controller.writeLifecycleOwnerPID(900); !errors.Is(err, failure) {
		t.Fatalf("error=%v", err)
	}
	assertNoLeftoverDaemonLifecycleOwnerTempFile(t, root)
	if _, err := os.Stat(mustDaemonPath(t, daemonLifecycleOwnerPath, root)); !os.IsNotExist(err) {
		t.Fatalf("failed protection published owner: %v", err)
	}
}
