//go:build e2e && linux

package migrate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2ECampaignFixtureGoTelemetryOffKeepsTheSidecarOutOfTheTempTree pins the
// fixture contract that keeps the go command's detached telemetry sidecar away
// from a fixture-owned XDG_CONFIG_HOME. The fixture writes Go's own mode file,
// so the next go command reports telemetry off and creates no telemetry
// database directory there -- the directory the sidecar would take as its
// working directory and write into while t.TempDir() removes the tree.
func TestE2ECampaignFixtureGoTelemetryOffKeepsTheSidecarOutOfTheTempTree(t *testing.T) {
	t.Parallel()
	configHome := filepath.Join(t.TempDir(), "config")
	disableGoTelemetryForFixture(t, configHome)

	command := exec.Command("go", "env", "GOTELEMETRY")
	// Scope the go command's telemetry -- and the detached sidecar it can
	// start -- to this fixture's private config root. Only the child process
	// environment is changed; this test mutates no process-wide state, so it
	// stays parallel-safe. exec.Cmd deduplicates repeated names, keeping the
	// last value appended here.
	command.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+configHome,
		// cmd/go's telemetry shim honors this test-only override ahead of
		// XDG_CONFIG_HOME; clear it so the fixture's own configuration is what
		// the go command reads.
		"TEST_TELEMETRY_DIR=",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go env GOTELEMETRY: %v: %s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "off" {
		t.Fatalf("GOTELEMETRY = %q, want off", got)
	}
	if _, statErr := os.Stat(filepath.Join(configHome, "go", "telemetry", "local")); !os.IsNotExist(statErr) {
		t.Fatalf("the go command created a telemetry database directory with telemetry off: %v", statErr)
	}
}
