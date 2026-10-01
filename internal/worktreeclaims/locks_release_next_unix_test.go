//go:build !windows

package worktreeclaims

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

func retainReleaseDuplicate(t *testing.T, original *os.File) *os.File {
	t.Helper()
	flags, err := unix.FcntlInt(original.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("original CLOEXEC flags=%x err=%v", flags, err)
	}
	syscall.ForkLock.RLock()
	fd, err := unix.Dup(int(original.Fd()))
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	duplicate := os.NewFile(uintptr(fd), "retained-lock-reference")
	t.Cleanup(func() { _ = duplicate.Close() })
	return duplicate
}

func TestLocksReleaseNextEmptyLockReacquiresWithRetainedDuplicate(t *testing.T) {
	t.Parallel()
	directory, path := openLockDirectory(t)
	held, err := AcquireOperationLock(directory, false, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	original := held.File()
	identity, err := original.Stat()
	if err != nil || identity.Size() != 0 {
		t.Fatalf("fresh empty lock=%v %v", identity, err)
	}
	duplicate := retainReleaseDuplicate(t, original)
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := original.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("original not closed: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("repeat Release=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(path, ".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("named lock not retired: %v", err)
	}
	next, err := AcquireOperationLock(directory, false, os.Getpid())
	if err != nil {
		t.Fatalf("retained duplicate blocked exact empty-lock sequence: %v", err)
	}
	nextIdentity, err := next.File().Stat()
	if err != nil || !os.SameFile(identity, nextIdentity) || nextIdentity.Size() != 0 {
		t.Fatalf("same empty inode lost: %v %v", nextIdentity, err)
	}
	if _, err := duplicate.Stat(); err != nil {
		t.Fatalf("test did not retain duplicate: %v", err)
	}
	nextFile := next.File()
	next.Preserve()
	if _, err := nextFile.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("preserved next file not closed: %v", err)
	}
}

func TestLocksReleaseNextUnlockCannotReleaseSuccessor(t *testing.T) {
	t.Parallel()
	directory, path := openLockDirectory(t)
	lock, err := AcquireLockAt(directory, "predecessor", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	original := lock.file
	oldIdentity, err := original.Stat()
	if err != nil {
		t.Fatal(err)
	}
	duplicate := retainReleaseDuplicate(t, original)
	var successor *os.File
	var retiredName string
	ports := OperationLockPorts{MoveQuarantine: func(dir *os.File, name string, identity ManagedLockIdentity) (*os.File, error) {
		moved, err := MoveExpectedLockNoReplace(dir, ".lock", name, identity)
		if err != nil {
			return moved, err
		}
		retiredName = name
		successor, err = os.OpenFile(filepath.Join(path, ".lock"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = successor.Close() })
		if err := WriteOperationLockMetadata(successor, "successor", os.Getpid()); err != nil {
			t.Fatal(err)
		}
		if err := HoldOperationLock(successor); err != nil {
			t.Fatal(err)
		}
		return moved, nil
	}}
	if err := lock.Release(ports); err != nil {
		t.Fatal(err)
	}
	if _, err := original.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("original not closed: %v", err)
	}
	named, err := os.Stat(filepath.Join(path, ".lock"))
	successorIdentity, statErr := successor.Stat()
	if err != nil || statErr != nil || os.SameFile(oldIdentity, named) || !os.SameFile(named, successorIdentity) {
		t.Fatalf("successor identity=%v %v", err, statErr)
	}
	probe, err := os.OpenFile(filepath.Join(path, ".lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = probe.Close() })
	if err := unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("successor lost kernel lock: %v", err)
	}
	retired, err := os.OpenFile(filepath.Join(path, retiredName), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = retired.Close() })
	info, err := retired.Stat()
	if err != nil || !os.SameFile(oldIdentity, info) {
		t.Fatalf("retired predecessor identity=%v", err)
	}
	if err := unix.Flock(int(retired.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("own predecessor remained held: %v", err)
	}
	if _, err := duplicate.Stat(); err != nil {
		t.Fatalf("predecessor duplicate not retained: %v", err)
	}
	if err := unix.Flock(int(retired.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
}

func TestLocksReleaseNextPropagatesNativeUnlockFailure(t *testing.T) {
	t.Parallel()
	directory, path := openLockDirectory(t)
	lock, err := AcquireLockAt(directory, "unlock-failure", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	original := lock.file
	identity, err := original.Stat()
	if err != nil {
		t.Fatal(err)
	}
	duplicate := retainReleaseDuplicate(t, original)
	// Deliberately close the os.File, then use its invalid current Fd. Never
	// issue a syscall against a cached numeric descriptor that may be reused.
	lock.beforeRelease = func() {
		if err := original.Close(); err != nil {
			t.Fatal(err)
		}
	}
	err = lock.Release()
	if !errors.Is(err, syscall.EBADF) || !strings.Contains(err.Error(), "unlock retired operation lock") {
		t.Fatalf("native unlock cause=%v", err)
	}
	if _, err := original.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("original still open: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(path, ".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retirement did not precede failure: %v", err)
	}
	names, err := os.ReadDir(path)
	if err != nil || len(names) != 1 {
		t.Fatalf("retired inventory=%v %v", names, err)
	}
	retired, err := os.OpenFile(filepath.Join(path, names[0].Name()), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = retired.Close() })
	info, err := retired.Stat()
	if err != nil || !os.SameFile(identity, info) {
		t.Fatalf("retirement identity changed: %v", err)
	}
	if err := unix.Flock(int(retired.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("failed unlock changed retained lock: %v", err)
	}
	if err := duplicate.Close(); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(retired.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("retained duplicate close did not release: %v", err)
	}
	if err := unix.Flock(int(retired.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
}

func TestLocksReleaseNextInertPartialHandlesStayOpen(t *testing.T) {
	t.Parallel()
	directory, path := openLockDirectory(t)
	borrowed, err := os.Create(filepath.Join(path, "borrowed"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = borrowed.Close() })
	for _, lock := range []OperationLock{{}, {directory: directory}, {file: borrowed}} {
		if err := lock.Release(); err != nil {
			t.Fatalf("inert release=%v", err)
		}
	}
	if _, err := borrowed.Stat(); err != nil {
		t.Fatalf("inert release closed borrowed file: %v", err)
	}
	if _, err := directory.Stat(); err != nil {
		t.Fatalf("inert release closed borrowed directory: %v", err)
	}
}
