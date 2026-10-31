//go:build !windows

package session

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/filewrite"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

func openRegistryLifecycleDirectory(directory *os.File, create bool) (*os.File, error) {
	if create {
		if err := unix.Mkdirat(int(directory.Fd()), "lifecycle", 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
	}
	fd, err := unix.Openat(int(directory.Fd()), "lifecycle", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	held := os.NewFile(uintptr(fd), filepath.Join(directory.Name(), "lifecycle"))
	if err := checkRegistryLifecyclePrivacy(held, true); err != nil {
		_ = held.Close()
		return nil, err
	}
	return held, nil
}
func openRegistryLifecycleFile(directory *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(directory.Name(), name)), nil
}
func checkRegistryLifecyclePrivacy(file *os.File, directory bool) error {
	mode := os.FileMode(0600)
	if directory {
		mode = 0700
	}
	return checkRegistryPrivacy(file, mode, directory)
}
func publishRegistryLifecycle(directory *os.File, name string, raw []byte, inj *filewrite.Injector) error {
	return filewrite.WriteBytesImmutableAtInjected(directory, name, raw, 0600, false, inj, nil)
}
