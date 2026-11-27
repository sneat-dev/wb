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

// LockModuleVersion takes Go's own lock on a module version's .lock file,
// without blocking: a version Go is downloading is skipped, not waited on.
// A version without one gets it created, exactly as cmd/go would before
// locking, so deletion never proceeds unlocked. The lock file itself is
// never removed: removing it while another process holds it would let two
// processes lock different inodes.
func LockModuleVersion(path string) (func(), error) {
	// Go's own protocol (cmd/go's lockedfile.MutexAt) creates the .lock
	// beside the version when it is missing; so does prune. The open never
	// blocks, and only a regular file is a lock.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0o666)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%s is not a regular lock file", path)
	}
	if err := lockSyscalls.flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = unix.Close(fd)
		if err == unix.EWOULDBLOCK {
			return nil, &LockedError{Path: path}
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}, nil
}
