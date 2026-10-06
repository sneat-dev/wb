package fleetinspect

import (
	"testing"

	"github.com/sneat-dev/wb/internal/discover" // fleetRemoteRollup is also drivable directly.
)

func TestRemoteRollupDirectRetainsLocalOnlyAndRegexRefusal(t *testing.T) {
	t.Parallel()
	root := projectsFixture(t, "acme/app")
	service := testService()
	service.deps.Owners = func([]string) []string { return []string{"acme"} }
	service.deps.Remote = func(string, string, func() []string) ([]discover.Repo, error) {
		repos, err := discover.ScanLocal(root)
		for i := range repos {
			repos[i].Local = true
		}
		return repos, err
	}
	remote, err := service.fleetRemoteRollup(root, "", Options{Parallel: 1})
	if err != nil {
		t.Fatal(err)
	}
	if remote.LocalOnly != 1 {
		t.Fatalf("remote rollup = %+v, want one local-only repository", remote)
	}
	if _, err := service.fleetRemoteRollup(root, "", Options{Parallel: 1, Regex: `(`}); err == nil {
		t.Fatal("an invalid --regex must be refused by fleetRemoteRollup")
	}
}
