package gitrepo

import (
	"os"
	"path/filepath"
	"testing"
)

// lockBranchRef plants a stale lock on the clone's main branch ref, so the
// next commit's ref update fails.
func lockBranchRef(t *testing.T, clonePath string) {
	t.Helper()
	dir := filepath.Join(clonePath, ".git", "refs", "heads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedPathCoversOnlyWhatTheProviderWrites(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{
		"claims/a.yaml":                 true,
		"machines/me/mac/snapshot.yaml": true,
		"README.md":                     true,
		".ingitdb/settings.yaml":        false,
		"sync-reports/$records/x.md":    false,
		"README.md.bak":                 false,
		"claimsx/a.yaml":                false,
		"notes.md":                      false,
		"docs/README.md":                false,
	} {
		if got := ownedPath(path); got != want {
			t.Errorf("ownedPath(%q) = %v, want %v", path, got, want)
		}
	}
}
