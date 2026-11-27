//go:build !linux && !windows

package diskprune

import "golang.org/x/sys/unix"

// RemoveTree needs mount IDs and /proc/self/fd, which only Linux provides, so
// scratch and module trees are never removed elsewhere.
func RemoveTree(parent, name string, expect Identity) (TreeOutcome, error) {
	return TreeOutcome{}, ErrUnsupported
}

// mountIDOf falls back to the device number where there are no bind mounts
// to tell apart.
func mountIDOf(path string) (uint64, error) {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Dev), nil
}

// hasACL cannot be established here; prune deletes no trees off Linux.
func hasACL(path string) bool { return true }

// fdMountID falls back to the device number of the open directory.
func fdMountID(fd int) (uint64, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Dev), nil
}

// bindOfSubdirectory has no mountinfo to consult; prune removes no trees
// here, and the build-cache trim's roots are not bind-checked.
func bindOfSubdirectory(path string) (bool, error) { return false, nil }

// birthAt is not consulted here; identities rely on device and inode.
func birthAt(dirfd int, name string) int64 { return 0 }

func ctimeOf(stat *unix.Stat_t) int64 { return stat.Ctim.Nano() }
