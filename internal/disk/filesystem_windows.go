package disk

import (
	"fmt"

	win "golang.org/x/sys/windows"
)

// filesystemFor reports the capacity of the volume holding path.
//
// GetDiskFreeSpaceEx reports free bytes available to the calling user, which is
// the same distinction the unix implementation draws between Free and Available:
// quota may make the caller's headroom smaller than the volume's.
func filesystemFor(path string) (Filesystem, error) {
	pointer, err := win.UTF16PtrFromString(path)
	if err != nil {
		return Filesystem{}, fmt.Errorf("stat filesystem at %s: %w", path, err)
	}
	var availableToCaller, total, free uint64
	if err := win.GetDiskFreeSpaceEx(pointer, &availableToCaller, &total, &free); err != nil {
		return Filesystem{}, fmt.Errorf("stat filesystem at %s: %w", path, err)
	}
	return Filesystem{
		Path:           path,
		TotalBytes:     int64(total),
		AvailableBytes: int64(availableToCaller),
		UsedBytes:      int64(total - free),
	}, nil
}
