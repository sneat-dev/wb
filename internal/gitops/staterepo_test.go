package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setGitIdentity(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t", "GIT_CONFIG_GLOBAL": "/dev/null"} {
		t.Setenv(k, v)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// seededOriginAndClone returns a bare origin with one commit on main and a
// clone of it.
func seededOriginAndClone(t *testing.T) (origin, clone string) {
	t.Helper()
	setGitIdentity(t)
	origin = t.TempDir()
	gitIn(t, origin, "init", "-q", "--bare", "-b", "main")
	clone = filepath.Join(t.TempDir(), "clone")
	gitIn(t, t.TempDir(), "clone", "-q", origin, clone)
	gitIn(t, clone, "commit", "-q", "--allow-empty", "-m", "seed")
	gitIn(t, clone, "push", "-q", "origin", "main")
	return origin, clone
}

func TestAddCommitSkipsWhenNothingChanged(t *testing.T) {
	_, clone := seededOriginAndClone(t)
	if err := os.WriteFile(filepath.Join(clone, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if committed, err := AddCommit(clone, "add a", "a.txt"); err != nil || !committed {
		t.Fatalf("first AddCommit = (%v, %v), want (true, nil)", committed, err)
	}
	committed, err := AddCommit(clone, "nothing", "a.txt")
	if err != nil || committed {
		t.Fatalf("second AddCommit = (%v, %v), want (false, nil)", committed, err)
	}
}

func TestAddCommitPushAndHeadSHA(t *testing.T) {
	origin, clone := seededOriginAndClone(t)
	if err := os.WriteFile(filepath.Join(clone, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	committed, err := AddCommit(clone, "add a", "a.txt")
	if err != nil || !committed {
		t.Fatalf("AddCommit = (%v, %v), want (true, nil)", committed, err)
	}
	if err := Push(clone); err != nil {
		t.Fatal(err)
	}
	sha, err := HeadSHA(clone)
	if err != nil || len(sha) != 40 {
		t.Fatalf("HeadSHA = %q, %v", sha, err)
	}
	if got := gitIn(t, origin, "rev-parse", "main"); got != sha {
		t.Fatalf("origin main = %s, want %s", got, sha)
	}
}

func TestPullRebaseReplaysLocalCommitOnTopOfRemote(t *testing.T) {
	origin, clone := seededOriginAndClone(t)
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", "-q", origin, other)
	gitIn(t, other, "commit", "-q", "--allow-empty", "-m", "remote work")
	gitIn(t, other, "push", "-q", "origin", "main")

	gitIn(t, clone, "commit", "-q", "--allow-empty", "-m", "local work")
	if err := Push(clone); err == nil {
		t.Fatal("push should be rejected before rebase")
	}
	if err := PullRebase(clone); err != nil {
		t.Fatal(err)
	}
	if err := Push(clone); err != nil {
		t.Fatalf("push after rebase: %v", err)
	}
	if log := gitIn(t, origin, "log", "--format=%s", "main"); !strings.HasPrefix(log, "local work\nremote work") {
		t.Fatalf("origin log = %q", log)
	}
}

func TestAddCommitReportsInspectionFailure(t *testing.T) {
	setGitIdentity(t)
	notGitRepo := t.TempDir()
	filePath := filepath.Join(notGitRepo, "a.txt")
	if err := os.WriteFile(filePath, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	committed, err := AddCommit(notGitRepo, "add a", "a.txt")
	if err == nil {
		t.Fatalf("AddCommit on non-git repo should error, got committed=%v", committed)
	}
	if committed {
		t.Fatalf("AddCommit should return committed=false on error")
	}
}
