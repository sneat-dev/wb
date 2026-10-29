package migrate

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		if helper, known := worktrees.SecureGitHelperForArgument(os.Args[1]); known {
			os.Exit(helper(os.Args[2:]))
		}
	}
	// See internal/testenv: strip inherited WB_AGENT_* and pin GOWORK=off
	// before any migrate/campaign test runs.
	testenv.IsolateProcess()
	// Disable git's detached gc/maintenance for every git this binary
	// starts, including this package's own fixture clones and pushes, so
	// no background writer can race t.TempDir() cleanup (task-21). Bare
	// remotes pushed to over a local transport are also configured
	// directly with testenv.ConfigureGitAutoMaintenanceOff.
	testenv.GitAutoMaintenanceOffProcess()
	os.Exit(m.Run())
}

// disableGoTelemetryForFixture turns the Go toolchain's telemetry off inside a
// fixture's own XDG_CONFIG_HOME, so a `go` command the fixture runs cannot
// leave a detached writer behind in a directory t.TempDir() is about to remove.
//
// The go command starts its telemetry sidecar early: cmd/go's main calls
// telemetry.MaybeParent, and when the telemetry database has no fresh
// upload.token the parent acquires one and starts a setsid'd
// `go ** telemetry **` child whose working directory is
// $XDG_CONFIG_HOME/go/telemetry/local (golang.org/x/telemetry Start/startChild;
// Go's own source notes the sidecar "may outlive the parent"). A fixture that
// points XDG_CONFIG_HOME into its t.TempDir() therefore runs its own commands
// and then, while testing.TempDir() is recursively removing that tree, a live
// process still creating counter files and upload state inside it. That is the
// "TempDir RemoveAll cleanup: ... directory not empty" race.
//
// The mode file is exactly what `go telemetry off` writes: "<mode> <date>",
// with the date as time.DateOnly in UTC (x/telemetry's
// internal/telemetry.Dir.SetModeAsOf). With that mode the go command returns
// from telemetry's parent path before it opens a counter file or starts the
// sidecar, so it creates nothing under the fixture root.
//
// The scope is XDG_CONFIG_HOME and therefore Linux: Go derives its telemetry
// directory from os.UserConfigDir, which honors XDG_CONFIG_HOME on Linux and a
// different per-OS location elsewhere. The helper only writes the mode file
// inside the private configHome it is given -- on other platforms that is just
// a private fake config directory, not an ambient telemetry setting -- so the
// machine's own telemetry configuration is untouched everywhere. The native
// probe that asserts the observed GOTELEMETRY value is Linux-only.
func disableGoTelemetryForFixture(t *testing.T, configHome string) {
	t.Helper()
	telemetryDir := filepath.Join(configHome, "go", "telemetry")
	if err := os.MkdirAll(telemetryDir, 0o755); err != nil {
		t.Fatalf("create fixture telemetry directory: %v", err)
	}
	mode := "off " + time.Now().UTC().Format(time.DateOnly)
	if err := os.WriteFile(filepath.Join(telemetryDir, "mode"), []byte(mode), 0o666); err != nil {
		t.Fatalf("write fixture telemetry mode: %v", err)
	}
}
