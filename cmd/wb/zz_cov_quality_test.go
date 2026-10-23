package main

import (
	"encoding/json"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/qualityrun"
	"github.com/spf13/cobra"
	"strings"
	"testing"
)

func TestCwCovQualityCommandsInProcess(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WB_HOME", t.TempDir())

	// An empty repository has no Go module, so coverage reports it as skipped
	// rather than running a suite.
	var stdout string
	var err error
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newCoverageCmd(&invocation{}) }, root, "--format", "json")
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("coverage exit = %d\n%s", code, stdout)
	}
	var report quality.CoverageReport
	if jsonErr := json.Unmarshal([]byte(stdout), &report); jsonErr != nil {
		t.Fatalf("coverage JSON: %v\n%s", jsonErr, stdout)
	}
	if len(report.Repositories) != 1 || report.Repositories[0].Status != quality.StatusSkipped {
		t.Fatalf("coverage report = %+v", report)
	}

	// verify with only the build check over an empty directory.
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newVerifyCmd(&invocation{}) }, root, "--checks", "build", "--format", "json")
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("verify exit = %d\n%s", code, stdout)
	}
	var index qualityrun.VerificationIndex
	if jsonErr := json.Unmarshal([]byte(stdout), &index); jsonErr != nil {
		t.Fatalf("verify JSON: %v\n%s", jsonErr, stdout)
	}
	if index.SchemaVersion != 1 || len(index.Repositories) != 1 {
		t.Fatalf("verification index = %+v", index)
	}

	// check runs the named profile's checks over one repository.
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newCheckCmd(&invocation{}) }, root, "--profile", "fast", "--format", "json")
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("check exit = %d\n%s", code, stdout)
	}
	if jsonErr := json.Unmarshal([]byte(stdout), &index); jsonErr != nil {
		t.Fatalf("check JSON: %v\n%s", jsonErr, stdout)
	}
	if index.SchemaVersion != 1 || len(index.Repositories) != 1 || index.Profile != "fast" {
		t.Fatalf("check index = %+v", index)
	}

	// Usage refusals happen before any check runs.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newCoverageCmd(&invocation{}) }, root, "--fleet"); err == nil ||
		!strings.Contains(err.Error(), "cannot be used with --fleet") {
		t.Fatalf("coverage --fleet with a path = %v", err)
	}
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newVerifyCmd(&invocation{}) }, root, "--fleet"); err == nil {
		t.Fatal("verify --fleet with a path must be refused")
	}
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newCheckCmd(&invocation{}) }, root, "--fleet", "--profile", "fast"); err == nil {
		t.Fatal("check --fleet with a path must be refused")
	}
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newCoverageCmd(&invocation{}) }, root, "--minimum", "200"); err == nil ||
		!strings.Contains(err.Error(), "--minimum must be between") {
		t.Fatalf("coverage --minimum 200 = %v", err)
	}
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newCoverageCmd(&invocation{}) }, root, "--test-shards", "0"); err == nil {
		t.Fatal("coverage --test-shards 0 must be refused")
	}
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newVerifyCmd(&invocation{}) }, root, "--checks", "lint,bogus"); err == nil {
		t.Fatal("verify must refuse an unknown check")
	}
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newCheckCmd(&invocation{}) }, root, "--profile", "turbo"); err == nil ||
		!strings.Contains(err.Error(), "unknown check profile") {
		t.Fatalf("check --profile turbo = %v", err)
	}
	// --resume without --report-dir is refused, not silently ignored.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newCoverageCmd(&invocation{}) }, root, "--resume"); err == nil ||
		!strings.Contains(err.Error(), "--report-dir") {
		t.Fatalf("coverage --resume without report dir = %v", err)
	}
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newVerifyCmd(&invocation{}) }, root, "--resume", "--checks", "build"); err == nil ||
		!strings.Contains(err.Error(), "--report-dir") {
		t.Fatalf("verify --resume without report dir = %v", err)
	}
}
