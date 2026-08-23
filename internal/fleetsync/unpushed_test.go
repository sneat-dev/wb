package fleetsync

import (
	"os/exec"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

// aheadOnlyClone is the shape that hid work in plain sight: a clone holding a
// commit origin has never seen, with nothing to pull. `git pull` succeeds
// there ("Already up to date"), so this used to report as an ordinary Pulled.
func aheadOnlyClone(t *testing.T) discover.Repo {
	t.Helper()
	origin := t.TempDir()
	git(t, origin, "init", "-q", "--bare", "-b", "main")

	local := t.TempDir()
	cloneInto(t, origin, local)
	git(t, local, "commit", "-q", "--allow-empty", "-m", "seed")
	git(t, local, "push", "-q", "origin", "main")
	git(t, local, "commit", "-q", "--allow-empty", "-m", "never pushed")

	return discover.Repo{Org: "acme", Name: "widgets", Path: local, Remote: true}
}

func cloneInto(t *testing.T, origin, dest string) {
	t.Helper()
	cmd := exec.Command("git", "clone", "-q", origin, dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
}

func TestSyncReportsAheadOnlyCloneAsUnpushed(t *testing.T) {
	repo := aheadOnlyClone(t)

	res := Sync(repo, "", false)

	if res.Status != Unpushed {
		t.Fatalf("Status = %v (err=%v), want Unpushed; a clean pull must not hide unlanded work",
			res.Status, res.Err)
	}
	if res.Err != nil {
		t.Fatalf("Err = %v, want nil; holding unpushed work is not a sync fault", res.Err)
	}
	if len(res.Detail.Unpushed) != 1 {
		t.Fatalf("Detail.Unpushed = %v, want exactly the one unpushed commit", res.Detail.Unpushed)
	}
}

// Work abandoned on a side branch is still work that exists nowhere else.
func TestSyncReportsUnpushedOnANonCheckedOutBranch(t *testing.T) {
	origin := t.TempDir()
	git(t, origin, "init", "-q", "--bare", "-b", "main")
	local := t.TempDir()
	cloneInto(t, origin, local)
	git(t, local, "commit", "-q", "--allow-empty", "-m", "seed")
	git(t, local, "push", "-q", "origin", "main")

	git(t, local, "switch", "-q", "-c", "abandoned")
	git(t, local, "commit", "-q", "--allow-empty", "-m", "forgotten work")
	git(t, local, "switch", "-q", "main")

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: local, Remote: true}
	res := Sync(repo, "", false)

	if res.Status != Unpushed {
		t.Fatalf("Status = %v (err=%v), want Unpushed for a side branch nobody pushed",
			res.Status, res.Err)
	}
}

// The ordinary case must stay ordinary: a clone with nothing of its own is
// still just Pulled.
func TestSyncStillReportsPulledWhenNothingIsOwed(t *testing.T) {
	origin := t.TempDir()
	git(t, origin, "init", "-q", "--bare", "-b", "main")
	local := t.TempDir()
	cloneInto(t, origin, local)
	git(t, local, "commit", "-q", "--allow-empty", "-m", "seed")
	git(t, local, "push", "-q", "origin", "main")

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: local, Remote: true}
	if res := Sync(repo, "", false); res.Status != Pulled {
		t.Fatalf("Status = %v (err=%v), want Pulled", res.Status, res.Err)
	}
}

// An archived remote is read-only, so unpushed commits in its clone can never
// be pushed. That is a decision someone has to make, not a keep-and-forget.
func TestSyncReportsArchivedCloneHoldingUnpushedWorkAsUnlandable(t *testing.T) {
	repo := aheadOnlyClone(t)
	repo.Archived = true

	res := Sync(repo, "", false)

	if res.Status != ArchivedUnlandable {
		t.Fatalf("Status = %v, want ArchivedUnlandable", res.Status)
	}
	if len(res.Detail.Unpushed) != 1 {
		t.Fatalf("Detail.Unpushed = %v, want the unpushed commit reported", res.Detail.Unpushed)
	}
}

// An archived clone dirty for any other reason is an ordinary keep: a stash or
// an edit can still be resolved locally, so it does not need a decision.
func TestSyncStillKeepsArchivedCloneDirtyForOtherReasons(t *testing.T) {
	origin := t.TempDir()
	git(t, origin, "init", "-q", "--bare", "-b", "main")
	local := t.TempDir()
	cloneInto(t, origin, local)
	git(t, local, "commit", "-q", "--allow-empty", "-m", "seed")
	git(t, local, "push", "-q", "origin", "main")
	write(t, local, "scratch.txt", "untracked\n")

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: local, Remote: true, Archived: true}
	if res := Sync(repo, "", false); res.Status != KeptArchived {
		t.Fatalf("Status = %v, want KeptArchived", res.Status)
	}
}

func TestUnpushedAndArchivedUnlandableStatusStrings(t *testing.T) {
	if got := Unpushed.String(); got != "unpushed commits" {
		t.Fatalf("Unpushed.String() = %q", got)
	}
	if got := ArchivedUnlandable.String(); got != "archived, holds unpushed commits" {
		t.Fatalf("ArchivedUnlandable.String() = %q", got)
	}
}
