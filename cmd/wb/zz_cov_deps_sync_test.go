package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

// Repo2TransferFrom was avoided; see the transferred fixture above.

// TestCwDepsFinishSyncReportsIssuesAndPublishIntent covers the exit-code
// mapping and the dry-run publish refusal without touching the network.

// TestCwDepsRunSyncPlainSyncsLocalClonesInParallel drives the non-TUI worker
// pool directly against scratch clones: dry-run keeps it read-only.

// TestCwDepsRunSyncReportsFleetState covers the whole runSync pipeline with a
// hermetic gh: a dry run over local clones reports and exits without mutating.
func TestCwDepsRunSyncReportsFleetState(t *testing.T) {
	projects := cwCovProjectsRoot(t, "acme/app", "acme/other")
	cwCovFakeGH(t, "cwcov-user", []string{"acme"}, `[]`)
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)

	var out, errOut bytes.Buffer
	code := runSync(context.Background(), &invocation{nonInteractive: true}, projects, "", []string{"cwcov-user", "acme"}, 2, true, false, false,
		remoteDeps{}, &out, &errOut)
	if code != 0 {
		t.Fatalf("dry-run sync exit = %d\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}
	// Both local clones are discovered and classified; the summary counts
	// them rather than listing every repository.
	for _, want := range []string{"Summary", "Final outcomes", "Not owned               2", "Sync issues: 0 records"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry-run sync report missing %q:\n%s", want, out.String())
		}
	}
}

// TestCwDepsRunSyncWithoutRepositoriesSaysSo keeps the empty-fleet path
// explicit: sync reports it rather than printing an empty summary.
func TestCwDepsRunSyncWithoutRepositoriesSaysSo(t *testing.T) {
	projects := t.TempDir()
	cwCovFakeGH(t, "cwcov-user", nil, `[]`)
	t.Setenv(wbhome.EnvOverride, t.TempDir())

	var out, errOut bytes.Buffer
	if code := runSync(context.Background(), &invocation{nonInteractive: true}, projects, "", []string{"cwcov-user"}, 1, true, false, false,
		remoteDeps{}, &out, &errOut); code != 0 {
		t.Fatalf("empty sync exit = %d\n%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "no repos found") {
		t.Errorf("empty fleet report = %q", out.String())
	}
}

// TestCwDepsSyncCommandDispatchesToRunSyncInProcess proves that "wb sync"
// reaches requestedSyncOwners and runSync through the real command tree,
// threading its invocation through both, not just through direct unit calls.
func TestCwDepsSyncCommandDispatchesToRunSyncInProcess(t *testing.T) {
	cwCovFakeGH(t, "cwcov-user", nil, `[]`)
	t.Setenv(wbhome.EnvOverride, t.TempDir())

	projectsRoot := t.TempDir()
	stdout, _, err := cwCovExec(t, projectsRoot, func() *cobra.Command {
		return newSyncCmd(&invocation{projectsRoot: projectsRoot, nonInteractive: true})
	},
		"--dry-run")
	if err != nil {
		t.Fatalf("wb sync --dry-run: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "no repos found") {
		t.Errorf("empty fleet report = %q", stdout)
	}
}

// TestCwDepsRunSyncRefusesBrokenAuthentication proves an authentication
// failure is a finding, not a silently unmanaged-but-successful sync.
func TestCwDepsRunSyncRefusesBrokenAuthentication(t *testing.T) {
	binDir := t.TempDir()
	if err := cwCovWriteExecutable(filepath.Join(binDir, "gh"), "#!/bin/sh\nexit 1\n"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(wbhome.EnvOverride, t.TempDir())

	var out, errOut bytes.Buffer
	code := runSync(context.Background(), &invocation{nonInteractive: true}, t.TempDir(), "", nil, 1, true, false, false, remoteDeps{}, &out, &errOut)
	if code != exitFindings {
		t.Fatalf("broken auth sync exit = %d, want findings\n%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "Re-authenticate with: gh auth login") {
		t.Errorf("broken auth stderr = %q", errOut.String())
	}
}

// TestCwDepsRunQueueSummaryKeepsTelemetryPrivate covers the privacy-safe label
// derivation used by the CPU queue receipts.

// TestCwDepsPrintRunQueueRendersRunningAndWaitingSeats covers both queue
// listing shapes in text and JSON.

// cwCovWriteExecutable writes a small executable script for PATH scoping.
func cwCovWriteExecutable(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return testenv.WriteExecutableFile(path, []byte(body), 0o755)
}
