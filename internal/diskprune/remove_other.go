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
