package qualityrun

import (
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/reposelection"
	"gopkg.in/yaml.v3"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoverageRunOptionsUseRepositoryQualityPolicy(t *testing.T) {
	repository := t.TempDir()
	policyPath := filepath.Join(repository, ".wb", "quality.yaml")
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, []byte("version: 1\ngo_test:\n  shards: 8\n  packages: [./cmd/wb, ./internal/orchestrate, ./internal/worktrees]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	options, err := realBatchOperations().runOptionsForTarget(quality.RunOptions{
		GoTestShards: 8, GoShardPackages: []string{"./internal/worktrees"},
	}, reposelection.Target{Repository: "sneat-dev/wb", Path: repository})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(options.GoShardPackages, ","), "./cmd/wb,./internal/orchestrate,./internal/worktrees"; got != want {
		t.Fatalf("coverage shard packages = %q, want repository policy %q", got, want)
	}
}
func TestRunCoverageTargetsReportsPolicyFailure(t *testing.T) {
	repository := t.TempDir()
	policyPath := filepath.Join(repository, ".wb", "quality.yaml")
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, []byte("version: 1\ngo_test:\n  shards: 1\n  packages: [./internal/worktrees]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var completed []string
	reports := realBatchOperations().runCoverageTargets([]reposelection.Target{{Repository: "acme/broken", Path: repository}}, 1, quality.RunOptions{
		Progress: func(event quality.Progress) {
			if event.State == quality.ProgressRepositoryCompleted {
				completed = append(completed, event.Repository)
			}
		},
	})
	if len(reports) != 1 || reports[0].Status != quality.StatusFailed || !strings.Contains(reports[0].Error, "go_test.shards") {
		t.Fatalf("policy failure reports = %+v", reports)
	}
	if len(completed) != 1 || completed[0] != "acme/broken" {
		t.Fatalf("completion callbacks = %v", completed)
	}
}
func TestVerificationUsesRepositoryQualityPolicy(t *testing.T) {
	repository := t.TempDir()
	policyPath := filepath.Join(repository, ".wb", "quality.yaml")
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, []byte("version: 1\nunknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reports := realBatchOperations().runVerificationTargets([]reposelection.Target{{Repository: "acme/repo", Path: repository}}, []quality.Check{quality.CheckLint}, 1, quality.RunOptions{})
	if len(reports) != 1 || reports[0].Status != quality.StatusFailed || len(reports[0].Results) != 1 || !strings.Contains(reports[0].Results[0].Detail, "field unknown not found") {
		t.Fatalf("verification policy failure = %#v", reports)
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

	reports := realBatchOperations().runVerificationTargets([]reposelection.Target{{Repository: "acme/repo", Path: repository}}, nil, 1, quality.RunOptions{})
	if len(reports) != 1 || reports[0].Revision != wantRevision || !reports[0].WorkspaceClean {
		t.Fatalf("clean exact verification identity = %#v", reports)
	}

	if err := os.WriteFile(filepath.Join(repository, "dirty.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reports = realBatchOperations().runVerificationTargets([]reposelection.Target{{Repository: "acme/repo", Path: repository}}, nil, 1, quality.RunOptions{})
	if reports[0].Revision != "" || reports[0].WorkspaceClean {
		t.Fatalf("dirty workspace received exact verification identity = %#v", reports[0])
	}
}
func TestResumeTargetsSelectsOnlyPriorFailures(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "verify.yaml"), []byte("schema_version: 1\nrepositories:\n  - repository: acme/failing\n    status: failed\n  - repository: acme/passing\n    status: passed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	targets := []reposelection.Target{{Repository: "acme/failing"}, {Repository: "acme/passing"}}
	resumed, previous, err := resumeVerificationTargets(targets, dir, "verify")
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 1 || resumed[0].Repository != "acme/failing" {
		t.Fatalf("resumed targets = %+v", resumed)
	}
	merged := mergeVerificationReports(previous, VerificationIndex{SchemaVersion: 1, Repositories: []quality.VerificationReport{{Repository: "acme/failing", Status: quality.StatusPassed}}})
	if len(merged.Repositories) != 2 || merged.Repositories[0].Repository != "acme/failing" || merged.Repositories[0].Status != quality.StatusPassed {
		t.Fatalf("merged verification = %+v", merged)
	}

}
func TestCwCovVerificationGitSnapshot(t *testing.T) {
	repository := scratchRepo(t)
	if state := verificationGitSnapshot(repository); state.Err == nil {
		t.Fatal("a repository with no commit has no HEAD to bind")
	}
	runGit(t, repository, "config", "user.email", "wb@example.test")
	runGit(t, repository, "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", ".")
	runGit(t, repository, "commit", "-m", "init")

	state := verificationGitSnapshot(repository)
	if state.Err != nil || !state.Clean || len(state.Revision) != 40 {
		t.Fatalf("clean snapshot = %+v", state)
	}
	if err := os.WriteFile(filepath.Join(repository, "dirty.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirty := verificationGitSnapshot(repository); dirty.Err != nil || dirty.Clean {
		t.Fatalf("dirty snapshot = %+v, want clean=false", dirty)
	}
	if missing := verificationGitSnapshot(filepath.Join(t.TempDir(), "absent")); missing.Err == nil {
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
	if wrapped, err := realBatchOperations().runOptionsForTarget(quality.RunOptions{}, reposelection.Target{Repository: "acme/app", Path: t.TempDir()}); err != nil {
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
	reports := realBatchOperations().runCoverageTargets([]reposelection.Target{{Repository: "acme/empty", Path: empty}}, 1,
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
	if reports := realBatchOperations().runCoverageTargets(nil, 4, quality.RunOptions{}); len(reports) != 0 {
		t.Fatalf("no targets = %+v", reports)
	}
}
func TestCwCovRunVerificationTargetsReportsAnUnrunnableTarget(t *testing.T) {
	// A directory that cannot hold a run is reported as a failed row, and the
	// error is surfaced rather than swallowed.
	missing := filepath.Join(t.TempDir(), "absent")
	reports := realBatchOperations().runVerificationTargets([]reposelection.Target{{Repository: "acme/absent", Path: missing}},
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
func TestCwCovResumeTargetsAndMergeCoverageReports(t *testing.T) {
	targets := []reposelection.Target{{Repository: "acme/clean", Path: "/clean"}, {Repository: "acme/failed", Path: "/failed"}}
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
	if len(resumed) != 1 || resumed[0].Repository != "acme/failed" {
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
func cwCovVerificationFixture() VerificationIndex {
	return VerificationIndex{
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
func TestExplicitShardingWinsPolicyWithoutDroppingLint(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".wb", "quality.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("version: 1\ngo_lint:\n  commands:\n    - [go, vet, ./...]\ngo_test:\n  shards: 8\n  packages: [./internal/worktrees, ./internal/orchestrate]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := realBatchOperations().runOptionsForTarget(quality.RunOptions{GoTestShards: 4, GoShardPackages: []string{"./internal/worktrees"}, GoTestPackages: []string{"./internal/worktrees"}, ExplicitGoTestSharding: true}, reposelection.Target{Repository: "fixture", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if got.GoTestShards != 4 || strings.Join(got.GoShardPackages, ",") != "./internal/worktrees" || strings.Join(got.GoTestPackages, ",") != "./internal/worktrees" || len(got.GoLintCommands) != 1 || strings.Join(got.GoLintCommands[0], " ") != "go vet ./..." {
		t.Fatalf("policy options=%+v", got)
	}
}
