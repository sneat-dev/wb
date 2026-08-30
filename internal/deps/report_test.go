package deps

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/worktrees"
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

func TestProductionReportDeltasPassFullValidatorForSupportedLockfiles(t *testing.T) {
	for _, fixture := range []struct {
		name             string
		manifest         string
		baseManifest     string
		sourceManifest   string
		targetManifest   string
		lockfile         string
		lockfileContents string
		packageName      string
		ecosystem        Ecosystem
		before           string
		requested        string
	}{
		{name: "package-lock", manifest: "package.json", baseManifest: `{"dependencies":{"nx":"22.6.4"}}` + "\n", sourceManifest: `{"dependencies":{"nx":"22.7.7"}}` + "\n", targetManifest: `{"dependencies":{"nx":"22.7.7"}}` + "\n", lockfile: "package-lock.json", lockfileContents: `{"packages":{"node_modules/nx":{"version":"22.7.7"}}}` + "\n", packageName: "nx", ecosystem: EcosystemNPM, before: "22.6.4", requested: "22.7.7"},
		{name: "pnpm-lock", manifest: "package.json", baseManifest: `{"dependencies":{"nx":"22.6.4"}}` + "\n", sourceManifest: `{"dependencies":{"nx":"22.7.7"}}` + "\n", targetManifest: `{"dependencies":{"nx":"22.7.7"}}` + "\n", lockfile: "pnpm-lock.yaml", lockfileContents: "snapshots:\n  /nx@22.7.7:\n    version: 22.7.7\n", packageName: "nx", ecosystem: EcosystemNPM, before: "22.6.4", requested: "22.7.7"},
		{name: "go-sum", manifest: "go.mod", baseManifest: "module example.com/app\n\nrequire example.com/mod v1.2.2\n", sourceManifest: "module example.com/app\n\nrequire example.com/mod v1.2.3\n", targetManifest: "module example.com/app\n\nrequire example.com/mod v1.2.3\n", lockfile: "go.sum", lockfileContents: "example.com/mod v1.2.3 h1:checksum\n", packageName: "example.com/mod", ecosystem: EcosystemGo, before: "v1.2.2", requested: "v1.2.3"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			canonical, source, targetHead, sourceHead := dependencyReportGitFixture(t, fixture.manifest, fixture.baseManifest, fixture.sourceManifest, fixture.targetManifest, fixture.lockfile, fixture.lockfileContents)
			pr := "https://github.com/acme/app/pull/17"
			result := orchestrate.Result[[]Decision]{
				Repository: "acme/app", PR: pr, Commit: sourceHead,
				Metadata: []Decision{
					{Dependency: fixture.packageName, Ecosystem: fixture.ecosystem, File: fixture.manifest, Selector: selectorForReport(fixture.ecosystem, fixture.packageName), BeforeRef: fixture.before, TargetVersion: fixture.requested, AfterRef: fixture.requested, AfterVersion: fixture.requested},
					{Dependency: fixture.packageName, File: fixture.lockfile, TargetVersion: fixture.requested, Action: "lockfile_regenerated"},
				},
			}
			report := repositoryReportFromResult(result)
			if len(report.DependencyDeltas) != 1 {
				t.Fatalf("production report deltas = %+v", report.DependencyDeltas)
			}
			delta := report.DependencyDeltas[0]
			receipt := worktrees.SupersessionReceipt{
				OriginalPR: pr, OriginalPRNumber: 17, OriginalPRRepository: "acme/app", OriginalPRHead: sourceHead, OriginalHead: sourceHead, Target: "main", TargetHead: targetHead,
				DependencyDeltasComplete: true,
				DependencyDeltas: []worktrees.SupersessionDependencyDelta{{
					SourcePR: delta.SourcePR, SourceHead: delta.SourceHead, Consumer: delta.Consumer, Ecosystem: string(delta.Ecosystem), Package: delta.Package, Manifest: delta.Manifest, Selector: delta.Selector,
					Before: delta.Before, RequestedAfter: delta.RequestedAfter, CandidateAfter: delta.CandidateAfter, Lockfile: delta.Lockfile, LockfileSelector: delta.LockfileSelector, LockfileVersion: delta.LockfileVersion, Reviewed: true,
				}},
			}
			entry := worktrees.ListResult{Repository: "acme/app", CanonicalDir: canonical, WorktreeDir: source, HeadSHA: sourceHead, RemoteTargetSHA: targetHead,
				OpenPullRequest: &worktrees.PullRequest{Number: 17, URL: pr, Repository: "acme/app", HeadSHA: sourceHead}}
			if err := worktrees.ValidateDependencyDeltas(context.Background(), receipt, entry); err != nil {
				t.Fatalf("production %s report receipt rejected: %v\nreport delta: %+v", fixture.name, err, delta)
			}
		})
	}
}

func selectorForReport(ecosystem Ecosystem, packageName string) string {
	if ecosystem == EcosystemGo {
		return "require:" + packageName
	}
	return "dependencies." + packageName
}

func dependencyReportGitFixture(t *testing.T, manifest, baseManifest, sourceManifest, targetManifest, lockfile, lockfileContents string) (canonical, source, targetHead, sourceHead string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	canonical = filepath.Join(root, "canonical")
	source = filepath.Join(root, "source")
	runDependencyGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	runDependencyGit(t, root, "clone", remote, canonical)
	runDependencyGit(t, canonical, "config", "user.email", "test@example.test")
	runDependencyGit(t, canonical, "config", "user.name", "Test")
	writeDependencyFixtureFile(t, canonical, "README.md", "# app\n")
	runDependencyGit(t, canonical, "add", "README.md")
	runDependencyGit(t, canonical, "commit", "-m", "initial")
	runDependencyGit(t, canonical, "push", "-u", "origin", "main")
	writeDependencyFixtureFile(t, canonical, manifest, baseManifest)
	runDependencyGit(t, canonical, "add", manifest)
	runDependencyGit(t, canonical, "commit", "-m", "base dependency")
	runDependencyGit(t, canonical, "push", "origin", "main")
	runDependencyGit(t, canonical, "worktree", "add", "-b", "feature/dependency", source, "main")
	writeDependencyFixtureFile(t, source, manifest, sourceManifest)
	runDependencyGit(t, source, "add", manifest)
	runDependencyGit(t, source, "commit", "-m", "source dependency request")
	sourceHead = strings.TrimSpace(runDependencyGit(t, source, "rev-parse", "HEAD"))
	writeDependencyFixtureFile(t, canonical, manifest, targetManifest)
	writeDependencyFixtureFile(t, canonical, lockfile, lockfileContents)
	runDependencyGit(t, canonical, "add", manifest, lockfile)
	runDependencyGit(t, canonical, "commit", "-m", "integrated dependency")
	runDependencyGit(t, canonical, "push", "origin", "main")
	targetHead = strings.TrimSpace(runDependencyGit(t, canonical, "rev-parse", "HEAD"))
	return canonical, source, targetHead, sourceHead
}

func writeDependencyFixtureFile(t *testing.T, root, name, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runDependencyGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
