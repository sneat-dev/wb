//go:build windows

package worktrees

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func repositoryTransferCleanupIdentity(file *os.File) (device, inode uint64, err error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return 0, 0, err
	}
	return uint64(info.VolumeSerialNumber), uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow), nil
}

func repositoryTransferCleanupIdentityMatches(file *os.File, device, inode uint64) bool {
	actualDevice, actualInode, err := repositoryTransferCleanupIdentity(file)
	return err == nil && actualDevice == device && actualInode == inode
}

func openRepositoryTransferCleanupQuarantine(parent *os.File, parentPath, name string) (*os.File, bool, error) {
	if !directoryStillMatches(parentPath, parent) {
		return nil, false, fmt.Errorf("replacement quarantine parent changed before verification: %s", parentPath)
	}
	path := filepath.Join(parentPath, name)
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, err
	}
	handle, err := windows.CreateFile(
		path16,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	held := os.NewFile(uintptr(handle), path)
	if held == nil {
		_ = windows.CloseHandle(handle)
		return nil, false, fmt.Errorf("open replacement quarantine")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = held.Close()
		return nil, false, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = held.Close()
		return nil, false, fmt.Errorf("replacement quarantine is not a direct directory: %s", path)
	}
	if !directoryStillMatches(parentPath, parent) {
		_ = held.Close()
		return nil, false, fmt.Errorf("replacement quarantine parent changed during verification: %s", parentPath)
	}
	return held, false, nil
}

func retireRepositoryTransferReplacement(path string, _ *os.File) error {
	return fmt.Errorf("secure replacement quarantine retirement is unavailable on Windows; preserved %s for manual inspection", path)
}
