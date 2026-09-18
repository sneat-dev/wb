//go:build unix

package landinglane

import (
	"errors"
	"testing"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// tailCovStubFlock swaps unixcompat's flock entry point so the lane lock
// error paths — which a real flock on a freshly opened regular file never
// takes — are still exercised.
//
// It lives behind the unix build tag because unixcompat exposes Flock as a
// settable variable there but as a plain function on Windows.
func tailCovStubFlock(t *testing.T, err error) (restore func()) {
	t.Helper()
	original := unix.Flock
	unix.Flock = func(int, int) error { return err }
	restored := false
	restore = func() {
		if restored {
			return
		}
		restored = true
		unix.Flock = original
	}
	t.Cleanup(restore)
	return restore
}

// TestTailCovLaneEntryPointsSurfaceLockFailures covers the flock failure
// branch shared by every lane entry point.
func TestTailCovLaneEntryPointsSurfaceLockFailures(t *testing.T) {
	t.Parallel()
	tailCovStubFlock(t, errors.New("tailcov: flock unavailable"))
	tailCovAssertLaneEntryPointsFail(t, tailCovLaneHome(t), "lock landing lane")
}
