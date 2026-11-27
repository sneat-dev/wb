//go:build !windows

package diskprune

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openDir opens a directory without following a final symlink.
func openDir(path string) (int, error) {
	return unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// RemoveFileBeneath unlinks name in the directory rel beneath anchor, only
// if it is still the checked file. The anchor is opened and must still be
// the directory the plan verified; every component of rel is then opened
// relative to it without following a symlink and must stay on the anchor's
// mount, so no rename, symlink or mount on the way can lead it elsewhere.
func RemoveFileBeneath(anchor Anchor, rel, name string, expect Identity) (int64, error) {
	dirfd, err := openBeneath(anchor, rel)
	if err != nil {
		return 0, err
	}
	defer func() { _ = unix.Close(dirfd) }()
	// The directory actually opened must be on the mount the file was
	// checked on: a shard replaced by a mount since is not the cache.
	if mount, err := fdMountID(dirfd); err != nil || mount != expect.MountID {
		return 0, ErrChanged
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, fmt.Errorf("stat %s/%s: %w", rel, name, err)
	}
	if !expect.sameInode(uint64(stat.Dev), uint64(stat.Ino), birthAt(dirfd, name)) || stat.Mode&unix.S_IFMT != unix.S_IFREG || (expect.Ctime != 0 && ctimeOf(&stat) != expect.Ctime) {
		return 0, ErrChanged
	}
	if err := unix.Unlinkat(dirfd, name, 0); err != nil {
		return 0, fmt.Errorf("unlink %s/%s: %w", rel, name, err)
	}
	if stat.Nlink > 1 {
		// Another link keeps the blocks; nothing came back.
		return 0, nil
	}
	return int64(stat.Blocks) * 512, nil
}

// openBeneath opens anchor, verifies it is the planned directory, and walks
// rel from it one component at a time.
func openBeneath(anchor Anchor, rel string) (int, error) {
	// The planned path must still resolve to itself: a symlink introduced
	// on the way since the plan (the cache moved into a checkout, its old
	// spelling now pointing there) is refused, even though the opened
	// directory would be the same object.
	if now, err := filepath.EvalSymlinks(anchor.Root); err != nil || now != anchor.Root {
		return -1, ErrChanged
	}
	fd, err := unix.Open(anchor.Root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", anchor.Root, err)
	}
	var stat unix.Stat_t
	mount, mountErr := fdMountID(fd)
	if unix.Fstat(fd, &stat) != nil || mountErr != nil || mount != anchor.ID.MountID || !anchor.ID.sameInode(uint64(stat.Dev), uint64(stat.Ino), birthAt(fd, "")) {
		_ = unix.Close(fd)
		return -1, ErrChanged
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			_ = unix.Close(fd)
			return -1, fmt.Errorf("%q leaves %s", rel, anchor.Root)
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			if err == unix.ELOOP || err == unix.ENOTDIR || err == unix.ENOENT {
				return -1, ErrChanged
			}
			return -1, fmt.Errorf("open %s/%s: %w", anchor.Root, rel, err)
		}
		if mount, err := fdMountID(next); err != nil || mount != anchor.ID.MountID {
			_ = unix.Close(next)
			return -1, ErrChanged
		}
		fd = next
	}
	return fd, nil
}

// StatIdentity records path's identity without following a final symlink,
// as the planner's checks see it.
func StatIdentity(path string) (Identity, error) {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return Identity{}, err
	}
	identity := Identity{Dev: uint64(stat.Dev), Ino: uint64(stat.Ino), IsDir: stat.Mode&unix.S_IFMT == unix.S_IFDIR, Birth: birthAt(unix.AT_FDCWD, path)}
	// The status-change time also moves when prune itself unlinks another
	// name of a hard-linked file, so it is recorded only for a single-link
	// file; birth time still tells a reused inode number apart.
	if stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Nlink == 1 {
		identity.Ctime = ctimeOf(&stat)
	}
	mount, err := mountIDOf(path)
	if err != nil {
		return Identity{}, err
	}
	identity.MountID = mount
	return identity, nil
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

// readRegularFile reads a whole regular file of at most limit bytes. The
// open never blocks (a FIFO or device planted under the name is refused, not
// waited on) and never follows a final symlink.
func readRegularFile(path string, limit int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { _ = file.Close() }()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(raw)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return raw, err
}
