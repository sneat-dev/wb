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
	block := blockSize(fragmentSize(&stat), uint64(stat.Bsize)) //nolint:gosec // a block size is never negative
	return uint64(stat.Bavail) * block, uint64(stat.Blocks) * block, nil
}

// blockSize is the unit statfs counts blocks in: the fragment size where the
// system has one (Linux's f_frsize), else the block size.
func blockSize(fragment, block uint64) uint64 {
	if fragment != 0 {
		return fragment
	}
	return block
}
