//go:build e2e

package worktreerun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
)

// committedWorkspaceRepository is a checkout of a repository that commits its
// own go.work at the root, as sneat-co/commitius does.
func committedWorkspaceRepository(t *testing.T) string {
	t.Helper()
	checkout := cwWtGitRepo(t, filepath.Join(t.TempDir(), "checkout"))
	if err := os.MkdirAll(filepath.Join(checkout, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "backend", "go.mod"), []byte("module example.test/backend\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, streams.GoWorkFile), []byte(committedWorkspace), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", ".")
	runGit(t, checkout, "commit", "-m", "commit the workspace")
	return checkout
}

// The 2026-11-08 refusal: a committed go.work unchanged from HEAD is
// repository content, not a WB local link, so `wb worktree end` proceeds.
func TestE2EWorktreeEndAcceptsACommittedGoWork(t *testing.T) {
	t.Parallel()
	checkout := committedWorkspaceRepository(t)
	guard := streamLinkGuard{store: streams.OpenAt(filepath.Join(t.TempDir(), "streams"))}
	reasons, sanctioned, err := guard.LiveLinks(checkout)
	if err != nil || len(reasons) != 0 || len(sanctioned) != 0 {
		t.Fatalf("a committed workspace refused: reasons %v, sanctioned %v, err %v", reasons, sanctioned, err)
	}
}

// A committed go.work edited to reach outside the repository is the protection
// the check exists for: it still refuses, and the remedy never deletes it.
func TestE2EWorktreeEndRefusesAModifiedCommittedGoWorkWithoutSuggestingDeletion(t *testing.T) {
	t.Parallel()
	checkout := committedWorkspaceRepository(t)
	if err := os.WriteFile(filepath.Join(checkout, streams.GoWorkFile),
		[]byte("go 1.27\n\nuse (\n\t./backend\n\t/elsewhere/library\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reasons, sanctioned, err := streamLinkGuard{}.LiveLinks(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if len(reasons) != 1 || !strings.Contains(reasons[0], "/elsewhere/library") {
		t.Fatalf("reasons = %v, want the modified workspace named", reasons)
	}
	if len(sanctioned) != 1 || strings.Contains(sanctioned[0], "--undo") || strings.Contains(sanctioned[0], "rm ") ||
		!strings.Contains(sanctioned[0], "restore --source=HEAD") {
		t.Fatalf("sanctioned = %v, want commit-or-restore, never a deletion", sanctioned)
	}
}
