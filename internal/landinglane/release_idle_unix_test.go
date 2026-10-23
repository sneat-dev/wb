//go:build unix

package landinglane

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestReleaseIdleReportsLockFailure(t *testing.T) {
	tailCovStubFlock(t, errors.New("flock failed"))
	if _, err := ReleaseIdle(t.TempDir(), "acme/app", "main", "owner"); err == nil || !strings.Contains(err.Error(), "lock landing lane") {
		t.Fatal(err)
	}
}

func TestLegacyOperationObservationUsesRealUnixProcessTable(t *testing.T) {
	t.Parallel()
	// Exercise the real process-table boundary without assuming host idleness.
	if _, err := legacyOperationActive(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	owner := Owner{WBSessionID: "owner", PID: os.Getpid()}
	if _, err := Acquire(home, AcquireRequest{Repository: "acme/app", Target: "main", Self: owner}); err != nil {
		t.Fatal(err)
	}
	owner.OperationPID = os.Getpid()
	if _, err := Acquire(home, AcquireRequest{Repository: "acme/app", Target: "main", Self: owner}); err != nil && !strings.Contains(err.Error(), "another WB command") {
		t.Fatal(err)
	}
}
