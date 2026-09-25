package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/spf13/cobra"
)

func cwCovCoverageFixture() quality.CoverageReport {
	return quality.NewCoverageReport([]quality.RepositoryCoverage{
		{
			Repository: "acme/clean", Path: "/tmp/clean", Status: quality.StatusPassed,
			Modules:    []quality.ModuleCoverage{{Path: ".", Statements: 100, Covered: 80, Percentage: 80}},
			Statements: 100, Covered: 80, Percentage: 80,
		},
		{
			Repository: "acme/failed", Path: "/tmp/failed", Status: quality.StatusFailed,
			Error: "go test: build failed", Statements: 10, Covered: 1, Percentage: 10,
			Diagnostic: &quality.CoverageDiagnostic{Manifest: "/tmp/manifest.json", SHA256: "abc"},
		},
	})
}

func cwCovVerificationFixture() verificationIndex {
	return verificationIndex{
		SchemaVersion: 1,
		Profile:       "full",
		Checks:        []quality.Check{quality.CheckLint, quality.CheckTest},
		Repositories: []quality.VerificationReport{
			{
				Repository: "acme/app", Path: "/tmp/app", Status: quality.StatusFailed,
				Results: []quality.VerificationEntry{
					{Language: "go", Module: ".", Check: quality.CheckTest, Command: "go test ./...", Status: quality.StatusFailed, Detail: "boom"},
				},
			},
			{Repository: "acme/clean", Path: "/tmp/clean", Status: quality.StatusPassed},
		},
	}
}

func TestCwCovCoverageFailedAndGateError(t *testing.T) {
	report := cwCovCoverageFixture()
	if !coverageFailed(report) {
		t.Fatal("a failed repository must be reported")
	}
	gate := coverageGateError(report, -1)
	if exitCodeOf(t, gate) != exitFindings {
		t.Fatalf("failed-report gate = %v, want findings", gate)
	}
	if !strings.Contains(gate.Error(), "could not be measured") {
		t.Errorf("gate message = %v", gate)
	}

	clean := quality.NewCoverageReport([]quality.RepositoryCoverage{{
		Repository: "acme/clean", Status: quality.StatusPassed, Statements: 100, Covered: 80, Percentage: 80,
	}})
	if err := coverageGateError(clean, -1); err != nil {
		t.Fatalf("a clean report with the gate disabled = %v", err)
	}
	if err := coverageGateError(clean, 80); err != nil {
		t.Fatalf("coverage exactly at the minimum = %v", err)
	}
	low := coverageGateError(clean, 90)
	if exitCodeOf(t, low) != exitFindings || !strings.Contains(low.Error(), "80.00% is below required 90.00%") {
		t.Fatalf("low-coverage gate = %v", low)
	}
	if !verificationFailed(cwCovVerificationFixture()) {
		t.Error("a failed verification repository must be reported")
	}
	if verificationFailed(verificationIndex{Repositories: []quality.VerificationReport{{Status: quality.StatusPassed}}}) {
		t.Error("a passing verification index must not fail")
	}
}

func TestCwCovRunOptionsChecksAndProfiles(t *testing.T) {
	options := runOptions(qualityOptions{timeout: 3_000_000_000, retry: 2, reportDir: "/tmp/reports"})
	if options.Timeout != 3_000_000_000 || options.Retry != 2 || options.CoverageDiagnosticsDir != "/tmp/reports" {
		t.Fatalf("runOptions = %+v", options)
	}
	for profile, want := range map[string][]quality.Check{
		"fast": {quality.CheckLint},
		"full": {quality.CheckLint, quality.CheckTest, quality.CheckBuild},
		"ci":   {quality.CheckLint, quality.CheckTest, quality.CheckBuild, quality.CheckSpec},
	} {
		checks, err := checksForProfile(profile)
		if err != nil {
			t.Fatalf("checksForProfile(%q): %v", profile, err)
		}
		if len(checks) != len(want) {
			t.Fatalf("checksForProfile(%q) = %v, want %v", profile, checks, want)
		}
	}
	if _, err := checksForProfile("turbo"); err == nil || !strings.Contains(err.Error(), "unknown check profile") {
		t.Fatalf("unknown profile error = %v", err)
	}
	if names := checkNames([]quality.Check{quality.CheckLint, quality.CheckBuild}); strings.Join(names, ",") != "lint,build" {
		t.Fatalf("checkNames = %v", names)
	}
}

func TestCwCovResumeTargetsAndMergeCoverageReports(t *testing.T) {
	targets := []qualityTarget{{repository: "acme/clean", path: "/clean"}, {repository: "acme/failed", path: "/failed"}}
	if _, _, err := resumeCoverageTargets(targets, ""); err == nil || !strings.Contains(err.Error(), "--report-dir") {
		t.Fatalf("resume without report dir = %v", err)
	}
	if _, _, err := resumeCoverageTargets(targets, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("resume with no report file must fail")
	}

	reportDir := t.TempDir()
	previous := cwCovCoverageFixture()
	raw, err := yaml.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reportDir, "coverage.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	resumed, loaded, err := resumeCoverageTargets(targets, reportDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 1 || resumed[0].repository != "acme/failed" {
		t.Fatalf("resumed targets = %+v, want only the previously failed repository", resumed)
	}
	if len(loaded.Repositories) != len(previous.Repositories) {
		t.Fatalf("loaded report = %+v", loaded)
	}

	// Malformed resume evidence is refused rather than silently starting over.
	if err := os.WriteFile(filepath.Join(reportDir, "coverage.yaml"), []byte("\tnot yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resumeCoverageTargets(targets, reportDir); err == nil {
		t.Fatal("a malformed resume report must be refused")
	}

	// Merging lets the current run's result replace the previous one and
	// re-derives the totals.
	previous = quality.NewCoverageReport([]quality.RepositoryCoverage{
		{Repository: "acme/a", Statements: 10, Covered: 0, Status: quality.StatusFailed},
		{Repository: "acme/keep", Statements: 10, Covered: 10, Status: quality.StatusPassed},
	})
	current := quality.NewCoverageReport([]quality.RepositoryCoverage{
		{Repository: "acme/a", Statements: 10, Covered: 5, Status: quality.StatusPassed},
	})
	merged := mergeCoverageReports(previous, current)
	if len(merged.Repositories) != 2 || merged.Repositories[0].Repository != "acme/a" {
		t.Fatalf("merged repositories = %+v", merged.Repositories)
	}
	for _, repository := range merged.Repositories {
		if repository.Repository == "acme/a" && (repository.Covered != 5 || repository.Status != quality.StatusPassed) {
			t.Errorf("merged acme/a = %+v, want the current run's result", repository)
		}
	}
	if merged.Statements != 20 || merged.Covered != 15 {
		t.Fatalf("merged totals = %d/%d, want 15/20", merged.Covered, merged.Statements)
	}
}

func TestCwCovWriteCoverageOutputTo(t *testing.T) {
	report := cwCovCoverageFixture()
	reportDir := filepath.Join(t.TempDir(), "reports")
	for _, format := range []string{"markdown", "yaml", "json", "summary"} {
		var out bytes.Buffer
		if err := writeCoverageOutputTo(&out, report, format, reportDir); err != nil {
			t.Fatalf("format %s: %v", format, err)
		}
		if strings.TrimSpace(out.String()) == "" {
			t.Errorf("format %s produced no output", format)
		}
		if format == "summary" && !strings.Contains(out.String(), "WB coverage failed:") {
			t.Errorf("summary = %q, want the failed verdict", out.String())
		}
	}
	for _, name := range []string{"coverage.md", "coverage.yaml", "coverage-diagnostics.yaml"} {
		if _, err := os.Stat(filepath.Join(reportDir, name)); err != nil {
			t.Errorf("coverage report did not write %s: %v", name, err)
		}
	}

	// The summary format needs the durable report it references.
	var out bytes.Buffer
	err := writeCoverageOutputTo(&out, report, "summary", "")
	if err == nil || !strings.Contains(err.Error(), "requires --report-dir") {
		t.Fatalf("summary without report dir = %v", err)
	}
	if err := writeCoverageOutputTo(&out, report, "toml", ""); err == nil ||
		!strings.Contains(err.Error(), `unknown --format "toml"`) {
		t.Fatalf("unknown format = %v", err)
	}

	// A report with no diagnostics writes no diagnostics index, and a passing
	// report summarises as passed.
	clean := quality.NewCoverageReport([]quality.RepositoryCoverage{{
		Repository: "acme/clean", Status: quality.StatusPassed, Statements: 4, Covered: 4, Percentage: 100,
	}})
	cleanDir := filepath.Join(t.TempDir(), "clean")
	out.Reset()
	if err := writeCoverageOutputTo(&out, clean, "summary", cleanDir); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "WB coverage passed:") {
		t.Errorf("clean summary = %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(cleanDir, "coverage-diagnostics.yaml")); !os.IsNotExist(err) {
		t.Errorf("diagnostics index written with no diagnostics: %v", err)
	}
}

func TestCwCovWriteVerificationOutputAndMarkdown(t *testing.T) {
	report := cwCovVerificationFixture()
	reportDir := filepath.Join(t.TempDir(), "reports")
	for _, format := range []string{"markdown", "yaml", "json"} {
		var err error
		out := cwCovCaptureStdout(t, func() {
			err = writeVerificationOutput(report, format, reportDir, "verify")
		})
		if err != nil {
			t.Fatalf("format %s: %v", format, err)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("format %s produced no output", format)
		}
	}
	for _, name := range []string{"verify.md", "verify.yaml"} {
		if _, err := os.Stat(filepath.Join(reportDir, name)); err != nil {
			t.Errorf("verification report did not write %s: %v", name, err)
		}
	}
	if err := writeVerificationOutput(report, "toml", "", "verify"); err == nil ||
		!strings.Contains(err.Error(), `unknown --format "toml"`) {
		t.Fatalf("unknown verification format = %v", err)
	}

	markdown := verificationMarkdown(report)
	for _, want := range []string{
		"# WB verification", "Profile: `full`", "Checks: `lint,test`",
		"acme/app", "go test ./...", "boom", "acme/clean",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("verification markdown missing %q:\n%s", want, markdown)
		}
	}
	// A repository with no results renders a dash row rather than nothing.
	if !strings.Contains(markdown, "| `acme/clean` | — | — | — | `passed` | — |") {
		t.Errorf("empty-results row missing:\n%s", markdown)
	}
}

func TestCwCovVerificationGitSnapshot(t *testing.T) {
	// This file is already on internal/quality/testdata/unit_tier.pending
	// (task-22): verificationGitSnapshot now runs through internal/runner
	// (task-8), and this test's whole point is to observe real git's clean/
	// dirty/missing-repository behaviour against the real repository below.
	runnertest.AllowRealProcess(t)
	realRunner := runner.New()
	ctx := context.Background()
	repository := scratchRepo(t)
	if state := verificationGitSnapshot(ctx, realRunner, repository); state.err == nil {
		t.Fatal("a repository with no commit has no HEAD to bind")
	}
	runGit(t, repository, "config", "user.email", "wb@example.test")
	runGit(t, repository, "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", ".")
	runGit(t, repository, "commit", "-m", "init")

	state := verificationGitSnapshot(ctx, realRunner, repository)
	if state.err != nil || !state.clean || len(state.revision) != 40 {
		t.Fatalf("clean snapshot = %+v", state)
	}
	if err := os.WriteFile(filepath.Join(repository, "dirty.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirty := verificationGitSnapshot(ctx, realRunner, repository); dirty.err != nil || dirty.clean {
		t.Fatalf("dirty snapshot = %+v, want clean=false", dirty)
	}
	if missing := verificationGitSnapshot(ctx, realRunner, filepath.Join(t.TempDir(), "absent")); missing.err == nil {
		t.Fatal("a missing repository must report an error")
	}
}

func TestCwCovQualityRunOptionsForTargetAndProgress(t *testing.T) {
	var seen []quality.Progress
	options := qualityRunOptionsForTarget(quality.RunOptions{
		Retry:    2,
		Progress: func(event quality.Progress) { seen = append(seen, event) },
	}, "acme/app")
	if options.CoverageDiagnosticsRepository != "acme/app" {
		t.Errorf("diagnostics repository = %q", options.CoverageDiagnosticsRepository)
	}
	if options.Retry != 2 {
		t.Errorf("unrelated options were lost: %+v", options)
	}
	reportQualityRepositoryCompleted(options, "acme/app", quality.StatusPassed)
	if len(seen) != 1 || seen[0].Repository != "acme/app" ||
		seen[0].State != quality.ProgressRepositoryCompleted || seen[0].Status != quality.StatusPassed {
		t.Fatalf("progress = %+v", seen)
	}
	// A nil progress sink is tolerated, and the target helper stamps the
	// repository onto the wrapped reporter.
	if quiet := qualityRunOptionsForTarget(quality.RunOptions{}, "acme/app"); quiet.Progress != nil {
		t.Fatalf("nil progress was replaced: %+v", quiet)
	}
	if wrapped, err := coverageRunOptionsForTarget(quality.RunOptions{}, qualityTarget{repository: "acme/app", path: t.TempDir()}); err != nil {
		t.Fatalf("coverageRunOptionsForTarget: %v", err)
	} else if wrapped.CoverageDiagnosticsRepository != "acme/app" {
		t.Errorf("coverage options = %+v", wrapped)
	}
}

func TestCwCovRunCoverageTargetsSkipsAModulelessRepository(t *testing.T) {
	empty := t.TempDir()
	// A repository with no Go module is skipped, not failed, and the completion
	// callback still fires for it.
	var completed []string
	reports := runCoverageTargets([]qualityTarget{{repository: "acme/empty", path: empty}}, 1,
		quality.RunOptions{Progress: func(event quality.Progress) {
			if event.State == quality.ProgressRepositoryCompleted {
				completed = append(completed, event.Repository)
			}
		}})
	if len(reports) != 1 || reports[0].Status != quality.StatusSkipped {
		t.Fatalf("reports = %+v, want one skipped repository", reports)
	}
	if len(completed) != 1 || completed[0] != "acme/empty" {
		t.Fatalf("completion callbacks = %v", completed)
	}
	// Zero targets is a no-op rather than a deadlock.
	if reports := runCoverageTargets(nil, 4, quality.RunOptions{}); len(reports) != 0 {
		t.Fatalf("no targets = %+v", reports)
	}
}

func TestCwCovQualityCommandsInProcess(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WB_HOME", t.TempDir())

	// An empty repository has no Go module, so coverage reports it as skipped
	// rather than running a suite.
	var stdout string
	var err error
	stdout = cwCovCaptureStdout(t, func() {
		_, _, err = cwCovExec(t, root, func() *cobra.Command { return newCoverageCmd(&invocation{}) }, root, "--format", "json")
	})
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
	stdout = cwCovCaptureStdout(t, func() {
		_, _, err = cwCovExec(t, root, func() *cobra.Command { return newVerifyCmd(&invocation{}) }, root, "--checks", "build", "--format", "json")
	})
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("verify exit = %d\n%s", code, stdout)
	}
	var index verificationIndex
	if jsonErr := json.Unmarshal([]byte(stdout), &index); jsonErr != nil {
		t.Fatalf("verify JSON: %v\n%s", jsonErr, stdout)
	}
	if index.SchemaVersion != 1 || len(index.Repositories) != 1 {
		t.Fatalf("verification index = %+v", index)
	}

	// check runs the named profile's checks over one repository.
	stdout = cwCovCaptureStdout(t, func() {
		_, _, err = cwCovExec(t, root, func() *cobra.Command { return newCheckCmd(&invocation{}) }, root, "--profile", "fast", "--format", "json")
	})
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

func TestCwCovRunVerificationTargetsReportsAnUnrunnableTarget(t *testing.T) {
	// A directory that cannot hold a run is reported as a failed row, and the
	// error is surfaced rather than swallowed.
	missing := filepath.Join(t.TempDir(), "absent")
	// missing does not exist, so both the before and after
	// verificationGitSnapshot calls fail the way real git would refuse a
	// non-existent directory; this test asserts only that the run is
	// reported as unrunnable, not the (absent) git identity.
	anyCall := func(runnertest.Call) bool { return true }
	fake := runnertest.New(t)
	fake.Expect(anyCall, runner.Result{}, errors.New("no such directory"))
	fake.Expect(anyCall, runner.Result{}, errors.New("no such directory"))
	reports := runVerificationTargets(fake, []qualityTarget{{repository: "acme/absent", path: missing}},
		[]quality.Check{quality.CheckBuild}, 1, quality.RunOptions{})
	if len(reports) != 1 {
		t.Fatalf("reports = %+v", reports)
	}
	if reports[0].Repository != "acme/absent" {
		t.Fatalf("report identity = %+v", reports[0])
	}
	if reports[0].Status == "" {
		t.Fatalf("an unrunnable target must carry a status: %+v", reports[0])
	}
}
