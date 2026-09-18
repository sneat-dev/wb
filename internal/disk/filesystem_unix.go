//go:build !windows

package disk

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// filesystemFor reports the capacity of the filesystem holding path.
//
// Available is deliberately not Free: on most filesystems a reserve is held
// back for root, so Free overstates what an ordinary process can still write.
// The number that matters here is the one a build will actually run out of.
func filesystemFor(path string) (Filesystem, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return Filesystem{}, fmt.Errorf("stat filesystem at %s: %w", path, err)
	}
	blockSize := int64(stat.Bsize)
	total := int64(stat.Blocks) * blockSize
	available := int64(stat.Bavail) * blockSize
	return Filesystem{
		Path:           path,
		TotalBytes:     total,
		AvailableBytes: available,
		UsedBytes:      total - int64(stat.Bfree)*blockSize,
	}, nil
}
