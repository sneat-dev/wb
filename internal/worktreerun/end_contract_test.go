package worktreerun

import (
	"context"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEndNativeFactoryAndPrivateFilesystemErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var released []string
	engine, err := NewEndEngine(root, io.Discard, func(r, task string, w io.Writer) {
		if w != io.Discard {
			t.Fatal("claim output changed")
		}
		released = append(released, r, task)
	})
	if err != nil || engine.ProjectsRoot != root || engine.Inventory == nil || engine.Links == nil || engine.Capture == nil || engine.Notes == nil || engine.Retirer == nil || engine.Claims == nil {
		t.Fatalf("factory engine=%+v error=%v", engine, err)
	}
	if got := engine.Claims.Release(root, "task"); got != "released through the remote-claim path" || len(released) != 2 || released[0] != root || released[1] != "task" {
		t.Fatalf("release=%v result=%q", released, got)
	}
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	cycle := filepath.Join(t.TempDir(), "cycle")
	if err := os.Symlink(cycle, cycle); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEndEngine(cycle, io.Discard, nil); err == nil {
		t.Fatal("streams.Open error lost")
	}
	if _, err := (worktreeInventory{}).Worktrees(context.Background(), cycle, "task", ""); err == nil {
		t.Fatal("inventory error lost")
	}
	if _, err := (workLogNotes{}).Seal(filepath.Join(blocker, "checkout"), "closing"); err == nil {
		t.Fatal("journal error lost")
	}
	if err := (cleanupRetirer{}).Retire(context.Background(), cycle, "task", "acme/app", "/private/wt"); err == nil {
		t.Fatal("cleanup error lost")
	}
}
func TestEndNativeCleanCaptureCannotInventAnImmutableRef(t *testing.T) {
	t.Parallel()
	checkout := cwWtGitRepo(t, filepath.Join(t.TempDir(), "checkout"))
	if _, err := (gitStashCapture{}).Preserve(context.Background(), checkout, "clean"); err == nil || !strings.Contains(err.Error(), "resolve the capture reference") {
		t.Fatalf("clean capture invented ref: %v", err)
	}
}

func TestEndNativeInventorySelectsRealLinkedCheckouts(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	canonical := filepath.Join(projects, "acme", "app")
	testenv.CloneWithOrigin(t, t.TempDir(), "app", canonical)
	checkout := filepath.Join(projects, ".worktrees", "native-end", "acme", "app")
	runGit(t, canonical, "worktree", "add", "-b", "feature/native-end", checkout, "main")
	for _, repo := range []string{"", "ACME/APP", "other/repo"} {
		found, err := (worktreeInventory{}).Worktrees(context.Background(), projects, "native-end", repo)
		if err != nil {
			t.Fatal(err)
		}
		if repo == "other/repo" {
			if len(found) != 0 {
				t.Fatalf("foreign filter = %v", found)
			}
			continue
		}
		if len(found) != 1 || found[0].Path != checkout || found[0].Repository != "acme/app" || found[0].Branch != "feature/native-end" {
			t.Fatalf("inventory(%q)=%+v", repo, found)
		}
	}
}
func TestEndCleanupOutcomeContractDefensiveSelection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		results []worktrees.CleanupResult
		want    string
	}{
		{name: "missing", want: "cleanup reported no candidate for acme/app at /checkout"},
		{name: "foreign", results: []worktrees.CleanupResult{{ListResult: worktrees.ListResult{Repository: "other/repo"}, Applied: true}}, want: "cleanup reported no candidate for acme/app at /checkout"},
		{name: "applied", results: []worktrees.CleanupResult{{ListResult: worktrees.ListResult{Repository: "ACME/APP"}, Applied: true}}},
		{name: "already-gone", results: []worktrees.CleanupResult{{ListResult: worktrees.ListResult{Repository: "acme/app"}, WorktreeGone: true}}},
		{name: "reason", results: []worktrees.CleanupResult{{ListResult: worktrees.ListResult{Repository: "acme/app"}, Reason: "dirty"}}, want: "cleanup did not retire /checkout: dirty"},
		{name: "missing-reason-first-wins", results: []worktrees.CleanupResult{{ListResult: worktrees.ListResult{Repository: "acme/app"}}, {ListResult: worktrees.ListResult{Repository: "acme/app"}, Applied: true}}, want: "cleanup did not retire /checkout: cleanup reported no reason"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := interpretCleanupRetirement(worktrees.CleanupOutcome{Results: tc.results}, "acme/app", "/checkout")
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != tc.want {
				t.Fatalf("error=%v want=%q", err, tc.want)
			}
		})
	}
}
