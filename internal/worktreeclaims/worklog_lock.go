package worktreeclaims

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/secureopen"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

// OpenWorkLogRunWith opens each private run component relative to its retained parent.
func OpenWorkLogRunWith(opener secureopen.Opener, home, effort, run string, create bool, valid worktreesecure.ValidSegment) (*os.File, string, error) {
	if !valid(effort) || !valid(run) {
		return nil, "", fmt.Errorf("invalid work-log effort/run identity")
	}
	homeDir, err := worktreesecure.OpenAbsoluteDirectoryNoFollowWith(opener, home, create)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = homeDir.Close() }()
	current := homeDir
	for _, segment := range []string{"worklogs", effort, "runs", run} {
		next, openErr := worktreesecure.OpenPrivateChildWith(opener, current, segment, create, valid)
		if current != homeDir {
			_ = current.Close()
		}
		if openErr != nil {
			return nil, "", openErr
		}
		current = next
	}
	return current, filepath.Join(home, "worklogs", effort, "runs", run), nil
}
func OpenWorkLogRun(home, effort, run string, create bool, valid worktreesecure.ValidSegment) (*os.File, string, error) {
	return OpenWorkLogRunWith(secureopen.Real{}, home, effort, run, create, valid)
}
func OpenWorkLogOutbox(home, effort string, create bool, valid worktreesecure.ValidSegment) (*os.File, error) {
	if !valid(effort) {
		return nil, fmt.Errorf("invalid work-log effort identity")
	}
	homeDir, err := worktreesecure.OpenAbsoluteDirectoryNoFollow(home, create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = homeDir.Close() }()
	worklogs, err := worktreesecure.OpenPrivateChild(homeDir, "worklogs", create, valid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = worklogs.Close() }()
	effortDir, err := worktreesecure.OpenPrivateChild(worklogs, effort, create, valid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = effortDir.Close() }()
	return worktreesecure.OpenPrivateChild(effortDir, "outbox", create, valid)
}

type ClaimLockPorts struct{ AfterOpen func(int) }

func LockClaim(runDir *os.File, claimID string, valid worktreesecure.ValidSegment, options ...ClaimLockPorts) (func(), error) {
	locks, err := worktreesecure.OpenPrivateChild(runDir, "locks", true, valid)
	if err != nil {
		return nil, fmt.Errorf("open claim-lock directory: %w", err)
	}
	fd, err := unix.Openat(int(locks.Fd()), claimID+".lock", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(locks.Fd()), claimID+".lock", unix.O_RDWR|unix.O_NOFOLLOW, 0)
	}
	_ = locks.Close()
	if err != nil {
		return nil, fmt.Errorf("open claim-lock file: %w", err)
	}
	if len(options) > 0 && options[0].AfterOpen != nil {
		options[0].AfterOpen(fd)
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = unix.Close(fd) }, nil
}

type LockedWorkLogRun struct {
	Directory *os.File
	Path      string
	Unlock    func()
}

func OpenLockedWorkLogRun(home, effort, run, claimID string, create bool, valid worktreesecure.ValidSegment) (*LockedWorkLogRun, error) {
	directory, path, err := OpenWorkLogRun(home, effort, run, create, valid)
	if err != nil {
		return nil, err
	}
	unlock, err := LockClaim(directory, claimID, valid)
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	return &LockedWorkLogRun{Directory: directory, Path: path, Unlock: unlock}, nil
}
func (run *LockedWorkLogRun) Close() { run.Unlock(); _ = run.Directory.Close() }
func ReadWorkLogClaimAt[T any](runDir *os.File, claimID string, valid worktreesecure.ValidSegment) (T, error) {
	return worktreesecure.ReadPrivateRecordAt[T](runDir, "claims", claimID+".json", valid)
}
func ReadWorkLogTerminalAt[T any](runDir *os.File, claimID string, valid worktreesecure.ValidSegment) (T, error) {
	return worktreesecure.ReadPrivateRecordAt[T](runDir, "terminals", claimID+".json", valid)
}
