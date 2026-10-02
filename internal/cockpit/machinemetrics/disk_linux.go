package machinemetrics

import "golang.org/x/sys/unix"

// fragmentSize is the statfs fragment size, in which Linux counts blocks.
func fragmentSize(stat *unix.Statfs_t) uint64 { return uint64(stat.Frsize) } //nolint:gosec // never negative
