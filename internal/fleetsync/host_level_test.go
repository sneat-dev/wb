package fleetsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

// TestSyncDoesNotRecloneAnExistingHostLevelClone is the BLOCKER's end-to-end
// predicate: a repository that already sits at <root>/{host}/{org}/{repo} must
// be found where it is and pulled — never read as uncloned and cloned again as
// a flat duplicate beside it.
func TestSyncDoesNotRecloneAnExistingHostLevelClone(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	root := t.TempDir()
	hosted := filepath.Join(root, "github.com", "acme", "widgets")
	if err := os.MkdirAll(filepath.Dir(hosted), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, root, "clone", "-q", remote, hosted)

	repositories, err := discover.ScanLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0].Path != hosted {
		t.Fatalf("ScanLocal() = %#v, want the host-level clone at %s", repositories, hosted)
	}

	// Reconcile marks a repository GitHub still lists as remote; that is the
	// state `wb sync` actually hands to Sync.
	repository := repositories[0]
	repository.Remote = true
	result := Sync(context.Background(), repository, root, false, false)
	if result.Status == Cloned {
		t.Fatalf("an existing host-level clone was cloned again: %+v", result)
	}
	if result.Status != Pulled {
		t.Fatalf("status = %s (%v), want pulled", result.Status, result.Err)
	}
	if _, err := os.Stat(filepath.Join(root, "acme", "widgets")); !os.IsNotExist(err) {
		t.Fatalf("a flat duplicate appeared at %s", filepath.Join(root, "acme", "widgets"))
	}
}

// TestSyncClonesIntoTheHostLevelNamedByTheCloneURL proves a genuinely missing
// repository is created where its own URL says it belongs, so a migrated root
// stays in the shape the layout rules require. A git url.insteadOf rewrite
// stands in for the network.
func TestSyncClonesIntoTheHostLevelNamedByTheCloneURL(t *testing.T) {
	remote := newRemote(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[url \""+remote+"\"]\n\tinsteadOf = git@github.com:acme/widgets.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repo := discover.Repo{Org: "acme", Name: "widgets", CloneURL: "git@github.com:acme/widgets.git", Remote: true}

	result := Sync(context.Background(), repo, root, false, false)
	if result.Status != Cloned {
		t.Fatalf("status = %s (%v), want cloned", result.Status, result.Err)
	}
	hosted := filepath.Join(root, "github.com", "acme", "widgets")
	if result.Repo.Path != hosted {
		t.Fatalf("clone landed at %q, want %q", result.Repo.Path, hosted)
	}
	if _, err := os.Stat(filepath.Join(hosted, "f.txt")); err != nil {
		t.Fatalf("the clone is not usable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "acme", "widgets")); !os.IsNotExist(err) {
		t.Fatalf("a flat duplicate appeared at %s", filepath.Join(root, "acme", "widgets"))
	}
}

// TestSyncKeepsALocalRemoteCloneAtTheLegacyPlacement guards the other side of
// the rule: a clone URL naming no literal forge must not invent a host level.
func TestSyncKeepsALocalRemoteCloneAtTheLegacyPlacement(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	root := t.TempDir()
	repo := discover.Repo{Org: "acme", Name: "widgets", CloneURL: remote, Remote: true}

	result := Sync(context.Background(), repo, root, false, false)
	if result.Status != Cloned {
		t.Fatalf("status = %s (%v), want cloned", result.Status, result.Err)
	}
	legacy := filepath.Join(root, "acme", "widgets")
	if result.Repo.Path != legacy {
		t.Fatalf("clone landed at %q, want the legacy placement %q", result.Repo.Path, legacy)
	}
}
