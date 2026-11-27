//go:build !windows

package diskprune

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// lockSyscalls is the flock call, as a seam: an NFS home where flock is not
// supported must be reportable without one.
var lockSyscalls = struct {
	flock     func(fd int, how int) error
	procLocks string
}{flock: unix.Flock, procLocks: "/proc/locks"}

// LockDir takes an exclusive, non-blocking flock on an existing directory,
// opened read-only, so it needs no file creation and works on a full disk.
// Any failure means nothing under it may be deleted.
func LockDir(path string) (*Lock, error) {
	fd, err := openDir(path)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if err := lockSyscalls.flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		var stat unix.Stat_t
		pid := 0
		if unix.Fstat(fd, &stat) == nil {
			pid = lockHolder(lockSyscalls.procLocks, uint64(stat.Dev), uint64(stat.Ino))
		}
		_ = unix.Close(fd)
		if err == unix.EWOULDBLOCK {
			return nil, &LockedError{Path: path, PID: pid}
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &Lock{release: func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}}, nil
}

// LockDirs locks each directory in turn, releasing all of them if any fails.
func LockDirs(paths []string) (*Lock, error) {
	var held []*Lock
	for _, path := range lockPaths(paths) {
		lock, err := LockDir(path)
		if err != nil {
			for _, h := range held {
				h.Release()
			}
			return nil, err
		}
		held = append(held, lock)
	}
	return &Lock{release: func() {
		for _, h := range held {
			h.Release()
		}
	}}, nil
}
