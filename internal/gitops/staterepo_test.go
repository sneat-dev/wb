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

// TestRebaseAbortClearsConflictAndKeepsLocalCommit creates a genuine
// rebase conflict (two clones editing the same line of the same file, one
// pushes, the other commits locally and then PullRebase fails) and proves
// RebaseAbort clears the in-progress rebase without discarding the local
// commit.
func TestRebaseAbortClearsConflictAndKeepsLocalCommit(t *testing.T) {
	origin, clone := seededOriginAndClone(t)

	// Seed a shared file both sides will edit, so the conflict is a real
	// content collision rather than an add/add conflict.
	if err := os.WriteFile(filepath.Join(clone, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "add", "shared.txt")
	gitIn(t, clone, "commit", "-q", "-m", "seed shared.txt")
	gitIn(t, clone, "push", "-q", "origin", "main")

	// A second clone edits the same line and pushes first.
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", "-q", origin, other)
	if err := os.WriteFile(filepath.Join(other, "shared.txt"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "commit", "-q", "-am", "remote change")
	gitIn(t, other, "push", "-q", "origin", "main")

	// The original clone edits the same line differently and commits
	// locally, without fetching first, so the upcoming rebase collides.
	if err := os.WriteFile(filepath.Join(clone, "shared.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "commit", "-q", "-am", "local change")
	localHead := gitIn(t, clone, "rev-parse", "HEAD")

	if err := PullRebase(clone); err == nil {
		t.Fatal("PullRebase should fail on conflict")
	}

	if err := RebaseAbort(clone); err != nil {
		t.Fatalf("RebaseAbort: %v", err)
	}

	if _, err := os.Stat(filepath.Join(clone, ".git", "rebase-merge")); !os.IsNotExist(err) {
		t.Fatalf(".git/rebase-merge still present after abort: err=%v", err)
	}
	if _, err := run(clone, "git", "rev-parse", "--verify", "REBASE_HEAD"); err == nil {
		t.Fatal("REBASE_HEAD should not resolve after abort")
	}
	if head := gitIn(t, clone, "rev-parse", "HEAD"); head != localHead {
		t.Fatalf("HEAD = %s, want local commit %s preserved", head, localHead)
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

func TestHasUpstreamTrueWhenBranchTracksRemote(t *testing.T) {
	_, clone := seededOriginAndClone(t)
	has, err := HasUpstream(clone)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("HasUpstream = false, want true for a freshly cloned branch")
	}
}

func TestHasUpstreamFalseWhenBranchHasNoUpstream(t *testing.T) {
	setGitIdentity(t)
	local := t.TempDir()
	gitIn(t, local, "init", "-q", "-b", "main")
	gitIn(t, local, "commit", "-q", "--allow-empty", "-m", "seed")
	has, err := HasUpstream(local)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("HasUpstream = true, want false: no remote is configured at all")
	}
}

func TestHasUpstreamErrorsOnNonRepo(t *testing.T) {
	setGitIdentity(t)
	notGitRepo := t.TempDir()
	if _, err := HasUpstream(notGitRepo); err == nil {
		t.Fatal("HasUpstream on a non-git directory should error, not report false")
	}
}

func TestRebaseInProgressFalseNormally(t *testing.T) {
	_, clone := seededOriginAndClone(t)
	inProgress, err := RebaseInProgress(clone)
	if err != nil {
		t.Fatal(err)
	}
	if inProgress {
		t.Fatal("RebaseInProgress = true, want false: no rebase was started")
	}
}

// TestRebaseInProgressTrueDuringConflict reuses the same conflict setup as
// TestRebaseAbortClearsConflictAndKeepsLocalCommit to prove RebaseInProgress
// detects the in-progress rebase before it is aborted, and reports false
// again afterward.
func TestRebaseInProgressTrueDuringConflict(t *testing.T) {
	origin, clone := seededOriginAndClone(t)

	if err := os.WriteFile(filepath.Join(clone, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "add", "shared.txt")
	gitIn(t, clone, "commit", "-q", "-m", "seed shared.txt")
	gitIn(t, clone, "push", "-q", "origin", "main")

	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", "-q", origin, other)
	if err := os.WriteFile(filepath.Join(other, "shared.txt"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "commit", "-q", "-am", "remote change")
	gitIn(t, other, "push", "-q", "origin", "main")

	if err := os.WriteFile(filepath.Join(clone, "shared.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "commit", "-q", "-am", "local change")

	if err := PullRebase(clone); err == nil {
		t.Fatal("PullRebase should fail on conflict")
	}

	inProgress, err := RebaseInProgress(clone)
	if err != nil {
		t.Fatal(err)
	}
	if !inProgress {
		t.Fatal("RebaseInProgress = false, want true: a rebase is mid-conflict")
	}

	if err := RebaseAbort(clone); err != nil {
		t.Fatal(err)
	}
	inProgress, err = RebaseInProgress(clone)
	if err != nil {
		t.Fatal(err)
	}
	if inProgress {
		t.Fatal("RebaseInProgress = true after abort, want false")
	}
}

// TestConfiguredOriginURLDiffersFromOriginURLAfterInsteadOf proves that
// ConfiguredOriginURL returns the original remote.origin.url before url.<base>.insteadOf
// rewriting, while OriginURL returns the rewritten URL.
func TestConfiguredOriginURLDiffersFromOriginURLAfterInsteadOf(t *testing.T) {
	origin, clone := seededOriginAndClone(t)

	// Get the original configured URL
	configuredBefore, err := ConfiguredOriginURL(clone)
	if err != nil {
		t.Fatalf("ConfiguredOriginURL: %v", err)
	}
	originBefore, err := OriginURL(clone)
	if err != nil {
		t.Fatalf("OriginURL: %v", err)
	}
	if configuredBefore != originBefore {
		t.Fatalf("before rewrite: ConfiguredOriginURL=%q, OriginURL=%q, want equal", configuredBefore, originBefore)
	}

	// Now set up a url.<somewhere>.insteadOf rewrite in the clone's local config
	tmpRewrite := filepath.Join(t.TempDir(), "elsewhere")
	gitIn(t, clone, "config", "url."+tmpRewrite+".insteadOf", origin)

	// OriginURL should return the rewritten URL
	rewrittenOriginURL, err := OriginURL(clone)
	if err != nil {
		t.Fatalf("OriginURL after rewrite: %v", err)
	}
	if rewrittenOriginURL != tmpRewrite {
		t.Fatalf("OriginURL after rewrite = %q, want %q", rewrittenOriginURL, tmpRewrite)
	}

	// ConfiguredOriginURL should still return the original URL
	configuredAfter, err := ConfiguredOriginURL(clone)
	if err != nil {
		t.Fatalf("ConfiguredOriginURL after rewrite: %v", err)
	}
	if configuredAfter != origin {
		t.Fatalf("ConfiguredOriginURL after rewrite = %q, want original %q", configuredAfter, origin)
	}
}

// TestResetHardUpstreamDiscardsLocalCommitAndDirt proves ResetHardUpstream
// returns the clone to exactly @{u}: a local commit the upstream never saw,
// plus an uncommitted change to a tracked file, are both discarded and HEAD
// lands on the upstream SHA with a clean tracked-file state. This is the
// primitive claims CAS uses to roll back a commit that lost its race.
// (`git reset --hard` deliberately does not touch untracked files, so this
// does not claim to remove those — only tracked-file and commit state.)
func TestResetHardUpstreamDiscardsLocalCommitAndDirt(t *testing.T) {
	origin, clone := seededOriginAndClone(t)

	// Seed a tracked file both sides know about, then push, so there is a
	// tracked file to dirty locally afterward.
	if err := os.WriteFile(filepath.Join(clone, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "add", "tracked.txt")
	gitIn(t, clone, "commit", "-q", "-m", "seed tracked.txt")
	gitIn(t, clone, "push", "-q", "origin", "main")
	upstreamSHA := gitIn(t, origin, "rev-parse", "main")

	// A local-only commit @{u} never saw.
	gitIn(t, clone, "commit", "-q", "--allow-empty", "-m", "local only")
	// Plus an uncommitted change to the tracked file.
	if err := os.WriteFile(filepath.Join(clone, "tracked.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ResetHardUpstream(clone); err != nil {
		t.Fatalf("ResetHardUpstream: %v", err)
	}

	if head := gitIn(t, clone, "rev-parse", "HEAD"); head != upstreamSHA {
		t.Fatalf("HEAD = %s, want upstream %s", head, upstreamSHA)
	}
	if status := gitIn(t, clone, "status", "--porcelain"); status != "" {
		t.Fatalf("status = %q, want clean tree after reset", status)
	}
	content, err := os.ReadFile(filepath.Join(clone, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "base\n" {
		t.Fatalf("tracked.txt = %q, want the upstream content restored", content)
	}
}
