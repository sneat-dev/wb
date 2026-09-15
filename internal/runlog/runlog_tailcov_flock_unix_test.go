//go:build unix

package runlog

import (
	"errors"
	"path/filepath"
	"testing"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// tailCovStubFlock swaps unixcompat's flock entry point so Append's lock error
// path — which a real flock on a freshly opened regular file never takes — is
// still exercised. It returns a restore func so a test can put the real
// implementation back part-way through.
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

// TestTailCovAppendReportsLockFailure proves a lock that cannot be taken is
// reported (rather than silently dropping the event), and that a later Append
// succeeds once locking works again.
func TestTailCovAppendReportsLockFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	restore := tailCovStubFlock(t, errors.New("tailcov: flock unavailable"))
	err := Append(path, Event{})
	tailCovWantError(t, "Append (flock fails)", err, "lock run event log")
	restore()

	if err := Append(path, Event{SchemaVersion: EventSchemaVersion, State: "requested"}); err != nil {
		t.Fatalf("Append after restoring flock: %v", err)
	}
}
