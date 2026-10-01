//go:build !windows

package fleet

import (
	"os"
	"syscall"
)

// openNonBlocking opens path for reading without blocking, so opening a FIFO
// with no writer returns at once instead of hanging the export.
func openNonBlocking(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // the path comes from the operator's own configuration.
}
