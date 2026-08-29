package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestQualityTargetsSupportsGlobAndRegex(t *testing.T) {
	root := t.TempDir()
	for _, repository := range []string{"sneat-co/bots", "sneat-co/core", "other/tools"} {
		path := filepath.Join(root, repository, ".git")
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	targets, err := qualityTargets("", root, "", qualityOptions{fleet: true, match: "sneat-co/*", regex: "(bots|core)$", parallel: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(targets), 2; got != want {
		t.Fatalf("targets = %v, want %d", targets, want)
	}
	filtered, err := qualityTargets("", root, "sneat-co/core", qualityOptions{fleet: true, parallel: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].repository != "sneat-co/core" {
		t.Fatalf("root --filter fleet targets = %#v, want only sneat-co/core", filtered)
	}
	if _, err := qualityTargets("", root, "", qualityOptions{fleet: true, regex: "[", parallel: 1}); err == nil {
		t.Fatal("invalid regex should fail")
	}
	if _, err := qualityTargets("", root, "", qualityOptions{fleet: true, match: "[", parallel: 1}); err == nil {
		t.Fatal("invalid glob should fail")
	}
}

func TestCoverageShardingFlagsFailClosedOnAmbiguousScope(t *testing.T) {
	for _, test := range []struct {
		name    string
		options qualityOptions
		want    string
	}{
		{name: "zero shards", options: qualityOptions{testShards: 0}, want: "at least 1"},
		{name: "shards without package", options: qualityOptions{testShards: 2}, want: "requires at least one"},
		{name: "package without shards", options: qualityOptions{testShards: 1, shardPackages: []string{"./internal/worktrees"}}, want: "greater than 1"},
		{name: "fleet package", options: qualityOptions{testShards: 2, shardPackages: []string{"./internal/worktrees"}, fleet: true}, want: "repository-specific"},
		{name: "fleet profile", options: qualityOptions{testShards: 1, fleet: true, coverageProfile: "profile.cov"}, want: "one fresh repository"},
		{name: "invalid minimum", options: qualityOptions{testShards: 1, minimumCoverage: 101}, want: "between 0 and 100"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateCoverageExecutionOptions(test.options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestQualityTargetsRejectsOwnerRepositorySelectorsForDirectPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, test := range []struct {
		name    string
		filter  string
		options qualityOptions
	}{
		{name: "root filter", filter: "acme/repo", options: qualityOptions{parallel: 1}},
		{name: "match", options: qualityOptions{parallel: 1, match: "acme/*"}},
		{name: "regex", options: qualityOptions{parallel: 1, regex: "^acme/"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := qualityTargets(root, t.TempDir(), test.filter, test.options); err == nil || !strings.Contains(err.Error(), "fleet mode") {
				t.Fatalf("direct owner/repository selector error = %v", err)
			}
		})
	}
}

func TestVerificationReportBindsOnlyAnUnchangedCleanGitRevision(t *testing.T) {
	repository := t.TempDir()
	git := func(arguments ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repository}, arguments...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "--initial-branch=main")
	git("config", "user.name", "WB Test")
	git("config", "user.email", "wb@example.test")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("clean\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("-c", "commit.gpgSign=false", "commit", "-m", "initial")
	wantRevision := git("rev-parse", "HEAD")

	reports := runVerificationTargets([]qualityTarget{{repository: "acme/repo", path: repository}}, nil, 1, quality.RunOptions{})
	if len(reports) != 1 || reports[0].Revision != wantRevision || !reports[0].WorkspaceClean {
		t.Fatalf("clean exact verification identity = %#v", reports)
	}

	if err := os.WriteFile(filepath.Join(repository, "dirty.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reports = runVerificationTargets([]qualityTarget{{repository: "acme/repo", path: repository}}, nil, 1, quality.RunOptions{})
	if reports[0].Revision != "" || reports[0].WorkspaceClean {
		t.Fatalf("dirty workspace received exact verification identity = %#v", reports[0])
	}
}

func TestQualityMarkdownIncludesTotalsAndCommands(t *testing.T) {
	coverage := quality.NewCoverageReport([]quality.RepositoryCoverage{{Repository: "acme/repo", Status: quality.StatusPassed, Statements: 4, Covered: 3, Percentage: 75}})
	if markdown := coverageMarkdown(coverage); !strings.Contains(markdown, "Fleet total:** 75.00%") {
		t.Fatalf("coverage markdown = %s", markdown)
	}
	verification := verificationIndex{Checks: []quality.Check{quality.CheckTest}, Repositories: []quality.VerificationReport{{Repository: "acme/repo", Status: quality.StatusPassed, Results: []quality.VerificationEntry{{Language: "go", Module: ".", Check: quality.CheckTest, Command: "go test ./...", Status: quality.StatusPassed}}}}}
	if markdown := verificationMarkdown(verification); !strings.Contains(markdown, "go test ./...") {
		t.Fatalf("verification markdown = %s", markdown)
	}
}

func TestCoverageSummaryRequiresDurableReport(t *testing.T) {
	report := quality.NewCoverageReport([]quality.RepositoryCoverage{{
		Repository: "acme/repo", Status: quality.StatusPassed, Statements: 4, Covered: 3, Percentage: 75,
	}})
	var output bytes.Buffer
	if err := writeCoverageOutputTo(&output, report, "summary", ""); err == nil {
		t.Fatal("summary without a report directory must fail rather than lose the durable reference")
	}
}

func TestCoverageSummaryReferencesBoundedDurableReport(t *testing.T) {
	for _, size := range []int{64<<10 - 1, 64<<10 + 1} {
		t.Run(fmt.Sprintf("detail-%d-bytes", size), func(t *testing.T) {
			// The persisted report may be larger than the session-message body;
			// the observer-facing summary must stay bounded at either side of
			// the transport limit.
			detail := strings.Repeat("x", size)
			report := quality.NewCoverageReport([]quality.RepositoryCoverage{{
				Repository: "acme/repo", Status: quality.StatusFailed, Error: detail,
			}})
			directory := t.TempDir()
			var output bytes.Buffer
			if err := writeCoverageOutputTo(&output, report, "summary", directory); err != nil {
				t.Fatalf("write summary: %v", err)
			}
			if output.Len() >= 64<<10 {
				t.Fatalf("summary length = %d, want below the session-message body limit", output.Len())
			}
			for _, expected := range []string{"WB coverage failed:", "report=" + filepath.Join(directory, "coverage.yaml"), "sha256="} {
				if !strings.Contains(output.String(), expected) {
					t.Fatalf("summary %q does not contain %q", output.String(), expected)
				}
			}
			raw, err := os.ReadFile(filepath.Join(directory, "coverage.yaml"))
			if err != nil {
				t.Fatalf("read durable report: %v", err)
			}
			if !strings.Contains(string(raw), detail) {
				t.Fatal("durable report lost the full failure detail")
			}
			digest := sha256.Sum256(raw)
			if !strings.Contains(output.String(), fmt.Sprintf("sha256=%x", digest)) {
				t.Fatalf("summary digest does not identify the durable report: %q", output.String())
			}
			gateErr, ok := coverageGateError(report, -1).(*exitError)
			if !ok || gateErr.code != exitFindings {
				t.Fatalf("coverage gate error = %v, want exitFindings", coverageGateError(report, -1))
			}
		})
	}
}

func TestCoverageSummaryPreservesRealFailureVerdict(t *testing.T) {
	report := quality.NewCoverageReport([]quality.RepositoryCoverage{{
		Repository: "acme/repo", Status: quality.StatusFailed, Error: "go test failed", Statements: 4,
	}})
	var output bytes.Buffer
	if err := writeCoverageOutputTo(&output, report, "summary", t.TempDir()); err != nil {
		t.Fatalf("write failed summary: %v", err)
	}
	if gateErr, ok := coverageGateError(report, -1).(*exitError); !ok || gateErr.code != exitFindings {
		t.Fatalf("coverage gate error = %v, want exitFindings", coverageGateError(report, -1))
	}
}

func TestCoverageSummaryKeepsFleetDiagnosticReferenceBounded(t *testing.T) {
	directory := t.TempDir()
	repositories := make([]quality.RepositoryCoverage, 2000)
	for index := range repositories {
		repositories[index] = quality.RepositoryCoverage{
			Repository: fmt.Sprintf("acme/repo-%04d", index),
			Status:     quality.StatusFailed,
			Diagnostic: &quality.CoverageDiagnostic{Manifest: filepath.Join(directory, fmt.Sprintf("manifest-%04d.yaml", index)), SHA256: fmt.Sprintf("%064d", index)},
		}
	}
	report := quality.NewCoverageReport(repositories)
	var output bytes.Buffer
	if err := writeCoverageOutputTo(&output, report, "summary", directory); err != nil {
		t.Fatalf("write fleet summary: %v", err)
	}
	if output.Len() >= 64<<10 {
		t.Fatalf("fleet summary length = %d, want below the session-message body limit", output.Len())
	}
	if !strings.Contains(output.String(), "diagnostics="+filepath.Join(directory, "coverage-diagnostics.yaml")) || !strings.Contains(output.String(), "diagnostics-sha256=") {
		t.Fatalf("fleet summary does not reference the aggregate diagnostic index: %q", output.String())
	}
	indexRaw, err := os.ReadFile(filepath.Join(directory, "coverage-diagnostics.yaml"))
	if err != nil {
		t.Fatalf("read diagnostic index: %v", err)
	}
	indexDigest := sha256.Sum256(indexRaw)
	if !strings.Contains(output.String(), fmt.Sprintf("diagnostics-sha256=%x", indexDigest)) {
		t.Fatalf("fleet summary has the wrong diagnostic index digest: %q", output.String())
	}
	indexInfo, err := os.Stat(filepath.Join(directory, "coverage-diagnostics.yaml"))
	if err != nil {
		t.Fatalf("stat diagnostic index: %v", err)
	}
	if indexInfo.Mode().Perm() != 0o600 {
		t.Fatalf("diagnostic index permissions = %o, want 600", indexInfo.Mode().Perm())
	}
}

func TestResumeTargetsSelectsOnlyPriorFailures(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "verify.yaml"), []byte("schema_version: 1\nrepositories:\n  - repository: acme/failing\n    status: failed\n  - repository: acme/passing\n    status: passed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	targets := []qualityTarget{{repository: "acme/failing"}, {repository: "acme/passing"}}
	resumed, previous, err := resumeVerificationTargets(targets, dir, "verify")
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 1 || resumed[0].repository != "acme/failing" {
		t.Fatalf("resumed targets = %+v", resumed)
	}
	merged := mergeVerificationReports(previous, verificationIndex{SchemaVersion: 1, Repositories: []quality.VerificationReport{{Repository: "acme/failing", Status: quality.StatusPassed}}})
	if len(merged.Repositories) != 2 || merged.Repositories[0].Repository != "acme/failing" || merged.Repositories[0].Status != quality.StatusPassed {
		t.Fatalf("merged verification = %+v", merged)
	}
	if _, err := checksForProfile("ci"); err != nil {
		t.Fatal(err)
	}
	if _, err := checksForProfile("unknown"); err == nil {
		t.Fatal("unknown profile should fail")
	}
}

func TestStatusMarkdownReportsAttention(t *testing.T) {
	report := statusIndex{Repositories: []repositoryStatusInfo{{Repository: "acme/repo", Status: "attention", Summary: "1 modified file", Modified: []string{"main.go"}}}}
	markdown := statusMarkdown(report, true, "# WB local repository status\n\n")
	for _, want := range []string{"attention", "1 modified file", "main.go"} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("status markdown missing %q:\n%s", want, markdown)
		}
	}
}
