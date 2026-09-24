package agents

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initChangeRepo builds a small real repository so the change summary is
// exercised against actual Git output rather than a hand-written fixture that
// can drift from what Git prints.
func initChangeRepo(t *testing.T) (dir, baseSHA string) {
	t.Helper()
	dir = t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-qm", "initial")
	return dir, strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
}

func runGit(t *testing.T, dir string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func TestSummarizeChangesCountsTheArtefactAndNotAReview(t *testing.T) {
	t.Parallel()
	dir, base := initChangeRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("one\ntwo changed\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	summary := SummarizeChanges(context.Background(), dir, base)
	if summary == nil {
		t.Fatal("a worktree with changes must produce a summary")
	}
	if summary.FilesChanged != 2 {
		t.Fatalf("files changed = %d, want 2 (tracked and untracked)", summary.FilesChanged)
	}
	joined := strings.Join(summary.Files, ",")
	if !strings.Contains(joined, "tracked.txt") || !strings.Contains(joined, "untracked.txt") {
		t.Fatalf("changed files = %#v", summary.Files)
	}
	if summary.Insertions != 2 || summary.Deletions != 1 {
		t.Fatalf("line counts = +%d/-%d, want +2/-1", summary.Insertions, summary.Deletions)
	}
	if summary.Commits != 0 {
		t.Fatalf("commits = %d, want 0 before the worker commits anything", summary.Commits)
	}
}

func TestSummarizeChangesCountsCommitsTheWorkerMade(t *testing.T) {
	t.Parallel()
	dir, base := initChangeRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-qm", "worker commit")

	summary := SummarizeChanges(context.Background(), dir, base)
	if summary == nil || summary.Commits != 1 {
		t.Fatalf("summary = %#v, want one commit", summary)
	}
}

func TestSummarizeChangesDegradesInsteadOfFailingARun(t *testing.T) {
	t.Parallel()
	// No directory: WB must report no summary rather than fail a run whose
	// worktree is the actual artefact.
	if summary := SummarizeChanges(context.Background(), "", "abc"); summary != nil {
		t.Fatalf("an empty worktree directory must yield no summary: %#v", summary)
	}
	// A directory that is not a repository: Git fails, and the run still has a
	// truthful (empty) summary.
	summary := SummarizeChanges(context.Background(), t.TempDir(), "")
	if summary == nil {
		t.Fatal("a non-repository directory must still yield a summary object")
	}
	if summary.FilesChanged != 0 || len(summary.Files) != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	// An unusable base revision must not erase the porcelain-derived counts.
	dir, _ := initChangeRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	summary = SummarizeChanges(context.Background(), dir, "not-a-revision")
	if summary == nil || summary.FilesChanged != 1 {
		t.Fatalf("summary = %#v, want the untracked file still counted", summary)
	}
}

func TestSummarizeChangesBoundsTheChangedFileList(t *testing.T) {
	t.Parallel()
	dir, base := initChangeRepo(t)
	for index := 0; index < maxChangedFiles+5; index++ {
		name := filepath.Join(dir, fmt.Sprintf("file-%03d.txt", index))
		if err := os.WriteFile(name, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	summary := SummarizeChanges(context.Background(), dir, base)
	if summary == nil {
		t.Fatal("expected a summary")
	}
	if len(summary.Files) != maxChangedFiles || !summary.Truncated {
		t.Fatalf("file list must be capped and flagged: %d truncated=%v", len(summary.Files), summary.Truncated)
	}
	if summary.FilesChanged <= maxChangedFiles {
		t.Fatalf("the count must stay exact even when the list is capped: %d", summary.FilesChanged)
	}
}

func TestParseShortstatReadsGitOutput(t *testing.T) {
	t.Parallel()
	cases := map[string][2]int{
		" 3 files changed, 12 insertions(+), 4 deletions(-)": {12, 4},
		" 1 file changed, 2 insertions(+)":                   {2, 0},
		" 1 file changed, 1 deletion(-)":                     {0, 1},
		"":                                                   {0, 0},
		" nonsense, 4 deletions(-)":                          {0, 4},
	}
	for input, want := range cases {
		insertions, deletions := parseShortstat(input)
		if insertions != want[0] || deletions != want[1] {
			t.Errorf("parseShortstat(%q) = %d/%d, want %d/%d", input, insertions, deletions, want[0], want[1])
		}
	}
}

func TestGitOutputReportsFailures(t *testing.T) {
	t.Parallel()
	if _, err := gitOutput(context.Background(), t.TempDir(), "rev-parse", "HEAD"); err == nil {
		t.Fatal("a Git failure must be reported")
	}
}
