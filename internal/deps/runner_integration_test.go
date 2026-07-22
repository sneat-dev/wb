package deps

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunUsesIsolatedWorktreeWhenCanonicalCloneIsDirty(t *testing.T) {
	fixture := t.TempDir()
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

func TestNormalizeOptionsMakesPublicationFlagsCumulative(t *testing.T) {
	t.Parallel()
	options, err := normalizeOptions(Options{GitHubDir: t.TempDir(), Merge: true})
	if err != nil {
		t.Fatal(err)
	}
	if !options.Merge || !options.PR || !options.Push || !options.Commit {
		t.Fatalf("normalized options = %+v", options)
	}
}

func TestDryRunDoesNotCreateOperationWorktreeRoot(t *testing.T) {
	fixture := t.TempDir()
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
