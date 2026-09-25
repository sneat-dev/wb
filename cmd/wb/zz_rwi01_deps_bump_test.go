package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/deps"
)

// TestRwi01RunDepsBumpEnsureRootFailureFinishesCampaignAsFailed forces
// wbhome.EnsureRoot to fail (a component of the projects root is a regular
// file, so os.MkdirAll for <root>/.wb cannot succeed) before the wave engine
// ever runs. That exercises two branches of runDepsBump in one call: the
// campaign is told "failed" (never "completed"), and since the report was
// never produced (report.Operation == ""), runDepsBump returns the
// underlying error directly instead of trying to write a report.
func TestRwi01RunDepsBumpEnsureRootFailureFinishesCampaignAsFailed(t *testing.T) {
	tmp := t.TempDir()
	blockingFile := filepath.Join(tmp, "not-a-directory")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("seeding a blocking file: %v", err)
	}
	inv := &invocation{projectsRoot: filepath.Join(blockingFile, "child")}

	var progressOut bytes.Buffer
	campaign := newCampaignProgress(&progressOut, true, "deps bump")
	command := &cobra.Command{}

	err := runDepsBump(inv, command, deps.EcosystemGo, nil, nil, depsSetOptions{campaign: campaign}, deps.Options{})
	if err == nil {
		t.Fatal("runDepsBump with an unwritable projects root: want an error, got nil")
	}
	if !strings.Contains(progressOut.String(), "deps bump: failed") {
		t.Fatalf("campaign progress = %q, want it to report \"deps bump: failed\"", progressOut.String())
	}
	if strings.Contains(progressOut.String(), "deps bump: completed") {
		t.Fatalf("campaign progress = %q, want it never to report completed", progressOut.String())
	}
}

// TestRwi01ExecuteDepsBumpWithRegistryPolicyNoEventsReturnsEmptyReport
// covers executeDepsBumpWithRegistryPolicy's report.Operation == "" branch:
// deps.RunBump rejects zero release events before acquiring any lock or
// touching a repository, so the wrapper must hand back the bare error
// instead of trying to persist a report that was never produced.
func TestRwi01ExecuteDepsBumpWithRegistryPolicyNoEventsReturnsEmptyReport(t *testing.T) {
	root := t.TempDir()
	inv := &invocation{projectsRoot: root, nonInteractive: true}
	command := &cobra.Command{}
	command.SetErr(&bytes.Buffer{})

	report, reportDirectory, err := executeDepsBumpWithRegistryPolicy(inv, command, deps.EcosystemGo, nil, nil, depsSetOptions{}, deps.Options{}, false)
	if err == nil || !strings.Contains(err.Error(), "at least one --changed module@version event is required") {
		t.Fatalf("executeDepsBumpWithRegistryPolicy with no events: err = %v, want the missing-events refusal", err)
	}
	if report.Operation != "" {
		t.Fatalf("report.Operation = %q, want empty since RunBump never produced a report", report.Operation)
	}
	if !strings.Contains(reportDirectory, "deps-bump") {
		t.Fatalf("reportDirectory = %q, want it under a deps-bump-* home directory", reportDirectory)
	}
}

// TestRwi01ResolveDepsBumpResumeParallelRejectsAnInvalidPersistedValue
// covers the report.Parallel < 1 branch: a persisted report with a zero or
// negative parallelism (corrupt, or from before parallelism was recorded)
// must be rejected rather than silently resumed at zero workers, when the
// operator did not pass --parallel themselves to override it.
func TestRwi01ResolveDepsBumpResumeParallelRejectsAnInvalidPersistedValue(t *testing.T) {
	lifecycle := deps.Options{Parallel: 4}
	report := deps.BumpReport{Parallel: 0}
	_, _, err := resolveDepsBumpResumeParallel(lifecycle, report, false)
	if err == nil || !strings.Contains(err.Error(), "resume report has invalid parallelism") {
		t.Fatalf("resolveDepsBumpResumeParallel(report.Parallel=0, explicit=false) = %v, want the invalid-parallelism refusal", err)
	}
}
