package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/selfupdate"
)

type selfUpdateReleaseTransport func(*http.Request) (*http.Response, error)

func (f selfUpdateReleaseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// Exercise WB's actual command binding without a real Homebrew process or
// network. Only the test executable's install classification is configured.
func TestSelfUpdateHomebrewDryRunReportsVersions(t *testing.T) {
	oldDetect := selfUpdateDetect
	t.Cleanup(func() { selfUpdateDetect = oldDetect })
	selfUpdateDetect = func(selfupdate.Config) (selfupdate.Detection, error) {
		return selfupdate.Detection{}, errors.New("no post-update work during this dry run")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		current     string
		unavailable bool
	}{
		{name: "newer", current: "0.81.1"},
		{name: "equal", current: "0.92.3"},
		{name: "unknown", current: "(devel)"},
		{name: "unavailable", current: "0.81.1", unavailable: true},
	} {
		for _, format := range []string{"text", "json"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				cfg := newSelfUpdateConfig()
				cfg.CurrentVersion = tc.current
				cfg.Managers[0].PathMarkers = []string{executable}
				requests := 0
				cfg.HTTPClient = &http.Client{Transport: selfUpdateReleaseTransport(func(req *http.Request) (*http.Response, error) {
					requests++
					if req.URL.Path != "/repos/sneat-dev/wb/releases" {
						t.Errorf("unexpected release/asset request: %s", req.URL)
					}
					status := http.StatusOK
					if tc.unavailable {
						status = http.StatusServiceUnavailable
					}
					return &http.Response{
						StatusCode: status,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(`[{"tag_name":"v0.92.3","draft":false,"prerelease":false}]`)),
					}, nil
				})}
				cmd := newSelfUpdateCmdWithConfig(cfg)
				var out, stderr bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&stderr)
				cmd.SetArgs([]string{"--dry-run", "--format", format})
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
				if requests != 1 {
					t.Errorf("release requests = %d, want 1", requests)
				}
				if strings.Contains(out.String(), "\x1b") {
					t.Errorf("redirected output contains ANSI escapes: %q", out.String())
				}
				if format == "json" {
					var result map[string]any
					if err := json.Unmarshal(out.Bytes(), &result); err != nil {
						t.Fatalf("stdout is not a single JSON document: %v\n%s", err, out.String())
					}
					for key, want := range map[string]string{
						"current": tc.current, "action": "planned",
						"manager": "Homebrew", "command": selfUpdateHomebrewUpgradeCommand,
					} {
						if result[key] != want {
							t.Errorf("%s = %v, want %s", key, result[key], want)
						}
					}
					if tc.unavailable {
						if result["latest"] != nil || result["release_check_warning"] == nil {
							t.Errorf("unavailable release must have a warning and no latest version: %v", result)
						}
					} else if result["latest"] != "0.92.3" {
						t.Errorf("latest = %v, want 0.92.3", result["latest"])
					}
				} else {
					latest := "0.92.3"
					if tc.unavailable {
						latest = "unavailable"
					}
					for _, want := range []string{"Current", "Latest", tc.current, latest, "Homebrew", selfUpdateHomebrewUpgradeCommand} {
						if !strings.Contains(out.String(), want) {
							t.Errorf("missing %q in output:\n%s", want, out.String())
						}
					}
				}
				if tc.unavailable && !strings.Contains(stderr.String(), "unavailable") {
					t.Errorf("release lookup warning missing from stderr: %q", stderr.String())
				}
			})
		}
	}
}
