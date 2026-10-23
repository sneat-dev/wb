package migrate

import (
	"errors"
	"github.com/sneat-dev/wb/internal/wbhome"
	"os"
	"path/filepath"
	"testing"
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

func TestCampaignAcquisitionRefusesIncompleteOwnedMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var owned *os.File
	held, err := acquireCampaignLockWithInitializer(root, "metadata", func(file *os.File, id string) error {
		owned = file
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		return initializeCampaignLockMetadata(file, id)
	})
	if !errors.Is(err, os.ErrClosed) || held.lock != nil || held.directory != nil {
		t.Fatalf("held=%+v error=%v", held, err)
	}
	if owned == nil {
		t.Fatal("native lock descriptor not acquired")
	}
	if _, err := owned.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("descriptor live: %v", err)
	}
	// Release retires the original named inode using its saved identity, even
	// when metadata initialization has already closed the owned descriptor.
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
