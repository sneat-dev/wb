package locallink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
)

// The link guard's refusal for a go.work no stream records never names
// `--undo`, which cannot clear it, and never names a command that deletes a
// tracked file.
// committedWorkspace is a repository's own multi-module workspace — the shape
// sneat-co/commitius commits at its root.
const committedWorkspace = "go 1.27\n\nuse ./backend\n"

func readFileString(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func TestRestoreLinkArtefactsNeverDeletesTrackedContent(t *testing.T) {
	t.Parallel()
	failed := errors.New("git is gone")
	yes := func(string, string) (bool, error) { return true, nil }
	no := func(string, string) (bool, error) { return false, nil }
	broken := func(string, string) (bool, error) { return false, failed }

	t.Run("untracked artefacts are removed, absent ones ignored", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		writeGuardTestFile(t, worktree, goWorkFile, "go 1.27\n")
		notes, err := headFiles{exists: no}.restoreLinkArtefacts(worktree, []string{goWorkFile, goWorkSum})
		if err != nil || len(notes) != 0 || fileExists(filepath.Join(worktree, goWorkFile)) {
			t.Fatalf("notes = %v, err = %v; want the untracked workspace removed silently", notes, err)
		}
	})

	t.Run("an unchanged tracked artefact is left alone", func(t *testing.T) {
		t.Parallel()
		restored := false
		files := headFiles{exists: yes, unchanged: yes, restore: func(string, string) error { restored = true; return nil }}
		notes, err := files.restoreLinkArtefacts(t.TempDir(), []string{goWorkSum})
		if err != nil || restored || len(notes) != 1 || !strings.Contains(notes[0], "unchanged from HEAD") {
			t.Fatalf("notes = %v, err = %v, restored = %v", notes, err, restored)
		}
	})

	t.Run("a committed linked go.work is refused, not cleared", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		writeGuardTestFile(t, worktree, goWorkFile, "linked\n")
		_, err := headFiles{exists: yes, unchanged: yes}.restoreLinkArtefacts(worktree, []string{goWorkFile})
		if err == nil || !strings.Contains(err.Error(), "revert that commit") || !fileExists(filepath.Join(worktree, goWorkFile)) {
			t.Fatalf("err = %v; want the committed link refused and the file kept", err)
		}
	})

	t.Run("a modified tracked artefact is restored, not removed", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		writeGuardTestFile(t, worktree, goWorkFile, "linked\n")
		var restoredPath string
		files := headFiles{exists: yes, unchanged: no, restore: func(_, path string) error { restoredPath = path; return nil }}
		notes, err := files.restoreLinkArtefacts(worktree, []string{goWorkFile})
		if err != nil || restoredPath != goWorkFile || !fileExists(filepath.Join(worktree, goWorkFile)) {
			t.Fatalf("notes = %v, err = %v, restored %q", notes, err, restoredPath)
		}
	})

	t.Run("a recorded path outside the worktree is refused", func(t *testing.T) {
		t.Parallel()
		_, err := headFiles{exists: no}.restoreLinkArtefacts(t.TempDir(), []string{"../go.work"})
		if err == nil || !strings.Contains(err.Error(), "outside the consumer worktree") {
			t.Fatalf("err = %v, want the escaping path refused", err)
		}
	})

	for _, tc := range []struct {
		name  string
		files headFiles
	}{
		{"presence unknown", headFiles{exists: broken}},
		{"difference unknown", headFiles{exists: yes, unchanged: broken}},
		{"restore failed", headFiles{exists: yes, unchanged: no, restore: func(string, string) error { return failed }}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := tc.files.restoreLinkArtefacts(t.TempDir(), []string{goWorkFile}); !errors.Is(err, failed) {
				t.Fatalf("err = %v, want the Git failure", err)
			}
		})
	}

	t.Run("an untracked artefact that cannot be removed is reported", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		writeGuardTestFile(t, worktree, filepath.Join(goWorkSum, "keep.txt"), "keep\n")
		if _, err := (headFiles{exists: no}).restoreLinkArtefacts(worktree, []string{goWorkSum}); err == nil ||
			!strings.Contains(err.Error(), "remove "+goWorkSum) {
			t.Fatalf("err = %v, want the failed removal reported", err)
		}
	})

	t.Run("a record without artefacts falls back to the Go workspace pair", func(t *testing.T) {
		t.Parallel()
		if got := goWorkArtefacts(streams.Link{}); strings.Join(got, ",") != goWorkFile+","+goWorkSum {
			t.Fatalf("artefacts = %v", got)
		}
	})
}

func TestUndoReportsAGoWorkItCouldNotRestore(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	library := t.TempDir()
	writeGuardTestFile(t, worktree, goWorkFile, "go 1.27\n\nuse "+library+"\n")
	engine := &Engine{}
	failed := errors.New("git is gone")
	outcome, remaining := engine.undoLinksWith(context.Background(), worktree, []streams.Link{lgCovGoWorkLink(library)},
		headFiles{exists: func(string, string) (bool, error) { return false, failed }})
	if len(outcome.Errors) != 1 || len(remaining) != 1 {
		t.Fatalf("outcome = %#v, remaining = %#v; a failed restore keeps its record", outcome, remaining)
	}
}

func TestUndoWithoutARecordReportsAGoWorkItCouldNotClassify(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	writeGuardTestFile(t, worktree, goWorkFile, "go 1.27\n\nuse /elsewhere/library\n")
	failed := errors.New("git is gone")
	outcome := undoUnrecordedWith(worktree, headFiles{exists: func(string, string) (bool, error) { return false, failed }})
	if len(outcome.Errors) != 1 || !fileExists(filepath.Join(worktree, goWorkFile)) {
		t.Fatalf("outcome = %#v; want the failure reported and the file kept", outcome)
	}
}

func TestGitRestoreFromHEADReportsAFailure(t *testing.T) {
	t.Parallel()
	if err := gitRestoreFromHEAD(t.TempDir(), goWorkFile); err == nil {
		t.Fatal("restoring outside a repository reported success")
	}
}

// The `wb worktree end` half: a committed, unchanged workspace is repository
// content; untracked or modified ones still refuse with an honest remedy.

func TestHeadGitCommandIgnoresInheritedRepositoryOverrides(t *testing.T) {
	t.Parallel()
	kept := withoutRepositoryOverrides([]string{
		"PATH=/bin", "GIT_DIR=/elsewhere/.git", "GIT_INDEX_FILE=/elsewhere/index", "GIT_WORK_TREE=/elsewhere", "HOME=/home/x",
	})
	if strings.Join(kept, " ") != "PATH=/bin HOME=/home/x" {
		t.Fatalf("environment = %v, want only the non-repository variables", kept)
	}
	command := headGitCommand("/worktree", "status")
	for _, entry := range command.Env {
		if strings.HasPrefix(entry, "GIT_DIR=") {
			t.Fatalf("headGitCommand kept %s", entry)
		}
	}
	if strings.Join(command.Args, " ") != "git -C /worktree status" {
		t.Fatalf("args = %v", command.Args)
	}
}

// The Git diagnostic of the failed listing survives into the error.

type fakeLiveLinkStore struct{ links []streams.StreamLink }

func (store fakeLiveLinkStore) LiveLinksForWorktree(string) ([]streams.StreamLink, error) {
	return store.links, nil
}
