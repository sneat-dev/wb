package discover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hostLevelClone creates one canonical clone at <root>/<host>/<org>/<repo>.
func hostLevelClone(t *testing.T, root, host, organization, name string) string {
	t.Helper()
	repository := filepath.Join(root, host, organization, name)
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repository
}

// TestScanLocalReadsBothCanonicalPlacements is the BLOCKER's core predicate: a
// root holding host-level clones and legacy two-level clones must report every
// one of them, and a host-level clone must never be read as no repository at
// all — that is what made `wb sync` on a migrated root see nothing and write a
// second, flat duplicate.
func TestScanLocalReadsBothCanonicalPlacements(t *testing.T) {
	root := t.TempDir()
	hosted := hostLevelClone(t, root, "github.com", "acme", "app")
	otherForge := hostLevelClone(t, root, "git.example.test", "acme", "other")
	legacy := mustIndexedRepository(t, root, "acme", "legacy")
	// A .git file is a linked worktree, not a fleet member, under either shape.
	linked := filepath.Join(root, "github.com", "acme", "linked")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: ../../../acme/app/.git/worktrees/linked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repositories, err := ScanLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, repository := range repositories {
		got[repository.Slug()] = repository.Path
	}
	if len(got) != 3 {
		t.Fatalf("ScanLocal() = %#v, want one clone per canonical placement", repositories)
	}
	if got["acme/app"] != hosted {
		t.Fatalf("acme/app path = %q, want %q", got["acme/app"], hosted)
	}
	if got["acme/other"] != otherForge {
		t.Fatalf("acme/other path = %q, want %q", got["acme/other"], otherForge)
	}
	if got["acme/legacy"] != legacy {
		t.Fatalf("acme/legacy path = %q, want %q", got["acme/legacy"], legacy)
	}
}

// TestScanLocalIndexedFindsHostLevelClones covers the persisted path every
// fleet command actually uses: the index must report host-level clones, cache
// them, and keep the cache valid on re-read.
func TestScanLocalIndexedFindsHostLevelClones(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hosted := hostLevelClone(t, root, "github.com", "acme", "app")
	legacy := mustIndexedRepository(t, root, "acme", "legacy")
	cachePath := filepath.Join(t.TempDir(), "fleet-inventory.json")

	first, err := ScanLocalIndexed(root, LocalIndexOptions{CachePath: cachePath, MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheHit {
		t.Fatal("the first scan must not be a cache hit")
	}
	if len(first.Repositories) != 2 {
		t.Fatalf("indexed repositories = %#v, want the host-level and legacy clones", first.Repositories)
	}

	second, err := ScanLocalIndexed(root, LocalIndexOptions{CachePath: cachePath, MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !second.CacheHit {
		t.Fatalf("the cached host-level index was rejected: %#v", second.Diagnostics)
	}
	paths := map[string]string{}
	for _, repository := range second.Repositories {
		paths[repository.Slug()] = repository.Path
	}
	if paths["acme/app"] != hosted || paths["acme/legacy"] != legacy {
		t.Fatalf("cached paths = %#v, want app=%q legacy=%q", paths, hosted, legacy)
	}
}

// TestSnapshotInvalidatesOnANewOrganizationBelowAForgeHost proves the cache
// fingerprint still notices a repository appearing under a host level; a
// fingerprint that only covered the first level would serve stale inventory.
func TestSnapshotInvalidatesOnANewOrganizationBelowAForgeHost(t *testing.T) {
	root := t.TempDir()
	hostLevelClone(t, root, "github.com", "acme", "app")

	first, err := snapshotLocalSource(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.owners) != 1 || first.owners[0].Host != "github.com" || first.owners[0].Name != "acme" {
		t.Fatalf("snapshot owners = %#v, want github.com/acme", first.owners)
	}

	hostLevelClone(t, root, "github.com", "beta", "app")
	second, err := snapshotLocalSource(root)
	if err != nil {
		t.Fatal(err)
	}
	if second.fingerprint == first.fingerprint {
		t.Fatalf("a new organization below a forge host did not change the fingerprint: %s", second.fingerprint)
	}
	if len(second.owners) != 2 {
		t.Fatalf("snapshot owners = %#v, want acme and beta", second.owners)
	}

	// A whole new forge level must also invalidate it.
	hostLevelClone(t, root, "git.example.test", "acme", "app")
	third, err := snapshotLocalSource(root)
	if err != nil {
		t.Fatal(err)
	}
	if third.fingerprint == second.fingerprint {
		t.Fatalf("a new forge host did not change the fingerprint: %s", third.fingerprint)
	}
}

// TestValidCachedReposAcceptsBothPlacements pins the index validator: both
// shapes are valid for their own coordinate, and neither may masquerade as the
// other or escape the root.
func TestValidCachedReposAcceptsBothPlacements(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name       string
		repository Repo
		ok         bool
	}{
		{name: "host level", repository: Repo{Org: "acme", Name: "app", Host: "github.com", Path: filepath.Join(root, "github.com", "acme", "app")}, ok: true},
		{name: "legacy", repository: Repo{Org: "acme", Name: "app", Path: filepath.Join(root, "acme", "app")}, ok: true},
		{name: "wrong owner at host level", repository: Repo{Org: "acme", Name: "app", Host: "github.com", Path: filepath.Join(root, "github.com", "other", "app")}, ok: false},
		{name: "wrong host label", repository: Repo{Org: "acme", Name: "app", Host: "github.com", Path: filepath.Join(root, "not-a-host", "acme", "app")}, ok: false},
		{name: "outside the root", repository: Repo{Org: "acme", Name: "app", Host: "github.com", Path: filepath.Join(filepath.Dir(root), "github.com", "acme", "app")}, ok: false},
		{name: "too deep", repository: Repo{Org: "acme", Name: "app", Host: "github.com", Path: filepath.Join(root, "github.com", "acme", "app", "extra")}, ok: false},
		{name: "port hosted forge", repository: Repo{Org: "acme", Name: "app", Host: "github.com:8443", Path: filepath.Join(root, "github.com:8443", "acme", "app")}, ok: true},
		{name: "cached host missing", repository: Repo{Org: "acme", Name: "app", Path: filepath.Join(root, "github.com", "acme", "app")}, ok: false},
		{name: "cached host wrong", repository: Repo{Org: "acme", Name: "app", Host: "gitlab.example.test", Path: filepath.Join(root, "github.com", "acme", "app")}, ok: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validCachedRepos(root, []Repo{test.repository}); got != test.ok {
				t.Fatalf("validCachedRepos(%+v) = %t, want %t", test.repository, got, test.ok)
			}
		})
	}
}

// TestScanLocalSeesAPortHostedForgeLevel pins the predicate consistency: a
// clone deliberately created under a forge with an explicit port must be
// visible to discovery, exactly as the placement rules allow it to be created.
func TestScanLocalSeesAPortHostedForgeLevel(t *testing.T) {
	root := t.TempDir()
	hosted := hostLevelClone(t, root, "github.com:8443", "acme", "app")

	repositories, err := ScanLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0].Path != hosted {
		t.Fatalf("ScanLocal() = %#v, want the port-hosted clone %q", repositories, hosted)
	}
	if !strings.Contains(repositories[0].Path, "github.com:8443") {
		t.Fatalf("path = %q", repositories[0].Path)
	}
}

// TestReconcileKeepsTwoForgesOfOneRepository is the multi-forge case the host
// level exists to express: the same owner/repository cloned from two forges is
// two repositories, and merging them by bare slug used to silently drop one.
func TestReconcileKeepsTwoForgesOfOneRepository(t *testing.T) {
	root := t.TempDir()
	githubClone := hostLevelClone(t, root, "github.com", "acme", "app")
	gitlabClone := hostLevelClone(t, root, "gitlab.example.test", "acme", "app")

	local, err := ScanLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 2 {
		t.Fatalf("ScanLocal() = %#v, want both forges", local)
	}
	reconciled := Reconcile(local, nil)
	if len(reconciled) != 2 {
		t.Fatalf("Reconcile() = %#v, want both forges kept", reconciled)
	}
	byIdentity := map[string]string{}
	for _, repository := range reconciled {
		byIdentity[repository.Identity()] = repository.Path
	}
	if byIdentity["github.com/acme/app"] != githubClone {
		t.Fatalf("github clone = %q, want %q", byIdentity["github.com/acme/app"], githubClone)
	}
	if byIdentity["gitlab.example.test/acme/app"] != gitlabClone {
		t.Fatalf("gitlab clone = %q, want %q", byIdentity["gitlab.example.test/acme/app"], gitlabClone)
	}
}

// TestReconcileMatchesARemoteListingToTheRightLocalClone proves the other side
// of the identity rule: a GitHub listing still folds into the GitHub clone, and
// a legacy flat clone is still recognized as that GitHub clone, so `wb sync`
// updates it in place instead of cloning a duplicate.
func TestReconcileMatchesARemoteListingToTheRightLocalClone(t *testing.T) {
	root := t.TempDir()
	githubClone := hostLevelClone(t, root, "github.com", "acme", "app")
	gitlabClone := hostLevelClone(t, root, "gitlab.example.test", "acme", "app")
	local, err := ScanLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	remote := []Repo{{Org: "acme", Name: "app", Host: "github.com", CloneURL: "git@github.com:acme/app.git"}}
	reconciled := Reconcile(local, remote)
	if len(reconciled) != 2 {
		t.Fatalf("Reconcile() = %#v, want two entries", reconciled)
	}
	for _, repository := range reconciled {
		switch repository.Path {
		case githubClone:
			if !repository.Remote {
				t.Fatalf("the GitHub clone was not matched to the GitHub listing: %+v", repository)
			}
		case gitlabClone:
			if repository.Remote {
				t.Fatalf("the GitLab clone was matched to a GitHub listing: %+v", repository)
			}
		}
	}

	// A legacy flat clone carries no host of its own, but a flat clone of a
	// GitHub repository is the GitHub clone on this fleet's layout.
	legacyRoot := t.TempDir()
	legacy := mustIndexedRepository(t, legacyRoot, "acme", "app")
	legacyLocal, err := ScanLocal(legacyRoot)
	if err != nil {
		t.Fatal(err)
	}
	merged := Reconcile(legacyLocal, remote)
	if len(merged) != 1 || merged[0].Path != legacy || !merged[0].Remote {
		t.Fatalf("a legacy clone was not matched to its GitHub listing: %#v", merged)
	}
}

// TestReconcileNeverDropsADuplicateClone proves two clones of the same
// identity are both kept: a duplicate on disk is a finding for the operator,
// not data the inventory may discard.
func TestReconcileNeverDropsADuplicateClone(t *testing.T) {
	reconciled := Reconcile([]Repo{
		{Org: "acme", Name: "app", Host: "github.com", Path: "/projects/github.com/acme/app"},
		{Org: "acme", Name: "app", Host: "github.com", Path: "/other/github.com/acme/app"},
	}, nil)
	if len(reconciled) != 2 {
		t.Fatalf("Reconcile() = %#v, want both clones kept", reconciled)
	}
}

// TestReconcileMatchesALegacyCloneToItsOwnForgeListing proves the legacy flat
// placement keeps working under the identity rule: a flat clone cannot express
// a forge in its path, so a remote listing of its slug still describes it, even
// when that listing names a host other than github.com.
func TestReconcileMatchesALegacyCloneToItsOwnForgeListing(t *testing.T) {
	root := t.TempDir()
	legacy := mustIndexedRepository(t, root, "acme", "app")
	local, err := ScanLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 1 || local[0].Host != "" {
		t.Fatalf("ScanLocal() = %#v, want one host-less legacy clone", local)
	}
	remote := []Repo{{Org: "acme", Name: "app", Host: "example.test", CloneURL: "git@example.test:acme/app.git"}}
	merged := Reconcile(local, remote)
	if len(merged) != 1 || merged[0].Path != legacy || !merged[0].Remote || merged[0].CloneURL == "" {
		t.Fatalf("a legacy clone was not matched to its own forge's listing: %#v", merged)
	}

	// A listing naming no forge at all still describes the GitHub clone, which
	// is the only addressable candidate for it.
	hostedRoot := t.TempDir()
	hosted := hostLevelClone(t, hostedRoot, "github.com", "acme", "app")
	hostedLocal, err := ScanLocal(hostedRoot)
	if err != nil {
		t.Fatal(err)
	}
	hostless := Reconcile(hostedLocal, []Repo{{Org: "acme", Name: "app"}})
	if len(hostless) != 1 || hostless[0].Path != hosted || !hostless[0].Remote {
		t.Fatalf("a forge-less listing was not matched to the GitHub clone: %#v", hostless)
	}
}
