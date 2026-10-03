package defaultbranch

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

func TestDefaultBranchDiscoveryExactScopeRejectsMalformedAndDeduplicates(t *testing.T) {
	service := New()

	for _, invalid := range []string{"owner", "/repo", "owner/", "owner/repo/extra"} {
		t.Run(invalid, func(t *testing.T) {
			repos, failures, err := service.discoverDefaultBranchFleet("", nil, []string{invalid}, false, false)
			if err == nil || !strings.Contains(err.Error(), "invalid --repo") || repos != nil || failures != nil {
				t.Fatalf("invalid exact scope %q = repos=%v failures=%v err=%v", invalid, repos, failures, err)
			}
		})
	}
	repos, failures, err := service.discoverDefaultBranchFleet("acme", nil, []string{"acme/app", "ACME/APP", "other/tool", "acme/second"}, false, false)
	if err != nil || len(failures) != 0 || len(repos) != 2 || repos[0].Slug() != "acme/app" || repos[1].Slug() != "acme/second" || !repos[0].Remote || !repos[1].Remote {
		t.Fatalf("exact filtered scope = repos=%v failures=%v err=%v", repos, failures, err)
	}
}

func TestDefaultBranchDiscoveryBuildsOwnerScopeWithoutPartialResults(t *testing.T) {
	service := New()

	for _, test := range []struct {
		name                 string
		Owners               []string
		IncludeUser, AllOrgs bool
		authErr, orgsErr     error
		wantOwners           []string
		wantError            string
	}{
		{name: "account and memberships", wantOwners: []string{"account", "acme"}},
		{name: "account lookup refused", authErr: errors.New("auth unavailable"), wantError: "auth unavailable"},
		{name: "membership lookup refused", orgsErr: errors.New("orgs unavailable"), wantError: "orgs unavailable"},
		{name: "all organizations skips account", AllOrgs: true, wantOwners: []string{"acme"}},
		{name: "explicit owner and account", Owners: []string{" acme ", "", "acme"}, IncludeUser: true, wantOwners: []string{"account", "acme"}},
		{name: "explicit owner and account lookup refused", Owners: []string{"acme"}, IncludeUser: true, authErr: errors.New("auth unavailable"), wantError: "auth unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldAuth, oldOrgs, oldList := service.deps.AuthUser, service.deps.MemberOrgs, service.deps.ListRemote
			t.Cleanup(func() {
				service.deps.AuthUser, service.deps.MemberOrgs, service.deps.ListRemote = oldAuth, oldOrgs, oldList
			})
			var Owners []string
			service.deps.AuthUser = func() (string, error) { return "account", test.authErr }
			service.deps.MemberOrgs = func() ([]string, error) { return []string{"acme"}, test.orgsErr }
			service.deps.ListRemote = func(owner string) ([]discover.Repo, error) {
				Owners = append(Owners, owner)
				return []discover.Repo{{Org: owner, Name: "app"}}, nil
			}
			repos, failures, err := service.discoverDefaultBranchFleet("", test.Owners, nil, test.IncludeUser, test.AllOrgs)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) || len(Owners) != 0 || len(repos) != 0 || len(failures) != 0 {
					t.Fatalf("scope error = repos=%v failures=%v listed=%v err=%v", repos, failures, Owners, err)
				}
				return
			}
			if err != nil || len(failures) != 0 || !reflect.DeepEqual(Owners, test.wantOwners) || len(repos) != len(test.wantOwners) {
				t.Fatalf("owner scope = repos=%v failures=%v listed=%v err=%v, want owners %v", repos, failures, Owners, err, test.wantOwners)
			}
			for _, repo := range repos {
				if !repo.Remote {
					t.Fatalf("remote discovery returned a local repo: %+v", repo)
				}
			}
		})
	}
}
