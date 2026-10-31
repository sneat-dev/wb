//go:build !windows

package session

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

func protectRegistryPrivate(_ *os.File) error { return nil }
func checkRegistryPrivacy(file *os.File, mode os.FileMode, directory bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	kind := uint32(unix.S_IFREG)
	if directory {
		kind = unix.S_IFDIR
	}
	if uint32(stat.Mode)&uint32(unix.S_IFMT) != kind || uint32(stat.Mode)&0o777 != uint32(mode.Perm()) || !directory && stat.Nlink != 1 {
		return fmt.Errorf("session registry entry is not one private owned file/directory")
	}
	return nil
}
func lockRegistryFile(file *os.File) (func(), error) {
	fd := int(file.Fd())
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return nil, err
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN) }, nil
}

func tryLockRegistryFile(file *os.File) (func(), bool, error) {
	fd := int(file.Fd())
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN) }, true, nil
}
