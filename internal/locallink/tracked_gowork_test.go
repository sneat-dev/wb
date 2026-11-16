package locallink

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
)

// committedWorkspace is a repository's own multi-module workspace — the shape
// sneat-co/commitius commits at its root.
const committedWorkspace = "go 1.27\n\nuse ./backend\n"

type fakeLiveLinkStore struct{ links []streams.StreamLink }

func (store fakeLiveLinkStore) LiveLinksForWorktree(string) ([]streams.StreamLink, error) {
	return store.links, nil
}

func TestClassifyGoWorkReportsProvenanceAndGitFailures(t *testing.T) {
	t.Parallel()
	failed := errors.New("git is gone")
	yes := func(string, string) (bool, error) { return true, nil }
	no := func(string, string) (bool, error) { return false, nil }
	broken := func(string, string) (bool, error) { return false, failed }
	cases := []struct {
		name      string
		files     headFiles
		want      GoWorkProvenance
		wantError bool
	}{
		{"untracked", headFiles{exists: no}, GoWorkUntracked, false},
		{"committed", headFiles{exists: yes, unchanged: yes}, GoWorkCommitted, false},
		{"modified", headFiles{exists: yes, unchanged: no}, GoWorkModified, false},
		{"presence unknown", headFiles{exists: broken}, "", true},
		{"difference unknown", headFiles{exists: yes, unchanged: broken}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.files.classify("/worktree", goWorkFile)
			if (err != nil) != tc.wantError || got != tc.want {
				t.Fatalf("classify = (%q, %v), want (%q, error %v)", got, err, tc.want, tc.wantError)
			}
		})
	}
}

func TestHasLiveLinkReportsAGoWorkWhoseProvenanceCannotBeRead(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	writeGuardTestFile(t, worktree, goWorkFile, "go 1.27\n\nuse /elsewhere/library\n")
	failed := errors.New("git is gone")
	_, err := hasLiveLinkWith(nil, worktree, headFiles{exists: func(string, string) (bool, error) { return false, failed }})
	if !errors.Is(err, failed) {
		t.Fatalf("err = %v, want the provenance failure", err)
	}
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

// An inherited GIT_DIR or index would aim the HEAD probes at another
// repository, where a tracked go.work reads as absent; they are stripped.
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
