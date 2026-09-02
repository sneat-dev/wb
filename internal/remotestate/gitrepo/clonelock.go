package gitrepo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// cloneLockTimeout bounds how long one WB process waits for another to
// finish with the shared clone. It is a deadlock bound, not a network SLO:
// every Provider method's critical section is a handful of local git
// commands plus one fetch/push, so a process still holding the lock after
// this long is presumed stuck (or dead without releasing — flock already
// covers the clean-death case; this covers a genuine hang) rather than
// merely slow.
var cloneLockTimeout = 2 * time.Minute

// cloneLock serializes every WB process on this machine that touches one
// wb-state clone directory. Without it, two `wb worktree cleanup` (or any
// other remote-touching) invocations running concurrently — the ordinary
// case for a shared clone under `~/projects/<owner>/wb-state` — interleave
// their git commands in the same working tree: one process's `git fetch`
// can be mid-write to `.git/refs/remotes/...` while another starts its own
// `git pull --rebase`, and either one can also leave the index dirty for
// the other to trip over. That is exactly the "cannot lock ref" and "Your
// index contains uncommitted changes" failures wb#321 reported — a real
// race between WB processes, not a remote-side conflict (mutateStore
// already handles those via commit+push+rebase-on-reject). Locking the
// clone directory itself is what actually prevents it.
type cloneLock struct {
	file *os.File
}

// cloneLockSuffix names the lock file as a sibling of the clone directory
// rather than inside it, so it exists (and can be locked) before the first
// clone, and is never mistaken for tracked store content.
const cloneLockSuffix = ".lock"

// acquireCloneLock opens (creating if needed) and flocks the lock file
// beside clonePath, waiting up to cloneLockTimeout. The file is retained
// (never removed) so two waiters can never lock two different inodes for
// the same clonePath — the same reasoning as repositoryRegistrationLock and
// sessionmove's execution lock.
func acquireCloneLock(clonePath string) (*cloneLock, error) {
	lockPath := clonePath + cloneLockSuffix
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("create wb-state lock directory: %w", err)
	}
	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open wb-state clone lock: %w", err)
	}
	deadline := time.Now().Add(cloneLockTimeout)
	for {
		lockErr := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if lockErr == nil {
			return &cloneLock{file: file}, nil
		}
		if !errors.Is(lockErr, unix.EWOULDBLOCK) {
			_ = file.Close()
			return nil, fmt.Errorf("lock wb-state clone %s: %w", clonePath, lockErr)
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("another WB process held the wb-state clone lock for %s: %s", cloneLockTimeout, lockPath)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// release unlocks and closes the held descriptor. It is safe to call on a
// nil lock (mirrors repositoryRegistrationLock.release).
func (l *cloneLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
