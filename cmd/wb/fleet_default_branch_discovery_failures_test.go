package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

func TestDefaultBranchDiscoveryExactScopeRejectsMalformedAndDeduplicates(t *testing.T) {
	for _, invalid := range []string{"owner", "/repo", "owner/", "owner/repo/extra"} {
		t.Run(invalid, func(t *testing.T) {
			repos, failures, err := discoverDefaultBranchFleet("", nil, []string{invalid}, false, false)
			if err == nil || !strings.Contains(err.Error(), "invalid --repo") || repos != nil || failures != nil {
				t.Fatalf("invalid exact scope %q = repos=%v failures=%v err=%v", invalid, repos, failures, err)
			}
		})
	}
	repos, failures, err := discoverDefaultBranchFleet("acme", nil, []string{"acme/app", "ACME/APP", "other/tool", "acme/second"}, false, false)
	if err != nil || len(failures) != 0 || len(repos) != 2 || repos[0].Slug() != "acme/app" || repos[1].Slug() != "acme/second" || !repos[0].Remote || !repos[1].Remote {
		t.Fatalf("exact filtered scope = repos=%v failures=%v err=%v", repos, failures, err)
	}
}

func TestDefaultBranchDiscoveryBuildsOwnerScopeWithoutPartialResults(t *testing.T) {
	for _, test := range []struct {
		name                 string
		owners               []string
		includeUser, allOrgs bool
		authErr, orgsErr     error
		wantOwners           []string
		wantError            string
	}{
		{name: "account and memberships", wantOwners: []string{"account", "acme"}},
		{name: "account lookup refused", authErr: errors.New("auth unavailable"), wantError: "auth unavailable"},
		{name: "membership lookup refused", orgsErr: errors.New("orgs unavailable"), wantError: "orgs unavailable"},
		{name: "all organizations skips account", allOrgs: true, wantOwners: []string{"acme"}},
		{name: "explicit owner and account", owners: []string{" acme ", "", "acme"}, includeUser: true, wantOwners: []string{"account", "acme"}},
		{name: "explicit owner and account lookup refused", owners: []string{"acme"}, includeUser: true, authErr: errors.New("auth unavailable"), wantError: "auth unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldAuth, oldOrgs, oldList := defaultBranchAuthUser, defaultBranchMemberOrgs, defaultBranchListRemote
			t.Cleanup(func() {
				defaultBranchAuthUser, defaultBranchMemberOrgs, defaultBranchListRemote = oldAuth, oldOrgs, oldList
			})
			var owners []string
			defaultBranchAuthUser = func() (string, error) { return "account", test.authErr }
			defaultBranchMemberOrgs = func() ([]string, error) { return []string{"acme"}, test.orgsErr }
			defaultBranchListRemote = func(owner string) ([]discover.Repo, error) {
				owners = append(owners, owner)
				return []discover.Repo{{Org: owner, Name: "app"}}, nil
			}
			repos, failures, err := discoverDefaultBranchFleet("", test.owners, nil, test.includeUser, test.allOrgs)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) || len(owners) != 0 || len(repos) != 0 || len(failures) != 0 {
					t.Fatalf("scope error = repos=%v failures=%v listed=%v err=%v", repos, failures, owners, err)
				}
				return
			}
			if err != nil || len(failures) != 0 || !reflect.DeepEqual(owners, test.wantOwners) || len(repos) != len(test.wantOwners) {
				t.Fatalf("owner scope = repos=%v failures=%v listed=%v err=%v, want owners %v", repos, failures, owners, err, test.wantOwners)
			}
			for _, repo := range repos {
				if !repo.Remote {
					t.Fatalf("remote discovery returned a local repo: %+v", repo)
				}
			}
		})
	}
}
