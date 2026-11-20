//go:build !windows

package worktreeclaims

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

// Each test below keeps a dup of the held descriptor open, standing in for a
// child forked between fork and exec: it shares the open file description, so
// Close alone would leave the flock held. The path under test must LOCK_UN
// before it closes, which a fresh non-blocking flock then proves.

func requireFlockFree(t *testing.T, path string) {
	t.Helper()
	probe, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Close() }()
	if err := unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("%s stayed flocked through a retained duplicate: %v", filepath.Base(path), err)
	}
}

func TestLocksPreserveUnlocksBeforeClose(t *testing.T) {
	t.Parallel()
	directory, path := openLockDirectory(t)
	held, err := AcquireOperationLock(directory, false, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	original := held.File()
	retainReleaseDuplicate(t, original)
	held.Preserve()
	if _, err := original.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("preserved descriptor not closed: %v", err)
	}
	requireFlockFree(t, filepath.Join(path, ".lock"))
}

func TestLocksFailedReleaseUnlocksBeforeClose(t *testing.T) {
	t.Parallel()
	directory, path := openLockDirectory(t)
	lock, err := AcquireLockAt(directory, "task", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	retainReleaseDuplicate(t, lock.file)
	moved := filepath.Join(path, "moved-away")
	if err := os.Rename(filepath.Join(path, ".lock"), moved); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err == nil || !strings.Contains(err.Error(), "changed before retirement") {
		t.Fatalf("release of a moved lock = %v", err)
	}
	if _, err := lock.file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("failed release left descriptor open: %v", err)
	}
	requireFlockFree(t, moved)
}

func TestLocksPostHoldAcquireFailureUnlocksBeforeClose(t *testing.T) {
	t.Parallel()
	directory, path := openLockDirectory(t)
	failure := errors.New("directory inspection failed")
	_, err := AcquireLockAtReclaimingInterrupted(directory, false, "task", os.Getpid(), OperationLockPorts{
		AfterHold:     func(file *os.File) { retainReleaseDuplicate(t, file) },
		StatDirectory: func(int, *unix.Stat_t) error { return failure },
	})
	if !errors.Is(err, failure) {
		t.Fatalf("acquire = %v, want the inspection failure", err)
	}
	requireFlockFree(t, filepath.Join(path, ".lock"))
}

func TestLocksReclaimIdentityChangeUnlocksBeforeClose(t *testing.T) {
	t.Parallel()
	directory, path := openLockDirectory(t)
	lockPath := filepath.Join(path, ".lock")
	if err := os.WriteFile(lockPath, []byte("operation=interrupted\npid=999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	swapped := filepath.Join(path, "swapped-away")
	_, err := ReclaimInterruptedLock(directory, true, OperationLockPorts{AfterHold: func(file *os.File) {
		retainReleaseDuplicate(t, file)
		if err := os.Rename(lockPath, swapped); err != nil {
			t.Fatal(err)
		}
	}})
	if err == nil || !strings.Contains(err.Error(), "changed while being reclaimed") {
		t.Fatalf("reclaim of a swapped lock = %v", err)
	}
	requireFlockFree(t, swapped)
}

func TestLocksRepositoryRegistrationReleaseUnlocksBeforeClose(t *testing.T) {
	t.Parallel()
	directory, path := openLockDirectory(t)
	held, err := AcquireRepositoryRegistrationLock(directory, func() error { return nil }, time.Now, time.Sleep, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	retainReleaseDuplicate(t, held.file)
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	requireFlockFree(t, filepath.Join(path, RepositoryRegistrationLockName))
}

func TestLocksUnlockAndCloseIgnoresANilDescriptor(t *testing.T) {
	t.Parallel()
	unlockAndClose(nil)
}
