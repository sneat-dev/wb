//go:build !windows

package diskprune

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// openDir opens a directory without following a final symlink.
func openDir(path string) (int, error) {
	return unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// RemoveFileAt unlinks name in dir only if it is still the checked file. It
// opens dir without following symlinks, so a dir replaced by a symlink is
// refused rather than followed.
func RemoveFileAt(dir, name string, expect Identity) (int64, error) {
	dirfd, err := openDir(dir)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", dir, err)
	}
	defer func() { _ = unix.Close(dirfd) }()
	var stat unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, fmt.Errorf("stat %s/%s: %w", dir, name, err)
	}
	if uint64(stat.Dev) != expect.Dev || uint64(stat.Ino) != expect.Ino || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return 0, ErrChanged
	}
	if err := unix.Unlinkat(dirfd, name, 0); err != nil {
		return 0, fmt.Errorf("unlink %s/%s: %w", dir, name, err)
	}
	return int64(stat.Blocks) * 512, nil
}

// StatIdentity records path's identity without following a final symlink,
// as the planner's checks see it.
func StatIdentity(path string) (Identity, error) {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return Identity{}, err
	}
	identity := Identity{Dev: uint64(stat.Dev), Ino: uint64(stat.Ino), IsDir: stat.Mode&unix.S_IFMT == unix.S_IFDIR}
	mount, err := mountIDOf(path)
	if err != nil {
		return Identity{}, err
	}
	identity.MountID = mount
	return identity, nil
}
