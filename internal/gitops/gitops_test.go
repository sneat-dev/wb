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
