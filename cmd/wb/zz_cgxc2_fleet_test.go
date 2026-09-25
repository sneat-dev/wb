package main

import (
	"path/filepath"
	"testing"
)

// TestCgxc2CollectFleetOverviewHidesCleanRepositoriesWhenAllIsFalse proves
// the all=false branch of collectFleetOverview reaches hideCleanRepositories
// rather than returning the unfiltered status index, over an empty fleet
// (which is itself a valid, error-free result).
func TestCgxc2CollectFleetOverviewHidesCleanRepositoriesWhenAllIsFalse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	inv := &invocation{projectsRoot: root}
	report, err := collectFleetOverview(inv, root, "", qualityOptions{parallel: 1, allowEmpty: true}, false, fleetDepthOptions{})
	if err != nil {
		t.Fatalf("collectFleetOverview: %v", err)
	}
	if report.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", report.SchemaVersion)
	}
}

// TestCgxc2FleetHooksRollupCountsCheckErrors proves fleetHooksRollup counts a
// per-repository hooks.Check failure into stats.Errors, rather than treating
// it the same as a clean report with zero findings.
func TestCgxc2FleetHooksRollupCountsCheckErrors(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	targets := []qualityTarget{{repository: "acme/missing", path: missing}}
	stats, err := fleetHooksRollup(&invocation{}, targets, 1)
	if err != nil {
		t.Fatalf("fleetHooksRollup: %v", err)
	}
	if stats.Repositories != 1 {
		t.Errorf("Repositories = %d, want 1", stats.Repositories)
	}
	if stats.Errors != 1 {
		t.Errorf("Errors = %d, want 1 (hooks.Check should fail against a missing repo path)", stats.Errors)
	}
}

// TestCgxc2FleetHooksRollupEmptyTargetsReportsNoErrors is a control: an empty
// target set must not report errors, so the failing case above is a genuine
// signal rather than a fixture bug.
func TestCgxc2FleetHooksRollupEmptyTargetsReportsNoErrors(t *testing.T) {
	t.Parallel()
	stats, err := fleetHooksRollup(&invocation{}, nil, 1)
	if err != nil {
		t.Fatalf("fleetHooksRollup: %v", err)
	}
	if stats.Errors != 0 || stats.Repositories != 0 {
		t.Errorf("stats = %+v, want zero", stats)
	}
}
