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
	// a final symlink; name "" means dirfd itself (AT_EMPTY_PATH), which is
	// how every opened directory is checked.
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
	defer func() { _ = dir.Close() }()
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
	defer func() { _ = unix.Close(parentfd) }()
	fd, err := r.sys.openat(parentfd, name)
	if err != nil {
		if err == unix.ELOOP || err == unix.ENOTDIR {
			return outcome, ErrChanged
		}
		return outcome, fmt.Errorf("open %s/%s: %w", parent, name, err)
	}
	defer func() { _ = unix.Close(fd) }()
	var stat unix.Stat_t
	if err := r.sys.fstat(fd, &stat); err != nil {
		return outcome, fmt.Errorf("stat %s/%s: %w", parent, name, err)
	}
	if !expect.sameInode(uint64(stat.Dev), uint64(stat.Ino), birthAt(fd, "")) {
		return outcome, ErrChanged
	}
	parentMount, err := r.mountIDAt(parentfd, "")
	if err != nil {
		return outcome, fmt.Errorf("mount id of %s: %w", parent, err)
	}
	mount, err := r.mountIDAt(fd, "")
	if err != nil {
		return outcome, fmt.Errorf("mount id of %s/%s: %w", parent, name, err)
	}
	if mount != expect.MountID || mount != parentMount {
		return outcome, ErrContainsMount
	}
	want := filepath.Join(parent, name)
	// A flat candidate has no subdirectory to re-check before, so its own
	// place is confirmed once before anything is removed.
	if err := r.stillAt(fd, want); err != nil {
		return outcome, err
	}
	if err := r.empty(fd, mount, want, &outcome); err != nil {
		return outcome, err
	}
	r.beforeRemoveTop()
	var now unix.Stat_t
	if err := r.sys.fstatat(parentfd, name, &now); err != nil || !expect.sameInode(uint64(now.Dev), uint64(now.Ino), birthAt(parentfd, name)) {
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
			if child.Nlink <= 1 {
				outcome.Bytes += int64(child.Blocks) * 512
			}
			continue
		}
		if err := r.stillAt(fd, top); err != nil {
			return err
		}
		childfd, err := r.sys.openat(fd, name)
		if err != nil {
			return err
		}
		// The mount is checked on the opened directory itself, so a mount
		// placed between the stat and the open is still seen.
		childMount, err := r.mountIDAt(childfd, "")
		if err != nil {
			_ = unix.Close(childfd)
			return err
		}
		if childMount != mount {
			_ = unix.Close(childfd)
			return ErrContainsMount
		}
		err = r.empty(childfd, mount, top, outcome)
		if err == nil {
			// The name must still be the directory just emptied: one renamed
			// away meanwhile, with another empty directory in its place, is
			// not removed in its stead.
			var emptied, named unix.Stat_t
			if r.sys.fstat(childfd, &emptied) != nil || r.sys.fstatat(fd, name, &named) != nil || emptied.Dev != named.Dev || emptied.Ino != named.Ino {
				err = ErrReplaced
			}
		}
		_ = unix.Close(childfd)
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
// same-device bind mounts. Before Linux 5.8 statx has no mount ID, and which
// mount a lookup reaches cannot be established from mountinfo alone (a mount
// over a parent can hide a deeper one), so the mount is unknown.
func statxMountID(dirfd int, name string) (uint64, error) {
	var stx unix.Statx_t
	flags := unix.AT_SYMLINK_NOFOLLOW
	if name == "" {
		flags |= unix.AT_EMPTY_PATH
	}
	if err := statx(dirfd, name, flags, unix.STATX_MNT_ID, &stx); err != nil {
		return 0, err
	}
	if stx.Mask&unix.STATX_MNT_ID == 0 {
		return 0, fmt.Errorf("statx gives no mount ID (Linux 5.8 or later is required): %w", ErrUnsupported)
	}
	return stx.Mnt_id, nil
}

// statx and mountinfoPath are seams for kernels this test machine is not.
var (
	statx         = unix.Statx
	mountinfoPath = "/proc/self/mountinfo"
)

// mountLine is one line of /proc/self/mountinfo.
type mountLine struct {
	id          uint64
	root, point string
}

func parseMountinfo(mountinfo string) []mountLine {
	var lines []mountLine
	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		id, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		// mountinfo escapes space, tab, newline and backslash as octal.
		lines = append(lines, mountLine{id: id, root: unescapeMountinfo(fields[3]), point: unescapeMountinfo(fields[4])})
	}
	return lines
}

func procFDPath(fd int) (string, error) {
	return os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
}

// mountIDOf is statxMountID for a path; a variable so a test can fail it
// after the stat that precedes it succeeded.
var mountIDOf = func(path string) (uint64, error) {
	return statxMountID(unix.AT_FDCWD, path)
}

// hasACL reports a POSIX access ACL on path, which may grant other users
// access the permission bits do not show.
func hasACL(path string) bool {
	size, err := unix.Getxattr(path, "system.posix_acl_access", nil)
	return err == nil && size > 0
}

// unescapeMountinfo decodes the \NNN octal escapes the kernel writes in
// mountinfo path fields.
func unescapeMountinfo(field string) string {
	var out strings.Builder
	for i := 0; i < len(field); i++ {
		if field[i] == '\\' && i+3 < len(field) {
			if value, err := strconv.ParseUint(field[i+1:i+4], 8, 8); err == nil {
				out.WriteByte(byte(value))
				i += 3
				continue
			}
		}
		out.WriteByte(field[i])
	}
	return out.String()
}

// fdMountID is the mount ID of the directory open at fd.
func fdMountID(fd int) (uint64, error) { return statxMountID(fd, "") }

// bindOfSubdirectory reports whether path lies on a mount whose source is a
// subdirectory of another filesystem (a bind mount): its relationship to the
// protected roots then cannot be established from paths.
func bindOfSubdirectory(path string) (bool, error) {
	raw, err := os.ReadFile(mountinfoPath)
	if err != nil {
		return false, err
	}
	// The mount a lookup actually reaches is named by its ID where statx
	// gives one; the line with that ID says what the mount shows.
	var stx unix.Statx_t
	if statx(unix.AT_FDCWD, path, 0, unix.STATX_MNT_ID, &stx) == nil && stx.Mask&unix.STATX_MNT_ID != 0 {
		for _, line := range parseMountinfo(string(raw)) {
			if line.id == stx.Mnt_id {
				return line.root != "/", nil
			}
		}
		return false, fmt.Errorf("mount %d of %s is not in mountinfo", stx.Mnt_id, path)
	}
	return false, fmt.Errorf("statx gives no mount ID for %s: %w", path, ErrUnsupported)
}

// birthAt is the creation time of name under dirfd (dirfd itself when name is
// empty), without following a symlink; 0 when the filesystem keeps none.
func birthAt(dirfd int, name string) int64 {
	flags := unix.AT_SYMLINK_NOFOLLOW
	if name == "" {
		flags |= unix.AT_EMPTY_PATH
	}
	var stx unix.Statx_t
	if unix.Statx(dirfd, name, flags, unix.STATX_BTIME, &stx) != nil || stx.Mask&unix.STATX_BTIME == 0 {
		return 0
	}
	return stx.Btime.Sec*1e9 + int64(stx.Btime.Nsec)
}

func ctimeOf(stat *unix.Stat_t) int64 { return stat.Ctim.Nano() }
