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
	held, err := openDirectoryAtNoFollow(int(parent.Fd()), name, name,
		"open replacement quarantine "+name, "wrap replacement quarantine "+name)
	if errors.Is(err, unix.ENOENT) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return held, false, nil
}

func retireRepositoryTransferReplacement(path string, held *os.File) error {
	return retireRepositoryTransferReplacementWithObservation(path, held, nil)
}

func retireRepositoryTransferReplacementWithObservation(path string, held *os.File, observe func(string, *os.File)) error {
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
	if observe != nil {
		observe("identity", parent)
	}
	if !directoryEntryStillMatches(parent, name, held) {
		return fmt.Errorf("replacement quarantine changed during retirement: %s", path)
	}
	if observe != nil {
		observe("unlink", parent)
	}
	if err := unlinkResidueEntry(parent, parentPath, name, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	if observe != nil {
		observe("absence", parent)
	}
	absent, err := noFollowChildAbsent(int(parent.Fd()), name)
	if err != nil || !absent {
		return fmt.Errorf("verify retired replacement quarantine %s", path)
	}
	return nil
}
