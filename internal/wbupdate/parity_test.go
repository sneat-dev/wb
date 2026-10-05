package wbupdate

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/buildinfo" // install#req:upgrade-host-config-and-hook — newSelfUpdateCmd and
	// newUpgradeCmd both build their host Config from the IDENTICAL
	// Config(buildinfo.Version()) constructor (see each function's own source: not a
	// second hand-copied literal). This pins that the constructor is a pure,
	// deterministic function of the compiled catalog and the running version,
	// so two separate call sites can never quietly diverge onto different
	// values.
	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
)

func TestUpgradeCmd_HostConfigConstructorIsDeterministic(t *testing.T) {
	t.Parallel()
	first := Config(buildinfo.Version())
	second := Config(buildinfo.Version())
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Config(buildinfo.Version()) is not deterministic:\n%+v\n%+v", first, second)
	}
} // AC: install#req:upgrade-host-config-and-hook, cli-install#req:self-update-
// equals-upgrade-self — with the identical selfupdate.Config wb's own
// self-update command would build, `cliinstall.CheckUpgrades`'s host row
// (the library call `wb upgrade wb --check` makes) MUST report the exact
// same current/latest/verdict that `selfupdate.Config.Check` (the library
// call `wb self-update --check` makes) reports, over a hermetic fake
// GitHub releases endpoint — reusing the library's own exported Check/
// CheckUpgrades API as the test seam (no network, no real os.Executable
// dependency: CheckUpgrades' own Current/Latest/Verdict come from calling
// Config.Check regardless of how the host's own binary classifies).
func TestSelfUpdateAndUpgradeSelf_ReachSameCheckVerdict(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: releaseTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`[{"tag_name":"v9.9.9","prerelease":false,"draft":false}]`))}, nil
	})}

	cfg := Config(buildinfo.Version())
	cfg.CurrentVersion = "0.1.0"
	cfg.ReleasesAPIURL = "https://releases.invalid"
	cfg.HTTPClient = client

	ctx := context.Background()

	selfUpdateResult, err := cfg.Check(ctx)
	if err != nil {
		t.Fatalf("self-update side Config.Check: %v", err)
	}
	if selfUpdateResult.Verdict != selfupdate.UpdateAvailable {
		t.Fatalf("fixture sanity: self-update side verdict = %v, want UpdateAvailable", selfUpdateResult.Verdict)
	}

	plan, err := cliinstall.CheckUpgrades(ctx, []string{CatalogID}, cliinstall.UpgradeOptions{
		HostID:     CatalogID,
		HostConfig: cfg,
		Env:        cliinstall.DefaultInstallEnv(),
	})
	if err != nil {
		t.Fatalf("upgrade side CheckUpgrades: %v", err)
	}
	var hostRow *cliinstall.UpgradeResult
	for i := range plan.Results {
		if plan.Results[i].Host {
			hostRow = &plan.Results[i]
			break
		}
	}
	if hostRow == nil {
		t.Fatalf("CheckUpgrades produced no host row for %q: %+v", CatalogID, plan.Results)
	}
	if hostRow.Current != selfUpdateResult.Current || hostRow.Latest != selfUpdateResult.Latest || hostRow.Verdict != selfUpdateResult.Verdict {
		t.Errorf("upgrade wb --check = {current:%s latest:%s verdict:%s}, self-update --check = {current:%s latest:%s verdict:%s}; want identical (cli-install#req:self-update-equals-upgrade-self)",
			hostRow.Current, hostRow.Latest, hostRow.Verdict,
			selfUpdateResult.Current, selfUpdateResult.Latest, selfUpdateResult.Verdict)
	}
}

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
