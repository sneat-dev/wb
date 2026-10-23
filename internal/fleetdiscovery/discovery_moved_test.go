package fleetdiscovery

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

func TestCwCovFleetOwnersAndFleetDiscovery(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "acme", "app", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolver := New(io.Discard)
	resolver.AuthUser = func() (string, error) { return "cwcov-user", nil }
	resolver.MemberOrgs = func() ([]string, error) { return []string{"cwcov-org", "other-org"}, nil }
	resolver.ListRemote = func(owner string) ([]discover.Repo, error) {
		return []discover.Repo{{Org: owner, Name: "remote-only", Remote: true}}, nil
	}

	owners := resolver.Owners([]string{"cwcov-extra"})
	assertContains := func(list []string, want string) {
		t.Helper()
		for _, item := range list {
			if item == want {
				return
			}
		}
		t.Errorf("owners %v missing %q", list, want)
	}
	assertContains(owners, "cwcov-user")
	assertContains(owners, "cwcov-org")
	assertContains(owners, "other-org")
	assertContains(owners, "cwcov-extra")

	repos, err := resolver.Discover(root, "", func() []string { return []string{"acme"} })
	if err != nil {
		t.Fatal(err)
	}
	slugs := map[string]bool{}
	for _, repo := range repos {
		slugs[repo.Slug()] = true
	}
	if !slugs["acme/app"] {
		t.Errorf("local repo missing from fleet result: %v", slugs)
	}
	if !slugs["acme/remote-only"] {
		t.Errorf("remote repo missing from fleet result: %v", slugs)
	}

	filtered, err := resolver.Discover(root, "app", func() []string { return []string{"acme"} })
	if err != nil {
		t.Fatal(err)
	}
	for _, repo := range filtered {
		if !strings.Contains(repo.Slug(), "app") {
			t.Errorf("filter leaked %q", repo.Slug())
		}
	}
}
func TestCwCovFleetOwnersFallsBackToExtraOrgsOnGHFailure(t *testing.T) {
	t.Parallel()
	resolver := New(io.Discard)
	resolver.AuthUser = func() (string, error) { return "", errors.New("unavailable") }
	resolver.MemberOrgs = func() ([]string, error) { return nil, errors.New("unavailable") }
	owners := resolver.Owners([]string{"only-extra"})
	if len(owners) != 1 || owners[0] != "only-extra" {
		t.Fatalf("owners = %v, want just the explicit extra org when discovery fails", owners)
	}
}
