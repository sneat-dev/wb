package cockpitoptions

import (
	"bytes"
	"io"
	"testing"

	"github.com/sneat-dev/wb/internal/wbconfig"
)

func TestCockpitFleetOptionsPublishOnlyWhenTheOwnerOptedIn(t *testing.T) {
	t.Parallel()
	host := func() (string, error) { return "h", nil }
	build := func(content string) bool {
		var logs bytes.Buffer
		options := testDefaultOptions(t.TempDir(), t.TempDir(), cockpitConfigFile(t, content)(), wbconfig.DefaultCockpitConfig(), &logs, host)
		return options.Publisher != nil
	}
	const store = "remote:\n  provider: git\n  repo: acme/wb-state\n  machine: laptop-1\n"
	for name, test := range map[string]struct {
		config string
		want   bool
	}{
		"hand-published git store":         {store, false},
		"only the redaction":               {store + "  publish:\n    unpushed: counts\n", false},
		"flags without an interval":        {store + "  publish:\n    agents: true\n    metrics: true\n", false},
		"interval set":                     {store + "  publish:\n    interval: 10m\n", true},
		"interval below the minimum":       {store + "  publish:\n    interval: 1m\n", true},
		"hub with an interval":             {"remote:\n  provider: hub\n  url: https://hub.example\n  token_file: /tmp/token\n  machine: laptop-2\n  publish:\n    interval: 15m\n", true},
		"negative interval is a bad value": {store + "  publish:\n    interval: -5m\n", false},
		"no remote section":                {"cockpit: {}\n", false},
	} {
		if got := build(test.config); got != test.want {
			t.Errorf("%s: publisher = %v, want %v", name, got, test.want)
		}
	}
	if none := testDefaultOptions(t.TempDir(), t.TempDir(), t.TempDir()+"/absent.yaml", wbconfig.DefaultCockpitConfig(), io.Discard, host); none.Publisher != nil {
		t.Error("a machine with no wb.yaml has a publisher")
	}
	// The snapshotter is always told where the login will be known from, and it is
	// not known before anything resolved it.
	if wired := testDefaultOptions(t.TempDir(), t.TempDir(), t.TempDir()+"/absent.yaml", wbconfig.DefaultCockpitConfig(), io.Discard, host); wired.LoginSource == nil || wired.LoginSource() != "" || wired.Login != "" {
		t.Error("the fleet options name no login source, or one that knows a login already")
	}
}
