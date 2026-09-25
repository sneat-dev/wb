package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/npmrelease"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// TestCgxc2RunPreparedNpmPublishApplyDispatchFailureWritesFailedReport drives
// the publicationErr != nil branch of runPreparedNpmPublishLocked: the
// injected runner fails immediately, so npmrelease.Run itself reports
// failure, the report is still persisted, and the command surfaces a
// findings-tier exitError rather than swallowing the failure.
func TestCgxc2RunPreparedNpmPublishApplyDispatchFailureWritesFailedReport(t *testing.T) {
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)

	runner := &cwDepsFakeNpmReleaseRunner{steps: []npmrelease.CommandResult{
		{Code: 1, Err: errors.New("cgxc2 fixture: resolve head failed")},
	}}
	options := cwDepsPublishOptionsFixture()
	options.fleet = true
	options.apply = true
	options.maxWaves = 1
	options.runner = runner
	prepared := npmPublishPrepared{
		releases:  []npmrelease.Release{cwDepsReleaseFixture()},
		checks:    []quality.Check{},
		reportDir: filepath.Join(home, "reports", "cgxc2-npm-apply-fail"),
		operation: "deps-npm-publish-cgxc2-fail",
	}
	var out bytes.Buffer
	command := cwDepsNewOutCommand(&out)
	err := runPreparedNpmPublishLocked(command, options, prepared, &invocation{projectsRoot: home})
	if err == nil {
		t.Fatalf("expected publication failure error, got nil")
	}
	exitErr, ok := err.(*exitError)
	if !ok {
		t.Fatalf("err = %v (%T), want *exitError", err, err)
	}
	if exitErr.code != exitFindings {
		t.Errorf("exitErr.code = %d, want %d", exitErr.code, exitFindings)
	}
	if !strings.Contains(err.Error(), "npm publication did not reach registry evidence") {
		t.Fatalf("err = %v", err)
	}
	persisted, readErr := os.ReadFile(filepath.Join(prepared.reportDir, "npm-publish.json"))
	if readErr != nil {
		t.Fatalf("read persisted report: %v", readErr)
	}
	if !json.Valid(persisted) {
		t.Fatalf("persisted report is not valid JSON: %s", persisted)
	}
	var report npmrelease.Report
	if err := json.Unmarshal(persisted, &report); err != nil {
		t.Fatalf("decode persisted report: %v", err)
	}
	if report.Status == npmrelease.StatusPublished {
		t.Errorf("persisted report status = %v, want a non-published status on failure", report.Status)
	}
}
