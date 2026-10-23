package lifecyclehooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestOwnedDirectoryProtectionFailuresRemainVisible(t *testing.T) {
	t.Parallel()
	failure := errors.New("protect owned directory failed")
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Err: failure}
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureStateInjected(inj); !errors.Is(err, failure) {
		t.Fatalf("state protection=%v", err)
	}
	if out, stderr, err := openDiagnosticsInjected(t.TempDir(), inj); out != nil || stderr != nil || !errors.Is(err, failure) {
		t.Fatalf("diagnostics protection=%v %v %v", out, stderr, err)
	}
}

func TestWorkerProbePropagatesLockAndReleaseFailures(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"lock", "release", "health quarantine", "health publish"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			dispatcher, _ := hkCovEnv(t)
			if err := dispatcher.ensureState(); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("worker probe release failed")
			calls := 0
			unlock := (*flock.Flock).Unlock
			switch phase {
			case "lock":
				hkCovLockFile(t, filepath.Join(dispatcher.StateDir, "worker.lock"))
			case "release":
				unlock = func(lock *flock.Flock) error { calls++; t.Cleanup(func() { _ = lock.Close() }); return failure }
			case "health quarantine":
				hkCovWriteFile(t, dispatcher.workerHealthPath(), "{broken", 0600)
				dispatcher.Now = func() time.Time {
					calls++
					if err := os.Remove(dispatcher.workerHealthPath()); err != nil {
						t.Fatal(err)
					}
					return time.Now()
				}
			case "health publish":
				dispatcher.Now = func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
			}
			started, err := dispatcher.startWorkerIfIdleWithUnlock(unlock)
			if started || err == nil {
				t.Fatalf("failed worker start admitted=%t %v", started, err)
			}
			if phase == "release" && (!errors.Is(err, failure) || calls != 1) {
				t.Fatalf("release cause=%v calls=%d", err, calls)
			}
			if phase == "health quarantine" && (!errors.Is(err, os.ErrNotExist) || calls != 1) {
				t.Fatalf("quarantine cause=%v calls=%d", err, calls)
			}
			if phase == "health publish" && !strings.Contains(err.Error(), "year outside of range") {
				t.Fatalf("publish cause=%v", err)
			}
		})
	}
}

func TestEmptyQueueShutdownPreservesWorkerReleaseError(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	worker := flock.New(filepath.Join(dispatcher.StateDir, "worker.lock"))
	if err := worker.Lock(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Close() })
	failure := errors.New("release owned worker failed")
	calls := 0
	jobs, released, _, err := dispatcher.claimBatchWithUnlock(1, worker, func(got *flock.Flock) error {
		calls++
		if got != worker {
			t.Fatal("different worker released")
		}
		return failure
	})
	if jobs != nil || released || !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("shutdown=%v %t %v calls=%d", jobs, released, err, calls)
	}
}

func TestStatusRetainsNativeQueueAndQuarantineSnapshotFailures(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"queue", "quarantine"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			dispatcher, _ := hkCovEnv(t)
			queue := dispatcher.queueSnapshot
			quarantine := dispatcher.quarantineSnapshot
			if phase == "queue" {
				queue = func() ([]Queued, []Queued, []string, error) {
					if err := os.Remove(dispatcher.pendingDir()); err != nil {
						t.Fatal(err)
					}
					return dispatcher.queueSnapshot()
				}
			}
			if phase == "quarantine" {
				quarantine = func(limit int) (int, []string, error) {
					if err := os.Remove(dispatcher.quarantineDir()); err != nil {
						t.Fatal(err)
					}
					return dispatcher.quarantineSnapshot(limit)
				}
			}
			if _, err := dispatcher.statusWithSnapshots(1, queue, quarantine, dispatcher.unseenFailureCount); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("snapshot cause=%v", err)
			}
		})
	}
}

func TestStatusReportsUnreadableUnseenStoreAlongsideAvailableSnapshots(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	status, err := dispatcher.statusWithSnapshots(1, dispatcher.queueSnapshot, dispatcher.quarantineSnapshot, func() (int, error) {
		if err := os.Remove(dispatcher.unseenDir()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dispatcher.unseenDir(), []byte("occupied"), 0600); err != nil {
			t.Fatal(err)
		}
		return dispatcher.unseenFailureCount()
	})
	if err != nil || len(status.Findings) != 1 || !strings.Contains(status.Findings[0], "read unseen failures") {
		t.Fatalf("status finding lost=%+v %v", status, err)
	}
}
