package main

import (
	"os"
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
	if _, err := qualityTargets("", root, "", qualityOptions{fleet: true, regex: "[", parallel: 1}); err == nil {
		t.Fatal("invalid regex should fail")
	}
	if _, err := qualityTargets("", root, "", qualityOptions{fleet: true, match: "[", parallel: 1}); err == nil {
		t.Fatal("invalid glob should fail")
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
	markdown := statusMarkdown(report, true)
	for _, want := range []string{"attention", "1 modified file", "main.go"} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("status markdown missing %q:\n%s", want, markdown)
		}
	}
}
