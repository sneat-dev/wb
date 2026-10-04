package cmddashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"

	"github.com/sneat-dev/wb/internal/cockpit"
)

const hostedDashboardURL = "https://sneat.work/bench/dashboard/"

// dashboardLocalDeprecation is what `wb dashboard --local` says on stderr: the
// operations pages this flag once opened are retired, and Cockpit replaces
// them. The flag still starts or reuses the daemon and opens Cockpit, without
// the sign-in that `wb cockpit` performs.
const dashboardLocalDeprecation = "--local is deprecated: the local operations dashboard is retired and Cockpit replaces it; run `wb cockpit`, which also signs you in"

type dashboardOpenResult struct {
	URL    string `json:"url"`
	Scope  string `json:"scope"`
	Opened bool   `json:"opened"`
}

func localCockpitURL(base string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("daemon address %q: %w", base, err)
	}
	parsed.Path = cockpit.PagePrefix
	return parsed.String(), nil
}
func writeDashboardOpenResult(out io.Writer, format string, result dashboardOpenResult) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	action := "dashboard"
	if result.Opened {
		action = "opened dashboard"
	}
	_, err := fmt.Fprintf(out, "%s: %s\n", action, result.URL)
	return err
}
