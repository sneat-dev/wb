package worktreerun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/streams"
)

const committedWorkspace = "go 1.27\n\nuse ./backend\n"

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
func TestWorktreeEndAcceptsACommittedGoWork(t *testing.T) {
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
func TestWorktreeEndRefusesAModifiedCommittedGoWorkWithoutSuggestingDeletion(t *testing.T) {
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

// A go.work a recorded link wrote is cleared by the recorded link's undo.
func TestWorktreeEndNamesUndoForARecordedGoWorkLink(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	library := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, streams.GoWorkFile), []byte("go 1.27\n\nuse "+library+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := streams.OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Create(streams.Stream{Name: "linked", Members: []streams.Member{{
		Repository: "acme/app", Role: streams.RoleConsumer, Worktree: worktree,
		Links: []streams.Link{{
			Library: library, Mechanism: streams.MechanismGoWork, State: streams.LinkStateApplied,
			Identity: "acme.example/library", CreatedAt: time.Now(),
		}},
	}}}); err != nil {
		t.Fatal(err)
	}
	reasons, sanctioned, err := streamLinkGuard{store: store}.LiveLinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(reasons) != 2 {
		t.Fatalf("reasons = %v, want the record and the go.work", reasons)
	}
	if want := "wb deps propagate local --to " + worktree + " --undo"; sanctioned[1] != want {
		t.Fatalf("sanctioned = %v, want %q for the go.work", sanctioned, want)
	}
}
