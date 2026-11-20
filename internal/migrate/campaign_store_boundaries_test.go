package migrate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestCampaignCleanupPreservesRootAndLockObservationFailures(t *testing.T) {
	t.Parallel()
	cause := errors.New("projects root unavailable")
	removed, err := cleanupCampaignWorktreesWithRootAndRun("relative", "root", func(string) (string, error) { return "", cause }, runIn)
	if removed != nil || !errors.Is(err, cause) {
		t.Fatalf("removed=%v error=%v", removed, err)
	}
	root := t.TempDir()
	home, err := wbhome.Root(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "worktrees"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	removed, err = CleanupCampaignWorktrees(root, "lock")
	if removed != nil || err == nil {
		t.Fatalf("removed=%v error=%v", removed, err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "worktrees"))
	if err != nil || string(raw) != "retained" {
		t.Fatalf("bytes=%q error=%v", raw, err)
	}
}

func TestRegisteredCampaignWorktreeReportsMissingCanonicalPath(t *testing.T) {
	t.Parallel()
	worktree, err := registeredCampaignWorktree(filepath.Join(t.TempDir(), "missing"), "wb/migrate/missing")
	if worktree != "" || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree=%q error=%v", worktree, err)
	}
}

// A failed metadata initialization must hand the still-flocked descriptor
// back to Release, which retires the inode, unlocks it and closes it. The
// initializer fails without closing the descriptor itself: closing it first
// would leave Release unable to LOCK_UN, so a sibling test's concurrent
// fork..exec window could keep the retired inode flocked and make the
// following acquisition report it as active. Truncate, seek and write
// failures on a closed descriptor are covered by
// TestCampaignLockMetadataReportsNativeDescriptorFailures.
func TestCampaignAcquisitionRefusesIncompleteOwnedMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	incomplete := errors.New("metadata initialization failed")
	var owned *os.File
	held, err := acquireCampaignLockWithInitializer(root, "metadata", func(file *os.File, id string) error {
		owned = file
		return fmt.Errorf("initialize migration campaign %q lock: %w", id, incomplete)
	})
	if !errors.Is(err, incomplete) || held.lock != nil || held.directory != nil {
		t.Fatalf("held=%+v error=%v", held, err)
	}
	if owned == nil {
		t.Fatal("native lock descriptor not acquired")
	}
	// Release closed the descriptor after retiring and unlocking its inode.
	if _, err := owned.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("descriptor live: %v", err)
	}
	home, homeErr := wbhome.Root(root)
	if homeErr != nil {
		t.Fatal(homeErr)
	}
	if _, statErr := os.Lstat(filepath.Join(home, "worktrees", "metadata", ".lock")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed acquisition left active lock: %v", statErr)
	}
	held, err = acquireCampaignLock(root, "metadata")
	if err != nil || held.lock == nil {
		t.Fatalf("following fresh acquisition failed: %v", err)
	}
	if !validCampaignLockMetadata(held.lock.File(), "metadata") {
		t.Fatal("following lock metadata is incomplete")
	}
	if err := held.release(); err != nil {
		t.Fatalf("following lock retirement: %v", err)
	}
}
