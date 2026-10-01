package fleet

import "os"

// openNonBlocking opens path for reading. Windows has no FIFO in the file
// namespace that opening could block on.
func openNonBlocking(path string) (*os.File, error) {
	return os.Open(path) //nolint:gosec // the path comes from the operator's own configuration.
}
