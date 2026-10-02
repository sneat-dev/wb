//go:build !windows

package migrate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCampaignCleanupRefusesUnreadableCanonicalRegistry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := filepath.Join(root, "acme", "loop")
	if err := os.MkdirAll(canonical, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(canonical, ".git")); err != nil {
		t.Fatal(err)
	}
	removed, err := CleanupCampaignWorktrees(root, "registry")
	if removed != nil || err == nil {
		t.Fatalf("removed=%v error=%v", removed, err)
	}
	if info, err := os.Lstat(filepath.Join(canonical, ".git")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("registry evidence removed: %v", err)
	}
}
