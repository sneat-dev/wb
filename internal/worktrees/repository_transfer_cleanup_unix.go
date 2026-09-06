//go:build !windows

package worktrees

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func repositoryTransferCleanupIdentity(file *os.File) (device, inode uint64, err error) {
	var status unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &status); err != nil {
		return 0, 0, err
	}
	return uint64(status.Dev), uint64(status.Ino), nil
}

func repositoryTransferCleanupIdentityMatches(file *os.File, device, inode uint64) bool {
	actualDevice, actualInode, err := repositoryTransferCleanupIdentity(file)
	return err == nil && actualDevice == device && actualInode == inode
}

func openRepositoryTransferCleanupQuarantine(parent *os.File, _ string, name string) (*os.File, bool, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	held := os.NewFile(uintptr(fd), name)
	if held == nil {
		_ = unix.Close(fd)
		return nil, false, fmt.Errorf("open replacement quarantine")
	}
	return held, false, nil
}

func retireRepositoryTransferReplacement(path string, held *os.File) error {
	parentPath := filepath.Dir(path)
	parent, err := openAbsoluteDirectoryNoFollow(parentPath, false)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	name := filepath.Base(path)
	if !directoryEntryStillMatches(parent, name, held) {
		return fmt.Errorf("replacement quarantine changed after verification: %s", path)
	}
	if err := removeDirectoryContentsAt(held, path, 0); err != nil {
		return err
	}
	if !directoryEntryStillMatches(parent, name, held) {
		return fmt.Errorf("replacement quarantine changed during retirement: %s", path)
	}
	if err := unlinkResidueEntry(parent, parentPath, name, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	absent, err := noFollowChildAbsent(int(parent.Fd()), name)
	if err != nil || !absent {
		return fmt.Errorf("verify retired replacement quarantine %s", path)
	}
	return nil
}
