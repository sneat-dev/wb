package worktrees

import (
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/repopath"
)

func TestLayoutFacadePreservesPlacementAndRepositoryGrammar(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if !validSafeSegment("task") || validSafeSegment(".task") ||
		!validRepositorySegment(".github") || validRepositorySegment("..") {
		t.Fatal("segment facade changed grammar")
	}
	if !validCloneRelative("github.com/acme/.github") || validCloneRelative("forge/acme/app") {
		t.Fatal("clone relative facade changed grammar")
	}
	parent, repo := splitCloneRelative(" /github.com/acme/app/ ")
	if parent != "github.com/acme" || repo != "app" {
		t.Fatalf("split clone facade = %q, %q", parent, repo)
	}
	address, err := splitRepositoryAddress("github.com/acme/.github")
	if err != nil || address != (repopath.Address{Host: "github.com", Org: "acme", Repo: ".github"}) {
		t.Fatalf("repository address facade = %+v, %v", address, err)
	}
	owner, name, err := splitRepository("github.com/acme/.github")
	if err != nil || owner != "acme" || name != ".github" {
		t.Fatalf("repository facade = %q, %q, %v", owner, name, err)
	}
	placement := WorktreePlacement{Root: root, relative: "github.com/acme/.github"}
	path, err := placement.Path("task", "acme/.github")
	want := filepath.Join(root, "task", "github.com", "acme", ".github")
	if err != nil || path != want {
		t.Fatalf("placement facade = %q, %v, want %q", path, err, want)
	}
	owner, name, path, err = canonicalRepositoryPath(root, "github.com/acme/.github")
	if err != nil || owner != "acme" || name != ".github" || path != address.Path(root) {
		t.Fatalf("canonical path facade = %q, %q, %q, %v", owner, name, path, err)
	}
	resolved, err := resolveCanonicalClone(root, address)
	if err != nil || resolved != address {
		t.Fatalf("canonical resolution facade = %+v, %v", resolved, err)
	}
	host, owner, name, err := canonicalCoordinates(root, address.Path(root))
	if err != nil || host != "github.com" || owner != "acme" || name != ".github" {
		t.Fatalf("coordinates facade = %q, %q, %q, %v", host, owner, name, err)
	}
}
