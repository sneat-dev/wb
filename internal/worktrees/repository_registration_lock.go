package worktrees

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// repositoryRegistrationLockName is deliberately outside Git's worktree
// metadata directory. Git worktree add/repair mutate several files below
// .git/worktrees and must not be allowed to interleave for one canonical
// repository. The file itself is harmless durable repository-local WB state;
// flock releases it when a process dies, so an interrupted create cannot
// strand future work.
const repositoryRegistrationLockName = "wb-worktree-registration.lock"

type repositoryRegistrationLock struct {
	file *os.File
}

// repositoryRegistrationLockTimeout is a deadlock bound, not a Git latency
// SLO. A Git hook may invoke WB recursively; an unbounded flock here would
// leave the outer Git helper holding the lock while the nested WB invocation
// waits for that same lock forever.
var repositoryRegistrationLockTimeout = 2 * time.Minute

func acquireRepositoryRegistrationLock(canonical *canonicalRepository) (*repositoryRegistrationLock, error) {
	if canonical == nil || canonical.common == nil {
		return nil, fmt.Errorf("repository registration lock requires canonical Git descriptor")
	}
	fd, err := unix.Openat(
		int(canonical.common.Fd()), repositoryRegistrationLockName,
		unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("open repository registration lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), "wb-repository-registration-lock")
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap repository registration lock")
	}
	deadline := time.Now().Add(repositoryRegistrationLockTimeout)
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = file.Close()
			return nil, fmt.Errorf("hold repository registration lock: %w", err)
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("another WB Git mutation held the repository registration lock for %s", repositoryRegistrationLockTimeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return &repositoryRegistrationLock{file: file}, nil
}

func (lock *repositoryRegistrationLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := lock.file.Close()
	lock.file = nil
	return err
}
