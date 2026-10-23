package testenv

import (
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/provenance"
)

//nolint:paralleltest // sets and unsets process environment variables
func TestIsolateHarnessProcessRemovesEveryHarnessVariableAndKeepsTheRest(t *testing.T) {
	removed := []string{
		provenance.EnvHarness, provenance.EnvHarnessSessionID, provenance.EnvEffortLevel,
		"CLAUDECODE", "CLAUDE_PID", "CLAUDE_CODE_EXECPATH",
	}
	for _, name := range removed {
		t.Setenv(name, "ambient")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "kept")

	IsolateHarnessProcess()

	for _, name := range removed {
		if value, set := os.LookupEnv(name); set {
			t.Errorf("%s is still set to %q after IsolateHarnessProcess", name, value)
		}
	}
	if got := os.Getenv("CLAUDE_CONFIG_DIR"); got != "kept" {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want it left alone", got)
	}
	if fields := provenance.FromEnv(); fields.HarnessSessionID != "" || fields.Harness != "" {
		t.Errorf("provenance still reads a harness after isolation: %+v", fields)
	}
}
