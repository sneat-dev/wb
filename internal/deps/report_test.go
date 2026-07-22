package deps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestReportWritesLinkedMarkdownAndDeterministicYAML(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	report := Report{
		SchemaVersion: 1,
		Operation:     "deps-set-github-actions-acme-cicd-v1-2-3",
		Status:        "completed",
		Target: Target{
			Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd",
			Version: "v1.2.3", Resolved: strings.Repeat("a", 40),
		},
		BaseRef:  "main",
		Parallel: 2,
		Repositories: []RepositoryReport{{
			Repository: "acme/app", CanonicalDir: filepath.Join(worktree, "canonical"),
			WorktreeDir: worktree, Branch: "wb/deps/set", Ref: "main",
			Status: "merged", Reason: "all checks passed", ChangedFiles: []string{".github/workflows/ci.yml"},
			Decisions: []Decision{{
				File: ".github/workflows/ci.yml", BeforeRef: "main", TargetVersion: "v1.2.3",
				ResolvedRef: strings.Repeat("a", 40), AfterRef: strings.Repeat("a", 40),
				AfterVersion: "v1.2.3", Action: "updated", Reason: "exact target applied",
			}},
			Verifications: []quality.VerificationEntry{{Command: "go test ./...", Status: quality.StatusPassed}},
			Commit:        strings.Repeat("b", 40), Pushed: true, PR: "https://github.com/acme/app/pull/7",
			Checks: []RemoteCheck{{Name: "CI", Bucket: "pass", Link: "https://github.com/acme/app/actions/runs/1"}},
			Merged: true,
		}},
	}
	markdown := report.Markdown()
	for _, expected := range []string{"acme/cicd@v1.2.3", "[PR](https://github.com/acme/app/pull/7)", "Dependency decisions", "go test ./...", "GitHub checks"} {
		if !strings.Contains(markdown, expected) {
			t.Errorf("Markdown does not contain %q:\n%s", expected, markdown)
		}
	}
	directory := filepath.Join(t.TempDir(), "reports")
	if err := WriteReports(directory, report); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"deps-set.md", "deps-set.yaml"} {
		contents, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if len(contents) == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
	raw, err := report.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "schema_version: 1") || !strings.Contains(string(raw), "repository: acme/app") {
		t.Fatalf("unexpected YAML:\n%s", raw)
	}
}
