//go:build !windows

package diskprune

import (
	"fmt"
	"os"

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
	// The directory actually opened must be on the mount the file was
	// checked on: a shard replaced by a mount since is not the cache.
	if mount, err := fdMountID(dirfd); err != nil || mount != expect.MountID {
		return 0, ErrChanged
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, fmt.Errorf("stat %s/%s: %w", dir, name, err)
	}
	if !expect.sameInode(uint64(stat.Dev), uint64(stat.Ino), birthAt(dirfd, name)) || stat.Mode&unix.S_IFMT != unix.S_IFREG || (expect.Ctime != 0 && ctimeOf(&stat) != expect.Ctime) {
		return 0, ErrChanged
	}
	if err := unix.Unlinkat(dirfd, name, 0); err != nil {
		return 0, fmt.Errorf("unlink %s/%s: %w", dir, name, err)
	}
	if stat.Nlink > 1 {
		// Another link keeps the blocks; nothing came back.
		return 0, nil
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
	identity := Identity{Dev: uint64(stat.Dev), Ino: uint64(stat.Ino), IsDir: stat.Mode&unix.S_IFMT == unix.S_IFDIR, Birth: birthAt(unix.AT_FDCWD, path)}
	if stat.Mode&unix.S_IFMT == unix.S_IFREG {
		identity.Ctime = ctimeOf(&stat)
	}
	mount, err := mountIDOf(path)
	if err != nil {
		return Identity{}, err
	}
	identity.MountID = mount
	return identity, nil
}

// writeMarker creates Go's .partial marker best-effort, never following a
// symlink a hostile entry might have planted.
func writeMarker(path string) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|unix.O_NOFOLLOW, 0o644)
	if err == nil {
		_ = file.Close()
	}
}

// writeNewFile creates path exclusively, never following a symlink and never
// truncating an existing file.
func writeNewFile(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(raw)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// readSmallRegular reads at most n bytes of a regular file without following
// a symlink and without blocking on a FIFO or device.
func readSmallRegular(path string, n int) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, ErrChanged
	}
	buf := make([]byte, n)
	read, err := unix.Read(fd, buf)
	return buf[:max(read, 0)], err
}
