package worktreerun

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/streams"
)

const committedWorkspace = "go 1.27\n\nuse ./backend\n"

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
