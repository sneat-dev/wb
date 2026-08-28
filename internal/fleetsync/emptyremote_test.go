package fleetsync

import (
	"context"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

// cloneOfEmptyRemote reproduces the shape that made sync red on every run: a
// repository created on the host but never pushed to, cloned locally, with a
// branch configured to track a ref the remote does not publish.
func cloneOfEmptyRemote(t *testing.T) discover.Repo {
	t.Helper()
	origin := t.TempDir()
	git(t, origin, "init", "-q", "--bare", "-b", "main")

	local := t.TempDir()
	git(t, local, "init", "-q", "-b", "main")
	git(t, local, "remote", "add", "origin", origin)
	git(t, local, "config", "branch.main.remote", "origin")
	git(t, local, "config", "branch.main.merge", "refs/heads/main")

	return discover.Repo{Org: "acme", Name: "widgets", Path: local, Remote: true}
}

func TestSyncReportsEmptyRemoteRatherThanFailing(t *testing.T) {
	repo := cloneOfEmptyRemote(t)

	res := Sync(context.Background(), repo, "", false, false)

	if res.Status != EmptyRemote {
		t.Fatalf("Status = %v (err=%v), want EmptyRemote", res.Status, res.Err)
	}
	if res.Err != nil {
		t.Fatalf("Err = %v, want nil; an unpushed remote is not a fault", res.Err)
	}
}

// The moment the remote has content, the same repository must sync normally
// with no marker to clear and no human step. This is the self-healing
// property that makes the detection preferable to a manual skip marker.
func TestSyncPullsOnceRemoteHasBranches(t *testing.T) {
	repo := cloneOfEmptyRemote(t)

	// Someone pushes the first commit.
	git(t, repo.Path, "commit", "-q", "--allow-empty", "-m", "first")
	git(t, repo.Path, "push", "-q", "origin", "main")

	res := Sync(context.Background(), repo, "", false, false)

	if res.Status != Pulled {
		t.Fatalf("Status = %v (err=%v), want Pulled once the remote has a branch", res.Status, res.Err)
	}
}

// A remote that publishes branches, just not the tracked one, is a renamed or
// deleted branch. That needs a human and must not be absorbed as benign.
func TestSyncStillFailsWhenTrackedBranchIsMissing(t *testing.T) {
	origin := t.TempDir()
	git(t, origin, "init", "-q", "--bare", "-b", "main")

	seed := t.TempDir()
	git(t, seed, "init", "-q", "-b", "main")
	git(t, seed, "remote", "add", "origin", origin)
	git(t, seed, "commit", "-q", "--allow-empty", "-m", "seed")
	git(t, seed, "push", "-q", "origin", "main")

	// Local tracks refs/heads/master, which origin does not publish.
	local := t.TempDir()
	git(t, local, "init", "-q", "-b", "master")
	git(t, local, "remote", "add", "origin", origin)
	git(t, local, "config", "branch.master.remote", "origin")
	git(t, local, "config", "branch.master.merge", "refs/heads/master")
	repo := discover.Repo{Org: "acme", Name: "widgets", Path: local, Remote: true}

	res := Sync(context.Background(), repo, "", false, false)

	if res.Status != Failed {
		t.Fatalf("Status = %v, want Failed; a missing tracked branch on a populated remote is a real problem", res.Status)
	}
	if res.Err == nil {
		t.Fatal("Err = nil, want the underlying git error")
	}
}

func TestEmptyRemoteStatusString(t *testing.T) {
	if got := EmptyRemote.String(); got != "empty remote" {
		t.Fatalf("EmptyRemote.String() = %q, want %q", got, "empty remote")
	}
}
