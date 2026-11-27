//go:build linux

package diskprune

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

// A second lock on the same directory fails without blocking and names the
// holder from /proc/locks; releasing lets the next run in.
//
//nolint:paralleltest // reads the real /proc/locks through a package seam
func TestLockDirIsExclusiveAndNamesTheHolder(t *testing.T) {
	dir := t.TempDir()
	first, err := LockDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LockDir(dir)
	var locked *LockedError
	if !errors.As(err, &locked) || !errors.Is(err, ErrLocked) || locked.PID != os.Getpid() {
		t.Fatalf("second lock: %v (%#v)", err, locked)
	}
	if locked.Error() != dir+": held by pid "+strconv.Itoa(os.Getpid()) {
		t.Fatalf("message = %q", locked.Error())
	}
	if (&LockedError{Path: "x"}).Error() != "x: held by another process" {
		t.Fatal("anonymous holder message")
	}
	first.Release()
	first.Release()
	var nilLock *Lock
	nilLock.Release()
	again, err := LockDir(dir)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again.Release()
}

// Several directories lock together, and one that cannot be locked releases
// the rest: a missing directory, or flock unsupported on a network home.
//
//nolint:paralleltest // replaces the flock seam
func TestLockDirsAllOrNothing(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	both, err := LockDirs([]string{a, b, a, ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockDir(b); !errors.Is(err, ErrLocked) {
		t.Fatalf("b not locked: %v", err)
	}
	both.Release()
	if _, err := LockDirs([]string{a, filepath.Join(b, "missing")}); err == nil {
		t.Fatal("missing directory locked")
	}
	if lock, err := LockDir(a); err != nil {
		t.Fatalf("a still held after a failed LockDirs: %v", err)
	} else {
		lock.Release()
	}
	realFlock := lockSyscalls.flock
	t.Cleanup(func() { lockSyscalls.flock = realFlock })
	lockSyscalls.flock = func(int, int) error { return unix.ENOLCK }
	if _, err := LockDir(a); err == nil || errors.Is(err, ErrLocked) {
		t.Fatalf("unsupported flock: %v", err)
	}
}

// /proc/locks lines are matched by device and inode; anything else is
// ignored, and a missing file yields no PID.
func TestLockHolderParsing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "locks")
	dev := makedev(8, 1)
	content := "1: POSIX  ADVISORY  WRITE 11 08:01:5 0 EOF\n" +
		"2: FLOCK  ADVISORY  WRITE 22 08:01:6 0 EOF\n" +
		"3: FLOCK  ADVISORY  WRITE 33 bad 0 EOF\n" +
		"4: FLOCK  ADVISORY  WRITE 44 zz:01:5 0 EOF\n" +
		"5: FLOCK  ADVISORY  WRITE xx 08:01:5 0 EOF\n" +
		"6: FLOCK  ADVISORY  WRITE 55 08:01:5 0 EOF\n" +
		"short\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if pid := lockHolder(path, dev, 5); pid != 55 {
		t.Fatalf("holder = %d, want 55", pid)
	}
	if pid := lockHolder(path, dev, 99); pid != 0 {
		t.Fatalf("unknown inode = %d", pid)
	}
	if pid := lockHolder(filepath.Join(t.TempDir(), "absent"), dev, 5); pid != 0 {
		t.Fatalf("missing file = %d", pid)
	}
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		t.Fatal(err)
	}
	if makedev(uint64(unix.Major(stat.Dev)), uint64(unix.Minor(stat.Dev))) != stat.Dev {
		t.Fatal("makedev disagrees with the kernel's encoding")
	}
}
