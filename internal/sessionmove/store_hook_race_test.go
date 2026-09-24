package sessionmove

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestRepairPendingLinkAtSkipsAPendingNameACompetingRepairAlreadyRemoved
// deterministically drives repairPendingLinkAt's ENOENT branch
// (internal/sessionmove/store.go: a directory-listed pending-name entry
// that is gone by the time this call tries to open it, so the loop
// continues instead of failing) using the
// repairPendingLinkAtBeforeOpenPending test-only seam, instead of racing
// goroutines against a scheduler that can let one caller finish before
// another starts (review found this exact statement covered only 0/5 under
// GOMAXPROCS=1 with the goroutine-barrier version of this task's tests).
//
// The seam runs right before repairPendingLinkAt opens the one pending-name
// entry its own directory listing already found. Removing that entry from
// inside the seam — simulating a concurrent repair that already finished it
// — guarantees this call's own open observes ENOENT every time, with no
// dependency on concurrency or scheduling at all. Because the competing
// repair already dropped the link, the final file is already single-link
// by the time repairPendingLinkAt re-checks it, so the call still succeeds.
func TestRepairPendingLinkAtSkipsAPendingNameACompetingRepairAlreadyRemoved(t *testing.T) {
	directory := t.TempDir()
	targetPath := directory + "/artifact"
	if err := os.WriteFile(targetPath, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	pendingName := ".pending-" + strings.Repeat("0", 32)
	pendingPath := directory + "/" + pendingName
	if err := os.Link(targetPath, pendingPath); err != nil {
		t.Fatal(err)
	}

	dirFile, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dirFile.Close() }()

	original := repairPendingLinkAtBeforeOpenPending
	defer func() { repairPendingLinkAtBeforeOpenPending = original }()
	called := false
	repairPendingLinkAtBeforeOpenPending = func(hookDirectory *os.File, name string) {
		called = true
		if name != pendingName {
			t.Fatalf("hook name = %q, want %q", name, pendingName)
		}
		// A competing repair (or this same repair racing against
		// itself on another machine) already unlinked the pending
		// name before this call gets to open it.
		if err := unix.Unlinkat(int(hookDirectory.Fd()), name, 0); err != nil {
			t.Fatalf("competing unlink inside hook: %v", err)
		}
	}

	if err := repairPendingLinkAt(dirFile, "artifact"); err != nil {
		t.Fatalf("repairPendingLinkAt with an already-removed pending name = %v, want nil", err)
	}
	if !called {
		t.Fatal("repairPendingLinkAtBeforeOpenPending was never invoked")
	}

	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("pending link stat = %v, want it gone (removed by the hook's competing unlink)", err)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("target mode = %v, want a regular file", info.Mode())
	}
}

// TestRepairPendingLinkAtPropagatesAnUnopenablePendingNameError proves the
// ENOENT branch is distinguished from every other open failure: the hook
// makes the pending name unopenable for an unrelated reason (a broken
// symlink), and repairPendingLinkAt must report that error rather than
// silently continuing.
func TestRepairPendingLinkAtPropagatesAnUnopenablePendingNameError(t *testing.T) {
	directory := t.TempDir()
	targetPath := directory + "/artifact"
	if err := os.WriteFile(targetPath, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	pendingName := ".pending-" + strings.Repeat("1", 32)
	pendingPath := directory + "/" + pendingName
	if err := os.Link(targetPath, pendingPath); err != nil {
		t.Fatal(err)
	}

	dirFile, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dirFile.Close() }()

	original := repairPendingLinkAtBeforeOpenPending
	defer func() { repairPendingLinkAtBeforeOpenPending = original }()
	repairPendingLinkAtBeforeOpenPending = func(hookDirectory *os.File, name string) {
		// Replace the pending hard link with a symlink to itself: the
		// production open uses O_NOFOLLOW, so this makes the open
		// fail with ELOOP, not ENOENT.
		if err := unix.Unlinkat(int(hookDirectory.Fd()), name, 0); err != nil {
			t.Fatalf("remove pending link inside hook: %v", err)
		}
		if err := os.Symlink(pendingPath, pendingPath); err != nil {
			t.Fatalf("create self-symlink inside hook: %v", err)
		}
	}

	err = repairPendingLinkAt(dirFile, "artifact")
	if err == nil {
		t.Fatal("repairPendingLinkAt with an unopenable pending name succeeded, want an error")
	}
	if strings.Contains(err.Error(), "not single-link") {
		t.Fatalf("repairPendingLinkAt error = %v, want the open failure, not a downstream link-count error", err)
	}
}
