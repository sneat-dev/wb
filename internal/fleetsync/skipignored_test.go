package fleetsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
)

// markedRepo returns a repo whose clone carries the skip marker and has no
// origin configured. The missing origin is the assertion mechanism: there is
// no seam over gitops to observe calls, so a pull attempt would surface as a
// Failed status rather than passing silently.
func markedRepo(t *testing.T) discover.Repo {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	if err := gitops.SetSkipSync(dir); err != nil {
		t.Fatalf("SetSkipSync: %v", err)
	}
	return discover.Repo{Org: "acme", Name: "widgets", Path: dir, Remote: true}
}

func TestSyncSkipsMarkedRepo(t *testing.T) {
	repo := markedRepo(t)

	res := Sync(context.Background(), repo, "", false, false)

	if res.Status != SkippedIgnored {
		t.Fatalf("Status = %v (err=%v), want SkippedIgnored", res.Status, res.Err)
	}
}

// The marker also protects the clone from archived-repo cleanup: wb must not
// delete a checkout the user told it to leave alone.
func TestSyncKeepsMarkedArchivedClone(t *testing.T) {
	repo := markedRepo(t)
	repo.Archived = true

	res := Sync(context.Background(), repo, "", false, false)

	if res.Status != SkippedIgnored {
		t.Fatalf("Status = %v (err=%v), want SkippedIgnored", res.Status, res.Err)
	}
	if _, err := os.Stat(filepath.Join(repo.Path, ".git")); err != nil {
		t.Fatalf("clone of a marked archived repo was removed: %v", err)
	}
}

func TestSyncMarkedRepoStatusString(t *testing.T) {
	if got := SkippedIgnored.String(); got != "skipped (ignored)" {
		t.Fatalf("SkippedIgnored.String() = %q, want %q", got, "skipped (ignored)")
	}
}

// A malformed marker must fail loudly rather than being read as "not marked".
func TestSyncFailsOnMalformedMarker(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "--local", gitops.SkipSyncKey, "garbage")
	repo := discover.Repo{Org: "acme", Name: "widgets", Path: dir, Remote: true}

	res := Sync(context.Background(), repo, "", false, false)

	if res.Status != Failed {
		t.Fatalf("Status = %v, want Failed for a malformed marker", res.Status)
	}
}
