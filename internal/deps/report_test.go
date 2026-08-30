package deps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/orchestrate"
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

func TestRepositoryReportEmitsExactDependencyDeltaEvidence(t *testing.T) {
	result := orchestrate.Result[[]Decision]{
		Repository: "acme/app", PR: "https://github.com/acme/app/pull/17", Commit: strings.Repeat("c", 40),
		Metadata: []Decision{{
			Dependency: "nx", Ecosystem: EcosystemNPM, File: "package.json", Selector: "dependencies.nx",
			BeforeRef: "22.6.4", TargetVersion: "22.7.7", AfterRef: "22.7.7", AfterVersion: "22.7.7", Action: "updated",
		}, {Dependency: "nx", File: "package-lock.json", TargetVersion: "22.7.7", Action: "lockfile_regenerated"}},
	}
	report := repositoryReportFromResult(result)
	if len(report.DependencyDeltas) != 1 {
		t.Fatalf("dependency deltas = %+v, want one direct reference", report.DependencyDeltas)
	}
	delta := report.DependencyDeltas[0]
	if delta.SourcePR != result.PR || delta.SourceHead != result.Commit || delta.Package != "nx" || delta.Selector != "dependencies.nx" || delta.Lockfile != "package-lock.json" || delta.LockfileSelector != "packages|node_modules/nx|version" || delta.LockfileVersion != "22.7.7" || delta.Reviewed {
		t.Fatalf("dependency delta = %+v", delta)
	}
	markdown := (Report{Repositories: []RepositoryReport{report}}).Markdown()
	for _, expected := range []string{"Exact dependency PR deltas", "dependencies.nx", result.PR, result.Commit} {
		if !strings.Contains(markdown, expected) {
			t.Fatalf("report Markdown missing %q:\n%s", expected, markdown)
		}
	}
}
