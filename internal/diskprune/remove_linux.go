//go:build linux

package diskprune

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// treeRemover removes one checked directory tree through descriptors. Its
// hooks exist so tests can simulate a mount, a move or a replacement that an
// unprivileged test cannot create for real.
type treeRemover struct {
	sys treeSyscalls
	// mountIDAt returns the mount ID of name relative to dirfd, not following
	// a final symlink.
	mountIDAt func(dirfd int, name string) (uint64, error)
	// fdPath returns the current path of an open descriptor.
	fdPath func(fd int) (string, error)
	// beforeRemoveTop runs just before the top directory's name is removed.
	beforeRemoveTop func()
}

// treeSyscalls are the system calls the remover makes, as a table so a test
// can fail any one of them; the real ones come from x/sys/unix.
type treeSyscalls struct {
	openDir  func(path string) (int, error)
	openat   func(dirfd int, name string) (int, error)
	fstat    func(fd int, stat *unix.Stat_t) error
	fstatat  func(dirfd int, name string, stat *unix.Stat_t) error
	unlinkat func(dirfd int, name string, flags int) error
	fchmod   func(fd int, mode uint32) error
	readDir  func(fd int) ([]string, error)
}

func realTreeSyscalls() treeSyscalls {
	return treeSyscalls{
		openDir: openDir,
		openat: func(dirfd int, name string) (int, error) {
			return unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		},
		fstat: unix.Fstat,
		fstatat: func(dirfd int, name string, stat *unix.Stat_t) error {
			return unix.Fstatat(dirfd, name, stat, unix.AT_SYMLINK_NOFOLLOW)
		},
		unlinkat: unix.Unlinkat,
		fchmod:   unix.Fchmod,
		readDir:  readDirNames,
	}
}

// readDirNames lists the directory open at fd without consuming fd itself.
func readDirNames(fd int) ([]string, error) {
	dup, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(dup), "dir")
	defer dir.Close()
	return dir.Readdirnames(-1)
}

func newTreeRemover() treeRemover {
	return treeRemover{sys: realTreeSyscalls(), mountIDAt: statxMountID, fdPath: procFDPath, beforeRemoveTop: func() {}}
}

// RemoveTree removes parent/name, which the planner recorded as expect, and
// everything under it (disk-prune#req:deletion).
func RemoveTree(parent, name string, expect Identity) (TreeOutcome, error) {
	return newTreeRemover().remove(parent, name, expect)
}

func (r treeRemover) remove(parent, name string, expect Identity) (TreeOutcome, error) {
	var outcome TreeOutcome
	parentfd, err := r.sys.openDir(parent)
	if err != nil {
		return outcome, fmt.Errorf("open %s: %w", parent, err)
	}
	defer unix.Close(parentfd)
	fd, err := r.sys.openat(parentfd, name)
	if err != nil {
		if err == unix.ELOOP || err == unix.ENOTDIR {
			return outcome, ErrChanged
		}
		return outcome, fmt.Errorf("open %s/%s: %w", parent, name, err)
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := r.sys.fstat(fd, &stat); err != nil {
		return outcome, fmt.Errorf("stat %s/%s: %w", parent, name, err)
	}
	if uint64(stat.Dev) != expect.Dev || uint64(stat.Ino) != expect.Ino {
		return outcome, ErrChanged
	}
	parentMount, err := r.mountIDAt(parentfd, ".")
	if err != nil {
		return outcome, fmt.Errorf("mount id of %s: %w", parent, err)
	}
	mount, err := r.mountIDAt(parentfd, name)
	if err != nil {
		return outcome, fmt.Errorf("mount id of %s/%s: %w", parent, name, err)
	}
	if mount != expect.MountID || mount != parentMount {
		return outcome, ErrContainsMount
	}
	want := filepath.Join(parent, name)
	if err := r.empty(fd, mount, want, &outcome); err != nil {
		return outcome, err
	}
	r.beforeRemoveTop()
	var now unix.Stat_t
	if err := r.sys.fstatat(parentfd, name, &now); err != nil || uint64(now.Ino) != expect.Ino || uint64(now.Dev) != expect.Dev {
		return outcome, ErrReplaced
	}
	if err := r.sys.unlinkat(parentfd, name, unix.AT_REMOVEDIR); err != nil {
		return outcome, fmt.Errorf("remove %s: %w", want, err)
	}
	return outcome, nil
}

// empty removes every entry of the directory open at fd. top is the path the
// candidate must still have; each directory is re-checked before descending.
func (r treeRemover) empty(fd int, mount uint64, top string, outcome *TreeOutcome) error {
	var stat unix.Stat_t
	if err := r.sys.fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&0o200 == 0 {
		// Go writes extracted modules read-only; entries cannot be removed
		// from a directory its owner cannot write.
		if err := r.sys.fchmod(fd, stat.Mode&0o7777|0o700); err != nil {
			return err
		}
	}
	names, err := r.sys.readDir(fd)
	if err != nil {
		return err
	}
	for _, name := range names {
		var child unix.Stat_t
		if err := r.sys.fstatat(fd, name, &child); err != nil {
			return err
		}
		if child.Mode&unix.S_IFMT != unix.S_IFDIR {
			if err := r.sys.unlinkat(fd, name, 0); err != nil {
				return err
			}
			outcome.Files++
			outcome.Bytes += int64(child.Blocks) * 512
			continue
		}
		childMount, err := r.mountIDAt(fd, name)
		if err != nil {
			return err
		}
		if childMount != mount {
			return ErrContainsMount
		}
		if err := r.stillAt(fd, top); err != nil {
			return err
		}
		childfd, err := r.sys.openat(fd, name)
		if err != nil {
			return err
		}
		err = r.empty(childfd, mount, top, outcome)
		unix.Close(childfd)
		if err != nil {
			return err
		}
		if err := r.sys.unlinkat(fd, name, unix.AT_REMOVEDIR); err != nil {
			return err
		}
	}
	return nil
}

// stillAt confirms the directory open at fd is still at or under top. A tree
// moved into, say, a protected root mid-removal is left alone from then on.
func (r treeRemover) stillAt(fd int, top string) error {
	current, err := r.fdPath(fd)
	if err != nil {
		return err
	}
	if current != top && !within(current, top) {
		return ErrMoved
	}
	return nil
}

// statxMountID reads the mount ID with statx, which also distinguishes
// same-device bind mounts. Before Linux 5.8 statx has no mount ID; then the
// longest mount point in /proc/self/mountinfo that contains the path decides.
func statxMountID(dirfd int, name string) (uint64, error) {
	var stx unix.Statx_t
	flags := unix.AT_SYMLINK_NOFOLLOW
	if name == "." {
		flags = 0
	}
	if err := statx(dirfd, name, flags, unix.STATX_MNT_ID, &stx); err != nil {
		return 0, err
	}
	if stx.Mask&unix.STATX_MNT_ID != 0 {
		return stx.Mnt_id, nil
	}
	path := name
	if !filepath.IsAbs(name) {
		base, err := procFDPath(dirfd)
		if err != nil {
			return 0, err
		}
		path = filepath.Join(base, name)
	}
	raw, err := os.ReadFile(mountinfoPath)
	if err != nil {
		return 0, fmt.Errorf("no mount id from statx or mountinfo: %w", ErrUnsupported)
	}
	return mountIDFromMountinfo(string(raw), path)
}

// statx and mountinfoPath are seams for kernels this test machine is not.
var (
	statx         = unix.Statx
	mountinfoPath = "/proc/self/mountinfo"
)

// mountIDFromMountinfo returns the ID of the mount whose mount point is the
// longest prefix of path.
func mountIDFromMountinfo(mountinfo, path string) (uint64, error) {
	best, bestLen := uint64(0), -1
	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		id, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		point := fields[4]
		if (point == "/" || within(path, point)) && len(point) > bestLen {
			best, bestLen = id, len(point)
		}
	}
	if bestLen < 0 {
		return 0, fmt.Errorf("no mount in mountinfo contains %s: %w", path, ErrUnsupported)
	}
	return best, nil
}

func procFDPath(fd int) (string, error) {
	return os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
}

// mountIDOf is statxMountID for a path; a variable so a test can fail it
// after the stat that precedes it succeeded.
var mountIDOf = func(path string) (uint64, error) {
	return statxMountID(unix.AT_FDCWD, path)
}
