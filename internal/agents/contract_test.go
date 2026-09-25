//go:build e2e

// SummarizeChanges' contract tests run against real git, proving the
// porcelain/shortstat parsing this package's fake-backed unit tests pin
// down (changes_test.go) actually matches what real git prints, rather than
// a hand-written fixture that could drift from it. These tests never run in
// the default `go test ./...` tier -- only under the e2e tag -- so they
// carry no unit_tier.pending entry.
package agents

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// initChangeRepo builds a small real repository so the change summary is
// exercised against actual Git output.
func initChangeRepo(t *testing.T) (dir, baseSHA string) {
	t.Helper()
	dir = t.TempDir()
	contractRunGit(t, dir, "init", "-b", "main")
	contractRunGit(t, dir, "config", "user.email", "test@example.com")
	contractRunGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	contractRunGit(t, dir, "add", "-A")
	contractRunGit(t, dir, "commit", "-qm", "initial")
	return dir, strings.TrimSpace(contractRunGit(t, dir, "rev-parse", "HEAD"))
}

func contractRunGit(t *testing.T, dir string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func TestContractSummarizeChangesCountsTheArtefactAndNotAReview(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
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

func TestContractSummarizeChangesCountsCommitsTheWorkerMade(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir, base := initChangeRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	contractRunGit(t, dir, "add", "-A")
	contractRunGit(t, dir, "commit", "-qm", "worker commit")

	summary := SummarizeChanges(context.Background(), dir, base)
	if summary == nil || summary.Commits != 1 {
		t.Fatalf("summary = %#v, want one commit", summary)
	}
}

func TestContractSummarizeChangesDegradesInsteadOfFailingARun(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
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

func TestContractGitOutputReportsFailures(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := gitOutput(context.Background(), realRunner(), t.TempDir(), "rev-parse", "HEAD"); err == nil {
		t.Fatal("a Git failure must be reported")
	}
}

// TestContractSpawnOwnerStartsADetachedProcessAndReportsItsPID proves the
// production wiring (SpawnOwner -> realRunner() -> runner.Real.Detach)
// actually starts and releases a real detached process, the piece
// owner_test.go's fake-backed TestSpawnOwnerStartsADetachedProcessAndReportsItsPID
// cannot exercise.
func TestContractSpawnOwnerStartsADetachedProcessAndReportsItsPID(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the detached owner uses a POSIX session")
	}
	runDir := t.TempDir()
	pid, err := SpawnOwner(runDir, func() (string, error) { return "/bin/sleep", nil })
	if err != nil {
		t.Fatalf("SpawnOwner: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("SpawnOwner returned pid %d", pid)
	}
	// The owner is released rather than waited on, so it may briefly outlive
	// this call; it must not become a zombie this process has to reap.
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	if _, err := SpawnOwner(runDir, func() (string, error) { return "", os.ErrNotExist }); err == nil {
		t.Fatal("an unresolvable executable must be reported")
	}
	if _, err := SpawnOwner(runDir, func() (string, error) { return "/nonexistent/wb", nil }); err == nil {
		t.Fatal("a failed exec must be reported")
	}
}
