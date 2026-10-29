//go:build e2e

package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// TestMandatoryPathTargetRefusesCanonicalEvenWithTheLegacyOverride seeds a real
// Git canonical clone and linked worktree to prove the contained admission path
// still refuses a whole-clone mount, so it belongs to the e2e tier.
func TestE2EMandatoryPathTargetRefusesCanonicalEvenWithTheLegacyOverride(t *testing.T) {
	t.Parallel()
	canonical := t.TempDir()
	runGit(t, canonical, "init", "-b", "main")
	runGit(t, canonical, "config", "user.email", "test@example.com")
	runGit(t, canonical, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(canonical, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, canonical, "add", "-A")
	runGit(t, canonical, "commit", "-qm", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, canonical, "worktree", "add", "-b", "linked", linked)

	var listed bool
	deps := DispatchDeps{
		OriginSlug: func(context.Context, string) (string, error) { return "acme/app", nil },
		ListWorktrees: func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error) {
			listed = true
			return []worktrees.ListResult{{
				Repository: "acme/app", WorktreeDir: linked, CanonicalDir: canonical,
			}}, nil
		},
	}

	request := DispatchRequest{Mode: ModePath, Worktree: linked}
	resolved, err := resolveExistingTargetWithPolicy(context.Background(), request, deps, true)
	if err != nil {
		t.Fatalf("a managed linked worktree must resolve: %v", err)
	}
	if resolved.CanonicalDir != canonical {
		t.Fatalf("canonical directory = %q, want %q", resolved.CanonicalDir, canonical)
	}

	// The legacy --allow-canonical option must not turn a contained execution
	// into a whole-clone mount.
	listed = false
	request = DispatchRequest{Mode: ModePath, Worktree: canonical, AllowCanonical: true}
	if _, err := resolveExistingTargetWithPolicy(context.Background(), request, deps, true); err == nil || !strings.Contains(err.Error(), "canonical clone") {
		t.Fatalf("a canonical clone must be refused: %v", err)
	}
	if listed {
		t.Fatal("a canonical target must be refused before the inventory is trusted")
	}

	// The absent-policy path keeps the legacy behaviour exactly.
	request = DispatchRequest{Mode: ModePath, Worktree: canonical, AllowCanonical: true}
	legacy, err := resolveExistingTargetWithPolicy(context.Background(), request, deps, false)
	if err != nil {
		t.Fatalf("legacy current/path resolution must still allow a canonical checkout: %v", err)
	}
	if legacy.CanonicalDir != "" {
		t.Fatalf("legacy resolution must not claim inventory metadata: %#v", legacy)
	}
}
