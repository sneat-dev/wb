package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestLocalState(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")

	// Empty repo, no commits, no changes -> clean.
	if dirty, reason, err := LocalState(dir); err != nil || dirty {
		t.Fatalf("empty repo: dirty=%v reason=%q err=%v, want clean", dirty, reason, err)
	}

	// Untracked file -> uncommitted changes.
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirty, reason, _ := LocalState(dir); !dirty || reason != "uncommitted changes" {
		t.Fatalf("untracked: dirty=%v reason=%q, want uncommitted changes", dirty, reason)
	}

	// Commit it -> tree clean, but the commit is on no remote -> unpushed.
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")
	if dirty, reason, _ := LocalState(dir); !dirty || reason != "unpushed commits" {
		t.Fatalf("committed/no-remote: dirty=%v reason=%q, want unpushed commits", dirty, reason)
	}
}

func TestPull(t *testing.T) {
	remoteDir := t.TempDir()
	git(t, remoteDir, "init", "-q", "--bare", "-b", "main")

	seed := t.TempDir()
	git(t, seed, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "f.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-qm", "v1")
	git(t, seed, "remote", "add", "origin", remoteDir)
	git(t, seed, "push", "-q", "origin", "main")

	clone := filepath.Join(t.TempDir(), "clone")
	cloneCmd := exec.Command("git", "clone", "-q", remoteDir, clone)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}

	if err := os.WriteFile(filepath.Join(seed, "f.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "commit", "-qam", "v2")
	git(t, seed, "push", "-q", "origin", "main")

	if err := Pull(clone); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(clone, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v2\n" {
		t.Fatalf("f.txt = %q, want v2", got)
	}
}

func TestStatusClean(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")

	s, err := Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.WorkingTreeDirty() || s.Dirty() {
		t.Fatalf("clean repo reported dirty: %+v", s)
	}
}

func TestStatusUntrackedAndModified(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")

	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Modified) != 1 || s.Modified[0] != "tracked.txt" {
		t.Errorf("Modified = %v, want [tracked.txt]", s.Modified)
	}
	if len(s.Untracked) != 1 || s.Untracked[0] != "untracked.txt" {
		t.Errorf("Untracked = %v, want [untracked.txt]", s.Untracked)
	}
	if !s.WorkingTreeDirty() || !s.Dirty() {
		t.Errorf("expected dirty, got %+v", s)
	}
}

func TestStatusStashAndUnpushed(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init") // unpushed: no remote at all

	s, err := Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.WorkingTreeDirty() {
		t.Errorf("expected clean working tree, got %+v", s)
	}
	if len(s.Unpushed) != 1 {
		t.Errorf("Unpushed = %v, want 1 entry", s.Unpushed)
	}
	if !s.Dirty() {
		t.Errorf("expected Dirty() true from unpushed commit")
	}

	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "stash", "-q")

	s, err = Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Stashed) != 1 {
		t.Errorf("Stashed = %v, want 1 entry", s.Stashed)
	}
}

func TestStatusConflict(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")

	git(t, dir, "checkout", "-qb", "other")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "commit", "-qam", "other change")

	git(t, dir, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "commit", "-qam", "main change")

	mergeCmd := exec.Command("git", "merge", "other")
	mergeCmd.Dir = dir
	mergeCmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	_, _ = mergeCmd.CombinedOutput() // expected to fail with a conflict

	s, err := Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Conflicted) != 1 || s.Conflicted[0] != "f.txt" {
		t.Fatalf("Conflicted = %v, want [f.txt]", s.Conflicted)
	}
	if !s.WorkingTreeDirty() || !s.Dirty() {
		t.Errorf("expected dirty from conflict, got %+v", s)
	}
}

func TestRepoStatusSummary(t *testing.T) {
	s := RepoStatus{
		Modified:  []string{"a.txt"},
		Untracked: []string{"b.txt", "c.txt"},
		Unpushed:  []string{"abc123 msg"},
	}
	got := s.Summary()
	want := "1 modified file, 2 untracked files, 1 unpushed commit"
	if got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}
