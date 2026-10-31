//go:build windows

package session

import (
	"errors"
	"fmt"
	"os"

	"github.com/strongo/cli-helpers/daemonlifecycle"
	"golang.org/x/sys/windows"
)

func protectRegistryPrivate(file *os.File) error { return daemonlifecycle.ProtectOwnerOnlyFile(file) }
func checkRegistryPrivacy(file *os.File, _ os.FileMode, directory bool) error {
	if !directory {
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
			return err
		}
		if info.NumberOfLinks != 1 {
			return fmt.Errorf("private session registry file has multiple links")
		}
		return daemonlifecycle.ValidateOwnerOnlyFile(file)
	}
	// The existing exported directory ACL primitive is named; bind both sides
	// to our retained no-follow handle instead of trusting an ambient path.
	held, err := file.Stat()
	if err != nil {
		return err
	}
	named, err := os.Lstat(file.Name())
	if err != nil {
		return err
	}
	if !held.IsDir() || named.Mode()&os.ModeSymlink != 0 || !os.SameFile(held, named) {
		return fmt.Errorf("private session registry directory changed")
	}
	if err := daemonlifecycle.ValidateOwnerOnly(file.Name()); err != nil {
		return err
	}
	named, err = os.Lstat(file.Name())
	if err != nil || named.Mode()&os.ModeSymlink != 0 || !os.SameFile(held, named) {
		return fmt.Errorf("private session registry directory changed during ACL check")
	}
	return nil
}
func lockRegistryFile(file *os.File) (func(), error) {
	var overlapped windows.Overlapped
	handle := windows.Handle(file.Fd())
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped); err != nil {
		return nil, err
	}
	return func() { _ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped) }, nil
}

func tryLockRegistryFile(file *os.File) (func(), bool, error) {
	var overlapped windows.Overlapped
	handle := windows.Handle(file.Fd())
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { _ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped) }, true, nil
}
