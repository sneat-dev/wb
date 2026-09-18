package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// DefaultBranch must follow origin/HEAD after a refresh, which is the path a
// normal clone takes.
func TestLgCovDefaultBranchFollowsOriginHEAD(t *testing.T) {
	_, clone := lgCovSeededClone(t)

	got, err := DefaultBranch(clone)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "main" {
		t.Fatalf("DefaultBranch = %q, want main", got)
	}
}

// Fetch must leave the local origin/main ref pointing at the remote's current
// tip, which is what makes a later Land work off fresh history.
func TestLgCovFetchUpdatesRemoteTrackingRefs(t *testing.T) {
	origin, clone := lgCovSeededClone(t)
	before := gitIn(t, clone, "rev-parse", "origin/main")

	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", "-q", origin, other)
	gitIn(t, other, "commit", "-q", "--allow-empty", "-m", "remote only")
	gitIn(t, other, "push", "-q", "origin", "main")
	remoteTip := gitIn(t, other, "rev-parse", "HEAD")

	if err := Fetch(clone); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := gitIn(t, clone, "rev-parse", "origin/main"); got != remoteTip {
		t.Fatalf("origin/main after Fetch = %s, want %s (was %s)", got, remoteTip, before)
	}
}

func TestLgCovFetchErrorsOutsideRepository(t *testing.T) {
	lgCovGitIdentity(t)
	if err := Fetch(t.TempDir()); err == nil {
		t.Fatal("Fetch outside a git repository should error")
	}
}

// ShowFile reads a blob at a ref, and reports missing paths as ok=false with a
// nil error because absence is an expected answer, not a fault.
func TestLgCovShowFile(t *testing.T) {
	_, clone := lgCovSeededClone(t)

	content, ok, err := ShowFile(clone, "origin/main", "seed.txt")
	if err != nil {
		t.Fatalf("ShowFile: %v", err)
	}
	if !ok || content != "seed\n" {
		t.Fatalf("ShowFile = (%q, %v), want (seed newline, true)", content, ok)
	}

	content, ok, err = ShowFile(clone, "origin/main", "does-not-exist.txt")
	if err != nil {
		t.Fatalf("ShowFile missing path returned an error: %v", err)
	}
	if ok || content != "" {
		t.Fatalf("ShowFile missing path = (%q, %v), want (\"\", false)", content, ok)
	}

	content, ok, err = ShowFile(clone, "no-such-ref", "seed.txt")
	if err != nil {
		t.Fatalf("ShowFile unknown ref returned an error: %v", err)
	}
	if ok || content != "" {
		t.Fatalf("ShowFile unknown ref = (%q, %v), want (\"\", false)", content, ok)
	}
}

// Clone creates missing parent directories and produces a usable checkout of
// the source.
func TestLgCovCloneCreatesParentDirectories(t *testing.T) {
	origin, _ := lgCovSeededClone(t)

	dest := filepath.Join(t.TempDir(), "nested", "deeper", "clone")
	if err := Clone(origin, dest); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		t.Fatalf("cloned checkout has no .git: %v", err)
	}
	content, ok, err := ShowFile(dest, "HEAD", "seed.txt")
	if err != nil || !ok || content != "seed\n" {
		t.Fatalf("ShowFile in clone = (%q, %v, %v), want the seed content", content, ok, err)
	}
}

func TestLgCovCloneErrorsOnUnreachableSource(t *testing.T) {
	lgCovGitIdentity(t)
	dest := filepath.Join(t.TempDir(), "clone")
	err := Clone(filepath.Join(t.TempDir(), "missing-origin"), dest)
	if err == nil {
		t.Fatal("Clone of a nonexistent source should error")
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("Clone reported an error yet left a destination behind")
	}
}

// A destination whose parent path is a regular file cannot be created, and the
// filesystem error must be returned rather than swallowed.
func TestLgCovCloneErrorsWhenParentCannotBeCreated(t *testing.T) {
	lgCovGitIdentity(t)
	blocker := lgCovWriteFile(t, t.TempDir(), "blocker", "not a directory\n")

	err := Clone("anywhere", filepath.Join(blocker, "sub", "clone"))
	if err == nil {
		t.Fatal("Clone should error when the destination parent cannot be created")
	}
}

// WorktreeChanged distinguishes a dirty worktree from a clean one and surfaces
// git's own failure outside a repository.
func TestLgCovWorktreeChanged(t *testing.T) {
	_, clone := lgCovSeededClone(t)

	changed, err := WorktreeChanged(clone)
	if err != nil {
		t.Fatalf("WorktreeChanged clean: %v", err)
	}
	if changed {
		t.Fatal("WorktreeChanged = true for a freshly cloned, clean worktree")
	}

	lgCovWriteFile(t, clone, "untracked.txt", "new\n")
	changed, err = WorktreeChanged(clone)
	if err != nil {
		t.Fatalf("WorktreeChanged dirty: %v", err)
	}
	if !changed {
		t.Fatal("WorktreeChanged = false after adding an untracked file")
	}

	if _, err := WorktreeChanged(t.TempDir()); err == nil {
		t.Fatal("WorktreeChanged outside a git repository should error")
	}
}

func TestLgCovHasCommits(t *testing.T) {
	lgCovGitIdentity(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")

	has, err := HasCommits(dir)
	if err != nil {
		t.Fatalf("HasCommits on an unborn branch: %v", err)
	}
	if has {
		t.Fatal("HasCommits = true on an unborn branch, want false")
	}

	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "first")
	has, err = HasCommits(dir)
	if err != nil {
		t.Fatalf("HasCommits after a commit: %v", err)
	}
	if !has {
		t.Fatal("HasCommits = false after a commit, want true")
	}

	if _, err := HasCommits(t.TempDir()); err == nil {
		t.Fatal("HasCommits outside a git repository should error, not report false")
	}
}

func TestLgCovCommitEmptyCreatesACommit(t *testing.T) {
	lgCovGitIdentity(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")

	if err := CommitEmpty(dir, "empty init"); err != nil {
		t.Fatalf("CommitEmpty: %v", err)
	}
	if got := gitIn(t, dir, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("commit count = %s, want 1", got)
	}
	if got := gitIn(t, dir, "log", "-1", "--format=%s"); got != "empty init" {
		t.Fatalf("HEAD subject = %q, want empty init", got)
	}

	if err := CommitEmpty(t.TempDir(), "nope"); err == nil {
		t.Fatal("CommitEmpty outside a git repository should error")
	}
}

func TestLgCovPushSetUpstreamPublishesAndTracks(t *testing.T) {
	lgCovGitIdentity(t)
	origin := t.TempDir()
	gitIn(t, origin, "init", "-q", "--bare", "-b", "main")

	local := t.TempDir()
	gitIn(t, local, "init", "-q", "-b", "main")
	gitIn(t, local, "remote", "add", "origin", origin)
	gitIn(t, local, "commit", "-q", "--allow-empty", "-m", "first")
	head := gitIn(t, local, "rev-parse", "HEAD")

	if err := PushSetUpstream(local, "main"); err != nil {
		t.Fatalf("PushSetUpstream: %v", err)
	}
	if got := gitIn(t, origin, "rev-parse", "main"); got != head {
		t.Fatalf("origin/main = %s, want the pushed HEAD %s", got, head)
	}
	if got := gitIn(t, local, "rev-parse", "--abbrev-ref", "@{u}"); got != "origin/main" {
		t.Fatalf("upstream = %q, want origin/main", got)
	}

	if err := PushSetUpstream(t.TempDir(), "main"); err == nil {
		t.Fatal("PushSetUpstream outside a git repository should error")
	}
}

// CurrentBranch reports an empty name for a detached HEAD and an error when the
// directory is not a repository at all.
func TestLgCovCurrentBranchDetachedAndError(t *testing.T) {
	_, clone := lgCovSeededClone(t)

	if got, err := CurrentBranch(clone); err != nil || got != "main" {
		t.Fatalf("CurrentBranch = (%q, %v), want (main, nil)", got, err)
	}

	gitIn(t, clone, "checkout", "-q", "--detach", "HEAD")
	if got, err := CurrentBranch(clone); err != nil || got != "" {
		t.Fatalf("CurrentBranch detached = (%q, %v), want (\"\", nil)", got, err)
	}

	if _, err := CurrentBranch(t.TempDir()); err == nil {
		t.Fatal("CurrentBranch outside a git repository should error")
	}
}

func TestLgCovOriginURLErrorsOutsideRepository(t *testing.T) {
	lgCovGitIdentity(t)
	if _, err := OriginURL(t.TempDir()); err == nil {
		t.Fatal("OriginURL outside a git repository should error")
	}
}

func TestLgCovConfiguredOriginURLErrorsWhenUnset(t *testing.T) {
	lgCovGitIdentity(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	if _, err := ConfiguredOriginURL(dir); err == nil {
		t.Fatal("ConfiguredOriginURL with no origin remote should error")
	}
}

// HasUpstream (like HasCommits) distinguishes an unborn/no-upstream branch from
// a genuine git failure.
func TestLgCovHasUpstreamErrorsOutsideRepository(t *testing.T) {
	lgCovGitIdentity(t)
	if _, err := HasUpstream(t.TempDir()); err == nil {
		t.Fatal("HasUpstream outside a git repository should error")
	}
}

// RebaseInProgress resolves its probe paths with git rev-parse, so outside a
// repository the rev-parse failure must be returned, not read as "no rebase".
func TestLgCovRebaseInProgressErrorsOutsideRepository(t *testing.T) {
	lgCovGitIdentity(t)
	inProgress, err := RebaseInProgress(t.TempDir())
	if err == nil {
		t.Fatal("RebaseInProgress outside a git repository should error")
	}
	if inProgress {
		t.Fatal("RebaseInProgress = true on error, want false")
	}
}

// PullRebase names the clone path when the current branch cannot be determined
// and refuses to run against a detached HEAD, because there is no branch to
// replay onto an upstream.
func TestLgCovPullRebaseErrorsOutsideRepositoryAndDetached(t *testing.T) {
	lgCovGitIdentity(t)

	if err := PullRebase(t.TempDir()); err == nil {
		t.Fatal("PullRebase outside a git repository should error")
	} else if !strings.Contains(err.Error(), "determine current branch") {
		t.Fatalf("error = %q, want it to name the branch lookup failure", err.Error())
	}

	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "first")
	gitIn(t, dir, "checkout", "-q", "--detach", "HEAD")

	err := PullRebase(dir)
	if err == nil {
		t.Fatal("PullRebase on a detached HEAD should error")
	}
	if !strings.Contains(err.Error(), "detached HEAD") || !strings.Contains(err.Error(), dir) {
		t.Fatalf("error = %q, want it to name %s and the detached HEAD", err.Error(), dir)
	}
}

// A fetch that cannot reach origin must fail with the ref and the clone named,
// rather than falling through to a rebase against a stale upstream.
func TestLgCovPullRebaseReportsFetchFailure(t *testing.T) {
	lgCovGitIdentity(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "first")
	gitIn(t, dir, "remote", "add", "origin", filepath.Join(t.TempDir(), "missing-origin"))

	err := PullRebase(dir)
	if err == nil {
		t.Fatal("PullRebase with an unreachable origin should error")
	}
	if !strings.Contains(err.Error(), "fetch origin main") {
		t.Fatalf("error = %q, want it to name the fetch that failed", err.Error())
	}
}

// Tracking reports an empty branch for a detached HEAD and says so in Summary,
// instead of failing or rendering a blank name.
func TestLgCovTrackingDetachedHead(t *testing.T) {
	_, clone := lgCovSeededClone(t)
	gitIn(t, clone, "checkout", "-q", "--detach", "HEAD")

	got, err := Tracking(clone)
	if err != nil {
		t.Fatalf("Tracking: %v", err)
	}
	if got.Branch != "" || got.Upstream != "" {
		t.Fatalf("Tracking detached = %+v, want no branch and no upstream", got)
	}
	if want := "detached HEAD"; got.Summary() != want {
		t.Fatalf("Summary() = %q, want %q", got.Summary(), want)
	}
}
