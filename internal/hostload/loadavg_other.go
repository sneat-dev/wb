//go:build !darwin && !linux

package hostload

// readLoadAvg1 has no source on this platform. Check treats this as
// fail-open: admission is never refused where load cannot be read.
func readLoadAvg1() (float64, error) {
	return 0, ErrUnsupported
}
