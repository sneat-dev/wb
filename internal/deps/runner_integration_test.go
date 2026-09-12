package deps

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestRunUsesIsolatedWorktreeWhenCanonicalCloneIsDirty(t *testing.T) {
	fixture := t.TempDir()
	// Scope WB_HOME to this fixture's own root. Without this, a fresh temp
	// githubDir has no legacy .wb, so wbhome.Root falls through to the real
	// ~/.wb; a hermetic test must not write there.
	t.Setenv(wbhome.EnvOverride, filepath.Join(fixture, ".wb"))
	seed := filepath.Join(fixture, "seed")
	remote := filepath.Join(fixture, "remote.git")
	githubDir := filepath.Join(fixture, "projects")
	canonical := filepath.Join(githubDir, "acme", "app")
	writeTestFile(t, filepath.Join(seed, ".github", "workflows", "ci.yml"), "jobs:\n  test:\n    uses: acme/cicd/.github/workflows/go.yml@"+strings.Repeat("1", 40)+" # v1.0.0\n")
	runTestGit(t, seed, "init", "-b", "main")
	runTestGit(t, seed, "config", "user.name", "WB Test")
	runTestGit(t, seed, "config", "user.email", "wb@example.test")
	runTestGit(t, seed, "add", "-A")
	runTestGit(t, seed, "commit", "-m", "initial")
	runTestGit(t, fixture, "clone", "--bare", seed, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, fixture, "clone", remote, canonical)
	dirtyPath := filepath.Join(canonical, "local-notes.txt")
	writeTestFile(t, dirtyPath, "unfinished\n")

	target := Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0"}
	resolved := strings.Repeat("2", 40)
	report, err := Run(context.Background(), target, []Repository{{Slug: "acme/app", Path: canonical, CloneURL: remote}}, Options{
		GitHubDir: githubDir, Ref: "main", Parallel: 1, Verify: false, Timeout: time.Minute,
		ResolveGitHubRef: func(context.Context, string, string) (string, error) { return resolved, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Repositories) != 1 || report.Repositories[0].Status != "changed" {
		t.Fatalf("report = %+v", report)
	}
	canonicalWorkflow, err := os.ReadFile(filepath.Join(canonical, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(canonicalWorkflow), resolved) {
		t.Fatal("canonical workflow was modified")
	}
	if contents, err := os.ReadFile(dirtyPath); err != nil || string(contents) != "unfinished\n" {
		t.Fatalf("dirty canonical file changed: contents=%q err=%v", contents, err)
	}
	worktreeWorkflow, err := os.ReadFile(filepath.Join(report.Repositories[0].WorktreeDir, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(worktreeWorkflow), resolved+" # v1.1.0") {
		t.Fatalf("worktree workflow was not updated:\n%s", worktreeWorkflow)
	}
}

func TestRunPlansManagedWorktreeFromItsLiveManifest(t *testing.T) {
	oldRef := strings.Repeat("1", 40)
	exactRef := strings.Repeat("2", 40)
	for _, test := range []struct {
		name             string
		worktreeWorkflow string
		stage            bool
		dryRun           bool
		wantStatus       string
		wantDecision     string
	}{
		{
			name:             "dependency introduced only in staged worktree",
			worktreeWorkflow: "jobs:\n  test:\n    uses: acme/cicd/.github/workflows/go.yml@" + oldRef + " # v1.0.0\n",
			stage:            true,
			wantStatus:       "changed",
			wantDecision:     "updated",
		},
		{
			name:             "worktree already exact while canonical is old",
			worktreeWorkflow: "jobs:\n  test:\n    uses: acme/cicd/.github/workflows/go.yml@" + exactRef + " # v1.1.0\n",
			dryRun:           true,
			wantStatus:       "skipped",
			wantDecision:     "unchanged",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newManagedGitHubActionsFixture(t, "jobs:\n")
			workflow := filepath.Join(fixture.worktree, ".github", "workflows", "ci.yml")
			writeTestFile(t, workflow, test.worktreeWorkflow)
			if test.stage {
				runTestGit(t, fixture.worktree, "add", ".github/workflows/ci.yml")
			}
			writeTestFile(t, filepath.Join(fixture.worktree, "unrelated-untracked.txt"), "keep out of dependency report\n")

			report, err := Run(context.Background(), Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0"}, []Repository{{Slug: "acme/app", Path: fixture.worktree, CloneURL: fixture.remote}}, Options{
				GitHubDir: fixture.githubDir, Ref: "main", DryRun: test.dryRun, Timeout: time.Minute,
				ResolveGitHubRef: func(context.Context, string, string) (string, error) { return exactRef, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Repositories) != 1 || report.Repositories[0].Status != test.wantStatus {
				t.Fatalf("report = %+v", report)
			}
			decisions := report.Repositories[0].Decisions
			if len(decisions) != 1 || decisions[0].Action != test.wantDecision || decisions[0].File != ".github/workflows/ci.yml" {
				t.Fatalf("decisions = %+v", decisions)
			}
			if strings.Contains(strings.Join(report.Repositories[0].ChangedFiles, ","), "unrelated-untracked.txt") {
				t.Fatalf("untracked input leaked into dependency report: %v", report.Repositories[0].ChangedFiles)
			}
		})
	}
}

type managedGitHubActionsFixture struct {
	githubDir string
	remote    string
	worktree  string
}

func newManagedGitHubActionsFixture(t *testing.T, canonicalWorkflow string) managedGitHubActionsFixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv(wbhome.EnvOverride, filepath.Join(root, ".wb"))
	seed := filepath.Join(root, "seed")
	remote := filepath.Join(root, "remote.git")
	githubDir := filepath.Join(root, "projects")
	canonical := filepath.Join(githubDir, "acme", "app")
	writeTestFile(t, filepath.Join(seed, ".github", "workflows", "ci.yml"), canonicalWorkflow)
	runTestGit(t, seed, "init", "-b", "main")
	runTestGit(t, seed, "config", "user.name", "WB Test")
	runTestGit(t, seed, "config", "user.email", "wb@example.test")
	runTestGit(t, seed, "add", "-A")
	runTestGit(t, seed, "commit", "-m", "initial")
	runTestGit(t, root, "clone", "--bare", seed, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "clone", remote, canonical)
	created, err := worktrees.Create(context.Background(), []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: githubDir, Operation: "managed-manifest-input", Branch: "feature/managed-manifest-input", BranchChosen: true,
		WorkLog: worktrees.WorkLogOptions{Model: "test"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create managed worktree: entries=%+v err=%v", created, err)
	}
	return managedGitHubActionsFixture{githubDir: githubDir, remote: remote, worktree: created[0].WorktreeDir}
}

func TestNormalizeOptionsMakesPublicationFlagsCumulative(t *testing.T) {
	t.Parallel()
	options, _, err := normalizeOptions(Options{GitHubDir: t.TempDir(), Merge: true}, "deps-set-test")
	if err != nil {
		t.Fatal(err)
	}
	if !options.Merge || !options.PR || !options.Push || !options.Commit {
		t.Fatalf("normalized options = %+v", options)
	}
}

func TestNormalizeOptionsKeepsFastValidationBoundToPublication(t *testing.T) {
	t.Parallel()
	options, lifecycle, err := normalizeOptions(Options{
		GitHubDir: t.TempDir(), ValidationMode: ValidationModeFast, Merge: true,
	}, "deps-set-fast")
	if err != nil {
		t.Fatal(err)
	}
	if options.ValidationMode != ValidationModeFast || options.Verify || lifecycle.Verify || !lifecycle.Merge {
		t.Fatalf("normalized fast options = %+v lifecycle=%+v", options, lifecycle)
	}
	prOptions, prLifecycle, err := normalizeOptions(Options{
		GitHubDir: t.TempDir(), ValidationMode: ValidationModeFast, PR: true,
	}, "deps-set-fast-pr")
	if err != nil {
		t.Fatal(err)
	}
	if prOptions.ValidationMode != ValidationModeFast || prOptions.Verify || prLifecycle.Verify || prLifecycle.Merge || !prLifecycle.PR || !prLifecycle.WaitForPRChecks {
		t.Fatalf("normalized fast PR options = %+v lifecycle=%+v", prOptions, prLifecycle)
	}
	if _, _, err := normalizeOptions(Options{
		GitHubDir: t.TempDir(), ValidationMode: ValidationModeFast,
	}, "deps-set-unsafe-fast"); err == nil || !strings.Contains(err.Error(), "requires --pr or --merge") {
		t.Fatalf("fast validation without publication error = %v", err)
	}
}

func TestDependencyPullRequestBodiesReportValidationAuthorityTruthfully(t *testing.T) {
	t.Parallel()
	target := Target{Ecosystem: EcosystemNPM, Dependency: "@acme/lib", Version: "1.2.3"}
	_, fastSet := exactSetHandler{target: target, options: Options{ValidationMode: ValidationModeFast}}.PullRequest(Repository{})
	if !strings.Contains(fastSet, "exact PR-head GitHub checks") || strings.Contains(fastSet, "local lint") {
		t.Fatalf("fast deps set PR body = %q", fastSet)
	}
	_, fastBump := (waveHandler{ecosystem: EcosystemNPM, options: Options{ValidationMode: ValidationModeFast}, targetsByRepository: map[string][]Target{
		"acme/app": {target},
	}}).PullRequest(Repository{Slug: "acme/app"})
	if !strings.Contains(fastBump, "exact PR-head GitHub checks") || strings.Contains(fastBump, "full local verification") {
		t.Fatalf("fast deps bump PR body = %q", fastBump)
	}
	_, fullBump := (waveHandler{ecosystem: EcosystemNPM, options: Options{ValidationMode: ValidationModeFull}, targetsByRepository: map[string][]Target{
		"acme/app": {target},
	}}).PullRequest(Repository{Slug: "acme/app"})
	if !strings.Contains(fullBump, "full local verification completed") {
		t.Fatalf("full deps bump PR body = %q", fullBump)
	}
}

func TestDryRunDoesNotCreateOperationWorktreeRoot(t *testing.T) {
	fixture := t.TempDir()
	// Scope WB_HOME to this fixture's own root. Without this, a fresh temp
	// githubDir has no legacy .wb, so wbhome.Root falls through to the real
	// ~/.wb; a hermetic test must not write there.
	t.Setenv(wbhome.EnvOverride, filepath.Join(fixture, ".wb"))
	seed := filepath.Join(fixture, "seed")
	remote := filepath.Join(fixture, "remote.git")
	githubDir := filepath.Join(fixture, "projects")
	canonical := filepath.Join(githubDir, "acme", "app")
	writeTestFile(t, filepath.Join(seed, ".github", "workflows", "ci.yml"), "uses: acme/cicd@v1.0.0\n")
	runTestGit(t, seed, "init", "-b", "main")
	runTestGit(t, seed, "config", "user.name", "WB Test")
	runTestGit(t, seed, "config", "user.email", "wb@example.test")
	runTestGit(t, seed, "add", "-A")
	runTestGit(t, seed, "commit", "-m", "initial")
	runTestGit(t, fixture, "clone", "--bare", seed, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, fixture, "clone", remote, canonical)
	target := Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0"}
	report, err := Run(context.Background(), target, []Repository{{Slug: "acme/app", Path: canonical}}, Options{
		GitHubDir: githubDir, Ref: "main", DryRun: true, Timeout: time.Minute,
		ResolveGitHubRef: func(context.Context, string, string) (string, error) { return strings.Repeat("3", 40), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Repositories[0].Status != "planned" {
		t.Fatalf("status = %s", report.Repositories[0].Status)
	}
	if _, err := os.Stat(filepath.Join(githubDir, ".wb")); !os.IsNotExist(err) {
		t.Fatalf("dry run created .wb state: %v", err)
	}
}

func TestRunCommitsVerifiedOperationWithoutPushing(t *testing.T) {
	fixture := t.TempDir()
	// Scope WB_HOME to this fixture's own root. Without this, a fresh temp
	// githubDir has no legacy .wb, so wbhome.Root falls through to the real
	// ~/.wb; a hermetic test must not write there.
	t.Setenv(wbhome.EnvOverride, filepath.Join(fixture, ".wb"))
	seed := filepath.Join(fixture, "seed")
	remote := filepath.Join(fixture, "remote.git")
	githubDir := filepath.Join(fixture, "projects")
	canonical := filepath.Join(githubDir, "acme", "app")
	writeTestFile(t, filepath.Join(seed, ".github", "workflows", "ci.yml"), "uses: acme/cicd/action@v1.0.0\n")
	runTestGit(t, seed, "init", "-b", "main")
	runTestGit(t, seed, "config", "user.name", "WB Test")
	runTestGit(t, seed, "config", "user.email", "wb@example.test")
	runTestGit(t, seed, "add", "-A")
	runTestGit(t, seed, "commit", "-m", "initial")
	runTestGit(t, fixture, "clone", "--bare", seed, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, fixture, "clone", remote, canonical)
	runTestGit(t, canonical, "config", "user.name", "WB Test")
	runTestGit(t, canonical, "config", "user.email", "wb@example.test")
	target := Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0"}
	report, err := Run(context.Background(), target, []Repository{{Slug: "acme/app", Path: canonical}}, Options{
		GitHubDir: githubDir, Ref: "main", Commit: true, Timeout: time.Minute,
		ResolveGitHubRef: func(context.Context, string, string) (string, error) { return strings.Repeat("4", 40), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := report.Repositories[0]
	if repository.Status != "committed" || repository.Commit == "" || repository.Pushed {
		t.Fatalf("repository report = %+v", repository)
	}
	message := strings.TrimSpace(runTestGit(t, repository.WorktreeDir, "log", "-1", "--format=%s"))
	if message != "chore(deps): set acme/cicd to v1.1.0" {
		t.Fatalf("commit message = %q", message)
	}
	remoteHead := strings.TrimSpace(runTestGit(t, repository.WorktreeDir, "rev-parse", "origin/main"))
	if remoteHead == repository.Commit {
		t.Fatal("local-only commit was unexpectedly pushed")
	}
}

func TestRunSkipsArchivedRepositoryWithoutCloning(t *testing.T) {
	t.Parallel()
	githubDir := t.TempDir()
	target := Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0"}
	report, err := Run(context.Background(), target, []Repository{{Slug: "acme/retired", Archived: true}}, Options{
		GitHubDir: githubDir, DryRun: true,
		ResolveGitHubRef: func(context.Context, string, string) (string, error) { return strings.Repeat("5", 40), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Repositories[0].Status != "skipped" || report.Repositories[0].Reason != "repository is archived" {
		t.Fatalf("repository report = %+v", report.Repositories[0])
	}
	if _, err := os.Stat(filepath.Join(githubDir, "acme", "retired")); !os.IsNotExist(err) {
		t.Fatalf("archived repository was cloned: %v", err)
	}
}

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
