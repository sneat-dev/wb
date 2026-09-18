//go:build unix

package landinglane

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTailCovReleaseReportsRecordRemovalFailure covers the error branch taken
// when the record exists but cannot be unlinked: on POSIX, unlinking requires
// write permission on the containing directory. A root process bypasses that
// check and so has nothing to assert here.
func TestTailCovReleaseReportsRecordRemovalFailure(t *testing.T) {
	t.Parallel()
	home := tailCovLaneHome(t)
	if _, err := Acquire(home, AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self: Owner{WBSessionID: "wbs-a", PID: 111}, IsOwnerLive: func(Owner) bool { return true },
		Now: fixedNow(time.Unix(1000, 0)),
	}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	lanes := filepath.Join(home, DirName)
	if err := os.Chmod(lanes, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(lanes, 0o700) }()

	err := Release(home, "acme/app", "main", "wbs-a")
	if os.Geteuid() == 0 {
		if err != nil {
			t.Fatalf("root Release: %v", err)
		}
		return
	}
	tailCovWantError(t, "Release", err, "release landing lane")
	if _, found, readErr := Read(home, "acme/app", "main"); readErr != nil || !found {
		t.Fatalf("the record must survive a failed release: found=%v err=%v", found, readErr)
	}
}
