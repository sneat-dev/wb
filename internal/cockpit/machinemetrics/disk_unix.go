//go:build linux || darwin

package machinemetrics

import "golang.org/x/sys/unix"

// diskUsage is the free (to an unprivileged user) and total bytes of the file
// system holding root.
func diskUsage(root string) (free, total uint64, err error) {
	var stat unix.Statfs_t
	if err = unix.Statfs(root, &stat); err != nil {
		return 0, 0, err
	}
	block := uint64(stat.Bsize) //nolint:gosec // a block size is never negative
	return uint64(stat.Bavail) * block, uint64(stat.Blocks) * block, nil
}
