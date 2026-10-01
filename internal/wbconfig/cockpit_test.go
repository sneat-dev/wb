package wbconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCockpitDefaultsApplyWhenNothingIsConfigured(t *testing.T) {
	t.Parallel()
	want := CockpitConfig{HostedURL: "https://sneat.dev/wb/cockpit/", CodeBrowserURL: "https://codegrapher.dev/", AnonymousMetadata: true}
	for name, raw := range map[string]string{"empty": "", "other sections only": "hub:\n  engine: x\n", "empty section": "cockpit: {}\n", "null section": "cockpit:\n"} {
		got, err := parseCockpit([]byte(raw))
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v; want %+v", name, got, err, want)
		}
	}
}

func TestCockpitSectionOverridesEachKey(t *testing.T) {
	t.Parallel()
	got, err := parseCockpit([]byte("cockpit:\n  hosted_url: https://hosted.example.test/c/\n  code_browser_url: https://code.example.test/\n  anonymous_metadata: false\n  refresh_interval: 45s\n"))
	want := CockpitConfig{HostedURL: "https://hosted.example.test/c/", CodeBrowserURL: "https://code.example.test/", RefreshInterval: 45 * time.Second}
	if err != nil || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
}

func TestCockpitPullRequestLimitIsConfigured(t *testing.T) {
	t.Parallel()
	got, err := parseCockpit([]byte("cockpit:\n  pull_request_limit: 25\n  pull_request_hourly_budget: 300\n"))
	if err != nil || got.PullRequestLimit != 25 || got.PullRequestHourlyBudget != 300 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestCockpitCodeIndexProviderIsConfiguredWithItsIndexer(t *testing.T) {
	t.Parallel()
	got, err := parseCockpit([]byte("cockpit:\n  code_index_provider: codegrapher\n"))
	if err != nil || got.CodeIndexProvider != "codegrapher" || got.CodeIndexIndexer != "" {
		t.Fatalf("provider only = %+v, %v", got, err)
	}
	got, err = parseCockpit([]byte("cockpit:\n  code_index_provider: \" codegrapher \"\n  code_index_indexer: code-graph\n"))
	if err != nil || got.CodeIndexProvider != "codegrapher" || got.CodeIndexIndexer != "code-graph" {
		t.Fatalf("provider and indexer = %+v, %v", got, err)
	}
	for raw, want := range map[string]string{
		"cockpit:\n  code_index_provider: other\n":                                  "cockpit.code_index_provider",
		"cockpit:\n  code_index_provider: \"\"\n":                                   "cockpit.code_index_provider",
		"cockpit:\n  code_index_indexer: code-graph\n":                              "needs cockpit.code_index_provider",
		"cockpit:\n  code_index_provider: codegrapher\n  code_index_indexer: A b\n": "cockpit.code_index_indexer",
	} {
		if _, err := parseCockpit([]byte(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want it to name %q", raw, err, want)
		}
	}
}

func TestCockpitSectionRejectsInvalidValues(t *testing.T) {
	t.Parallel()
	for raw, wantKey := range map[string]string{
		"cockpit:\n  anonymous_metdata: false\n":                   "anonymous_metdata",
		"cockpit:\n  hosted_url: https://x.example/\n  extra: 1\n": "extra",
		"cockpit:\n  refresh_interval: soon\n":                     "cockpit.refresh_interval",
		"cockpit:\n  refresh_interval: -5s\n":                      "cockpit.refresh_interval",
		"cockpit:\n  refresh_interval: 0s\n":                       "cockpit.refresh_interval",
		"cockpit:\n  pull_request_limit: 0\n":                      "cockpit.pull_request_limit",
		"cockpit:\n  pull_request_limit: 201\n":                    "cockpit.pull_request_limit",
		"cockpit:\n  pull_request_hourly_budget: 9\n":              "cockpit.pull_request_hourly_budget",
		"cockpit:\n  pull_request_hourly_budget: 2001\n":           "cockpit.pull_request_hourly_budget",
		"cockpit:\n  anonymous_metadata: maybe\n":                  "parse cockpit section",
		"cockpit: [unterminated\n":                                 "parse config",
		"cockpit: just text\n":                                     "parse cockpit section",
		"cockpit:\n  code_browser_url: nope\n":                     "cockpit.code_browser_url",
	} {
		if _, err := parseCockpit([]byte(raw)); err == nil || !strings.Contains(err.Error(), wantKey) {
			t.Errorf("%q: err = %v, want it to name %q", raw, err, wantKey)
		}
	}
}

func TestCockpitURLsFollowTheHubAddressRule(t *testing.T) {
	t.Parallel()
	for value, ok := range map[string]bool{
		"https://sneat.dev/wb/cockpit/":        true,
		"https://hosted.example.test/":         true,
		"http://localhost:8080/c/":             true,
		"http://127.0.0.1:9000/":               true,
		"http://[::1]:9000/":                   true,
		"http://hosted.example.test/":          false,
		"ftp://hosted.example.test/":           false,
		"hosted.example.test/wb":               false,
		"/relative":                            false,
		"https:///nohost":                      false,
		"https://user:pw@hosted.example.test/": false,
		"https://user@hosted.example.test/":    false,
		"https://hosted.example.test/?a=1":     false,
		"https://hosted.example.test/#frag":    false,
		"https://:443/":                        false,
		"https://bad host/":                    false,
		"":                                     false,
	} {
		err := validateCockpitURL("hosted_url", value)
		if (err == nil) != ok {
			t.Errorf("%q: err = %v, want ok = %t", value, err, ok)
		}
	}
}

func TestCockpitURLsAreStoredTrimmed(t *testing.T) {
	t.Parallel()
	got, err := parseCockpit([]byte("cockpit:\n  hosted_url: \"  https://hosted.example.test/c/ \"\n  code_browser_url: \" https://code.example.test/\\t\"\n"))
	if err != nil || got.HostedURL != "https://hosted.example.test/c/" || got.CodeBrowserURL != "https://code.example.test/" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestLoadCockpitReadsFileAndTreatsAbsenceAsDefaults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got, err := LoadCockpit(filepath.Join(dir, "missing.yaml")); err != nil || got != DefaultCockpitConfig() {
		t.Fatalf("missing file = %+v, %v", got, err)
	}
	good := filepath.Join(dir, "wb.yaml")
	if err := os.WriteFile(good, []byte("cockpit:\n  refresh_interval: 1m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadCockpit(good); err != nil || got.RefreshInterval != time.Minute {
		t.Fatalf("good file = %+v, %v", got, err)
	}
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("cockpit:\n  hosted_url: nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCockpit(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("bad file err = %v, want it to name the path", err)
	}
	if _, err := LoadCockpit(dir); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("directory err = %v", err)
	}
}
