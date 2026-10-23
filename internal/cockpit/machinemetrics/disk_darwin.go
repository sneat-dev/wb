package machinemetrics

import "golang.org/x/sys/unix"

// fragmentSize is zero on macOS, which counts blocks in its block size.
func fragmentSize(*unix.Statfs_t) uint64 { return 0 }
