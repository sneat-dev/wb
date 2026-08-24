package gitops

import (
	"os/exec"
	"testing"
)

// pushedClone builds a clone with a real origin, so upstream tracking behaves
// exactly as it does in the fleet rather than being simulated.
func pushedClone(t *testing.T) (clone, origin string) {
	t.Helper()
	origin = t.TempDir()
	git(t, origin, "init", "-q", "--bare", "-b", "main", origin)

	seed := t.TempDir()
	git(t, seed, "init", "-q", "-b", "main", seed)
	git(t, seed, "commit", "-q", "--allow-empty", "-m", "seed")
	git(t, seed, "remote", "add", "origin", origin)
	git(t, seed, "push", "-q", "origin", "main")

	clone = t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", origin, clone).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	return clone, origin
}

// The case that made every completed pull request look like work at risk: a
// branch that was pushed, merged, and had its remote deleted.
func TestUnpushedCommitsIgnoresABranchWhoseUpstreamIsGone(t *testing.T) {
	clone, origin := pushedClone(t)

	git(t, clone, "checkout", "-q", "-b", "feature")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "feature work")
	git(t, clone, "push", "-q", "-u", "origin", "feature")

	// The merge deletes the remote branch, as --delete-branch does.
	git(t, origin, "update-ref", "-d", "refs/heads/feature")
	git(t, clone, "fetch", "-q", "--prune", "origin")

	commits, err := UnpushedCommits(clone)
	if err != nil {
		t.Fatalf("UnpushedCommits: %v", err)
	}
	if len(commits) != 0 {
		t.Fatalf("commits = %v, want none: the branch was pushed, so its work is not unpushed", commits)
	}
}

// A branch that was never pushed holds work that really does exist nowhere
// else, and must still be reported.
func TestUnpushedCommitsReportsABranchThatWasNeverPushed(t *testing.T) {
	clone, _ := pushedClone(t)

	git(t, clone, "checkout", "-q", "-b", "local-only")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "never pushed")

	commits, err := UnpushedCommits(clone)
	if err != nil {
		t.Fatalf("UnpushedCommits: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("commits = %v, want the one local-only commit", commits)
	}
}

// A branch whose upstream still exists and is behind holds genuinely unpushed
// work — the remindius case, which the fix must not silence.
func TestUnpushedCommitsReportsCommitsAheadOfALiveUpstream(t *testing.T) {
	clone, _ := pushedClone(t)

	git(t, clone, "checkout", "-q", "-b", "feature")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "pushed")
	git(t, clone, "push", "-q", "-u", "origin", "feature")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "not yet pushed")

	commits, err := UnpushedCommits(clone)
	if err != nil {
		t.Fatalf("UnpushedCommits: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("commits = %v, want only the commit ahead of the live upstream", commits)
	}
}

// A gone upstream must not hide unpushed work on other branches.
func TestUnpushedCommitsStillSeesOtherBranchesBesideAGoneOne(t *testing.T) {
	clone, origin := pushedClone(t)

	git(t, clone, "checkout", "-q", "-b", "merged")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "merged work")
	git(t, clone, "push", "-q", "-u", "origin", "merged")
	git(t, origin, "update-ref", "-d", "refs/heads/merged")
	git(t, clone, "fetch", "-q", "--prune", "origin")

	// Branch from main, not from the merged branch: a new effort starts from
	// the integration target, and branching off "merged" would legitimately
	// carry its commit along.
	git(t, clone, "checkout", "-q", "main")
	git(t, clone, "checkout", "-q", "-b", "live")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "real unpushed work")

	commits, err := UnpushedCommits(clone)
	if err != nil {
		t.Fatalf("UnpushedCommits: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("commits = %v, want only the live branch's unpushed commit", commits)
	}
}
