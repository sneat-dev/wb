package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// TestStatusCommandReportsTheFleetWorklistInProcess proves that "wb status"
// (with no path, the fleet worklist) reaches the shared status collector through the
// real command tree, threading its invocation into the progress reporter.
func TestStatusCommandReportsTheFleetWorklistInProcess(t *testing.T) {
	root := t.TempDir()
	cwCovCloneWithOrigin(t, t.TempDir(), "app", filepath.Join(root, "acme", "app"))
	var stdout string
	var err error
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newStatusCmd(&invocation{projectsRoot: root}) }, "--format", "json", "--all")
	if err != nil {
		t.Fatalf("wb status: %v\n%s", err, stdout)
	}
	var report statusIndex
	if jsonErr := json.Unmarshal([]byte(stdout), &report); jsonErr != nil {
		t.Fatalf("status JSON: %v\n%s", jsonErr, stdout)
	}
	if len(report.Repositories) != 1 || report.Repositories[0].Repository != "acme/app" || report.Repositories[0].Status != "clean" {
		t.Fatalf("status report = %+v", report)
	}
}

// TestStatusFiltersACleanFleetEndToEnd runs the built binary over a two-repo
// fleet, because the filter is only useful if the flag is actually wired to
// the fleet path and left off the single-repository path.
func TestStatusFiltersACleanFleetEndToEnd(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	clean := initTestRepository(t, filepath.Join(root, "acme", "clean"))
	initTestRepository(t, filepath.Join(root, "acme", "dirty"))
	if err := os.WriteFile(filepath.Join(root, "acme", "dirty", "notes.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fleet := decodeStatusIndex(t, runWB(t, "status", "--projects-root", root, "--format", "json"))
	if len(fleet.Repositories) != 1 || fleet.Repositories[0].Repository != "acme/dirty" {
		t.Errorf("default fleet report = %+v, want only acme/dirty", fleet.Repositories)
	}
	if fleet.HiddenClean != 1 {
		t.Errorf("hidden_clean = %d, want 1", fleet.HiddenClean)
	}

	everything := decodeStatusIndex(t, runWB(t, "status", "--projects-root", root, "--format", "json", "--all"))
	if len(everything.Repositories) != 2 {
		t.Errorf("--all report = %+v, want both repositories", everything.Repositories)
	}
	if everything.HiddenClean != 0 {
		t.Errorf("--all hid %d repositories; it must hide none", everything.HiddenClean)
	}
	selected := decodeStatusIndex(t, runWB(t, "status", "--projects-root", root, "--filter", "acme/clean", "--format", "json", "--all"))
	if len(selected.Repositories) != 1 || selected.Repositories[0].Repository != "acme/clean" {
		t.Errorf("--projects-root + --filter selection = %+v, want only acme/clean", selected.Repositories)
	}

	single := decodeStatusIndex(t, runWB(t, "status", clean, "--format", "json"))
	if len(single.Repositories) != 1 || single.Repositories[0].Status != "clean" {
		t.Errorf("naming one clean repository reported %+v; it must answer for that checkout", single.Repositories)
	}
}

func decodeStatusIndex(t *testing.T, result smokeResult) statusIndex {
	t.Helper()
	if result.exitCode != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr: %s", result.exitCode, exitOK, result.stderr)
	}
	var report statusIndex
	if err := json.Unmarshal([]byte(result.stdout), &report); err != nil {
		t.Fatalf("stdout is not a status index: %v\nstdout: %s", err, result.stdout)
	}
	return report
}

// initTestRepository creates a repository with no commits and no remote, which
// gitops.Status reports as clean, and returns its path.
func initTestRepository(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", path, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("init %s: %v\n%s", path, err, output)
	}
	return path
}
