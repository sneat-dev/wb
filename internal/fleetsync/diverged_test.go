package fleetsync

import (
	"os/exec"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

// divergedClone reproduces the third shape that made sync red: a canonical
// clone holding a commit origin does not have, whose upstream has meanwhile
// moved on. `git pull` there fails with git's multi-line "Need to specify how
// to reconcile divergent branches" hint, which used to be reported as a
// fleet-wide error.
func divergedClone(t *testing.T) (discover.Repo, string) {
	t.Helper()
	origin := t.TempDir()
	git(t, origin, "init", "-q", "--bare", "-b", "main")

	seed := t.TempDir()
	git(t, seed, "init", "-q", "-b", "main")
	git(t, seed, "remote", "add", "origin", origin)
	git(t, seed, "commit", "-q", "--allow-empty", "-m", "base")
	git(t, seed, "push", "-q", "origin", "main")

	local := t.TempDir()
	clone(t, origin, local)
	git(t, local, "commit", "-q", "--allow-empty", "-m", "local only")

	// The upstream moves on independently.
	git(t, seed, "commit", "-q", "--allow-empty", "-m", "remote only")
	git(t, seed, "push", "-q", "origin", "main")

	return discover.Repo{Org: "acme", Name: "widgets", Path: local, Remote: true}, local
}

func clone(t *testing.T, origin, dest string) {
	t.Helper()
	cmd := exec.Command("git", "clone", "-q", origin, dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
}

func TestSyncReportsDivergenceRatherThanFailing(t *testing.T) {
	repo, local := divergedClone(t)
	before := revParse(t, local, "HEAD")

	res := Sync(repo, "", false)

	if res.Status != Diverged {
		t.Fatalf("Status = %v (err=%v), want Diverged", res.Status, res.Err)
	}
	if res.Err != nil {
		t.Fatalf("Err = %v, want nil; a divergence is a decision to make, not a fault", res.Err)
	}
	if res.Tracking.Ahead != 1 || res.Tracking.Behind != 1 {
		t.Fatalf("Tracking = %d ahead, %d behind, want 1 and 1 — the report is useless without counts",
			res.Tracking.Ahead, res.Tracking.Behind)
	}
	if got := revParse(t, local, "HEAD"); got != before {
		t.Fatalf("sync rewrote a diverged clone: HEAD %s -> %s", before, got)
	}
}

// The local commit must survive: --ff-only means sync can never absorb a
// divergence into a merge commit, whatever pull.rebase says on this machine.
func TestSyncLeavesDivergedLocalCommitInPlace(t *testing.T) {
	repo, local := divergedClone(t)
	git(t, local, "config", "pull.rebase", "false")

	if res := Sync(repo, "", false); res.Status != Diverged {
		t.Fatalf("Status = %v (err=%v), want Diverged", res.Status, res.Err)
	}

	cmd := exec.Command("git", "log", "--oneline", "@{u}..HEAD")
	cmd.Dir = local
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v: %s", err, out)
	}
	if len(out) == 0 {
		t.Fatal("the local-only commit is gone; sync must never reconcile a divergence itself")
	}
}

// A branch tracking nothing has nowhere to pull from. Reportable, not fatal.
func TestSyncReportsNoUpstreamRatherThanFailing(t *testing.T) {
	repo, local := divergedClone(t)
	git(t, local, "switch", "-q", "-c", "detour")

	res := Sync(repo, "", false)

	if res.Status != NoUpstream {
		t.Fatalf("Status = %v (err=%v), want NoUpstream", res.Status, res.Err)
	}
	if res.Err != nil {
		t.Fatalf("Err = %v, want nil", res.Err)
	}
	if res.Tracking.Branch != "detour" {
		t.Fatalf("Tracking.Branch = %q, want %q", res.Tracking.Branch, "detour")
	}
}

// Behind-only is the ordinary case; the new classification must not stop it
// from fast-forwarding.
func TestSyncStillFastForwardsWhenOnlyBehind(t *testing.T) {
	origin := t.TempDir()
	git(t, origin, "init", "-q", "--bare", "-b", "main")
	seed := t.TempDir()
	git(t, seed, "init", "-q", "-b", "main")
	git(t, seed, "remote", "add", "origin", origin)
	git(t, seed, "commit", "-q", "--allow-empty", "-m", "base")
	git(t, seed, "push", "-q", "origin", "main")

	local := t.TempDir()
	clone(t, origin, local)
	git(t, seed, "commit", "-q", "--allow-empty", "-m", "newer")
	git(t, seed, "push", "-q", "origin", "main")

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: local, Remote: true}
	if res := Sync(repo, "", false); res.Status != Pulled {
		t.Fatalf("Status = %v (err=%v), want Pulled", res.Status, res.Err)
	}
	if got := revParse(t, local, "HEAD"); got != revParse(t, seed, "HEAD") {
		t.Fatal("clone was not fast-forwarded to the upstream commit")
	}
}

func TestDivergedAndNoUpstreamStatusStrings(t *testing.T) {
	if got := Diverged.String(); got != "diverged" {
		t.Fatalf("Diverged.String() = %q, want %q", got, "diverged")
	}
	if got := NoUpstream.String(); got != "no upstream" {
		t.Fatalf("NoUpstream.String() = %q, want %q", got, "no upstream")
	}
}

func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse %s: %v: %s", ref, err, out)
	}
	return string(out)
}
