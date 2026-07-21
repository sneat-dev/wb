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
