package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/testenv"
)

//nolint:paralleltest // calls runnertest.AllowRealProcess, which Go's testing package forbids combined with t.Parallel
func TestOrchCovEnsureCanonicalClonesAMissingRepository(t *testing.T) {
	runnertest.AllowRealProcess(t)
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	remote := filepath.Join(root, "remote.git")
	writeEngineFile(t, filepath.Join(seed, "file.txt"), "contents\n")
	runEngineGit(t, seed, "init", "-b", "main")
	runEngineGit(t, seed, "config", "user.name", "WB Test")
	runEngineGit(t, seed, "config", "user.email", "wb@example.test")
	runEngineGit(t, seed, "add", "-A")
	runEngineGit(t, seed, "commit", "-m", "initial")
	runEngineGit(t, root, "clone", "--bare", seed, remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)

	projectsRoot := filepath.Join(root, "projects")
	canonical := filepath.Join(projectsRoot, "acme", "cloned")
	resolved, err := EnsureCanonical(context.Background(),
		Repository{Slug: "acme/cloned", Path: canonical, CloneURL: remote}, canonical,
		Options{GitHubDir: projectsRoot, Ref: "main", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Ref != "main" || resolved.Fallback {
		t.Fatalf("resolved = %+v", resolved)
	}
	if contents := mustReadEngineFile(t, filepath.Join(canonical, "file.txt")); contents != "contents\n" {
		t.Fatalf("cloned file = %q", contents)
	}
}

//nolint:paralleltest // calls runnertest.AllowRealProcess, which Go's testing package forbids combined with t.Parallel
func TestOrchCovEnsureCanonicalReportsAnUnclonableRepository(t *testing.T) {
	runnertest.AllowRealProcess(t)
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	canonical := filepath.Join(projectsRoot, "acme", "missing")
	if _, err := EnsureCanonical(context.Background(),
		Repository{Slug: "acme/missing", Path: canonical, CloneURL: filepath.Join(root, "absent.git")}, canonical,
		Options{GitHubDir: projectsRoot, Ref: "main", Timeout: time.Minute}); err == nil {
		t.Fatal("a repository that could not be cloned was accepted")
	}
}

func TestOrchCovEnsureCanonicalReportsAFetchFailure(t *testing.T) {
	fixture := newEngineFixture(t)
	runEngineGit(t, fixture.canonical, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	if _, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical,
		Options{GitHubDir: fixture.githubDir, Ref: "main", Timeout: time.Minute}); err == nil {
		t.Fatal("an unfetchable canonical clone was accepted")
	}
}

//nolint:paralleltest // calls runnertest.AllowRealProcess, which Go's testing package forbids combined with t.Parallel
func TestOrchCovEnsureCanonicalRefreshesAStaleOriginHeadSymref(t *testing.T) {
	runnertest.AllowRealProcess(t)
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	remote := filepath.Join(root, "remote.git")
	writeEngineFile(t, filepath.Join(seed, "file.txt"), "contents\n")
	runEngineGit(t, seed, "init", "-b", "trunk")
	runEngineGit(t, seed, "config", "user.name", "WB Test")
	runEngineGit(t, seed, "config", "user.email", "wb@example.test")
	runEngineGit(t, seed, "add", "-A")
	runEngineGit(t, seed, "commit", "-m", "initial")
	runEngineGit(t, root, "clone", "--bare", seed, remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)

	// A clone assembled by `git init` + `git remote add` + fetch never gets
	// the origin/HEAD symref a `git clone` would have set, which is exactly
	// the long-lived fleet layout the fallback exists for.
	projectsRoot := filepath.Join(root, "projects")
	canonical := filepath.Join(projectsRoot, "acme", "assembled")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, canonical, "init", "-b", "main")
	runEngineGit(t, canonical, "remote", "add", "origin", remote)
	runEngineGit(t, canonical, "fetch", "origin")

	resolved, err := EnsureCanonical(context.Background(),
		Repository{Slug: "acme/assembled", Path: canonical, CloneURL: remote}, canonical,
		Options{GitHubDir: projectsRoot, Ref: "main", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Ref != "trunk" || !resolved.Fallback {
		t.Fatalf("resolved = %+v, want the refreshed default branch trunk", resolved)
	}
	if symref := strings.TrimSpace(runEngineGit(t, canonical, "symbolic-ref", "refs/remotes/origin/HEAD")); symref != "refs/remotes/origin/trunk" {
		t.Fatalf("origin/HEAD symref = %q", symref)
	}
}

//nolint:paralleltest // calls runnertest.AllowRealProcess, which Go's testing package forbids combined with t.Parallel
func TestOrchCovEnsureCanonicalReportsAnUnresolvableDefaultBranch(t *testing.T) {
	runnertest.AllowRealProcess(t)
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	canonical := filepath.Join(projectsRoot, "acme", "broken")
	emptyRemote := filepath.Join(root, "empty.git")
	runEngineGit(t, root, "init", "--bare", emptyRemote)
	testenv.ConfigureGitAutoMaintenanceOff(t, emptyRemote)
	runEngineGit(t, root, "clone", emptyRemote, canonical)
	if _, err := EnsureCanonical(context.Background(),
		Repository{Slug: "acme/broken", Path: canonical, CloneURL: filepath.Join(root, "empty.git")}, canonical,
		Options{GitHubDir: projectsRoot, Ref: "main", Timeout: time.Minute}); err == nil ||
		!strings.Contains(err.Error(), "does not contain origin/main") {
		t.Fatalf("unresolvable default branch error = %v", err)
	}
}

func TestOrchCovPrepareWorktreeRefusesAnExistingWorktreeOrBranch(t *testing.T) {
	t.Run("existing worktree", func(t *testing.T) {
		fixture := newEngineFixture(t)
		options := fixture.options()
		options.Commit = true
		if _, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options); err != nil {
			t.Fatal(err)
		}
		_, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
		if err == nil || !strings.Contains(err.Error(), "operation worktree already exists") {
			t.Fatalf("second run error = %v", err)
		}
	})
	t.Run("existing branch", func(t *testing.T) {
		fixture := newEngineFixture(t)
		options := fixture.options()
		options.Commit = true
		runEngineGit(t, fixture.canonical, "branch", options.Branch)
		_, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
		if err == nil || !strings.Contains(err.Error(), "operation branch already exists") {
			t.Fatalf("existing branch error = %v", err)
		}
	})
	t.Run("resume on the wrong branch", func(t *testing.T) {
		fixture := newEngineFixture(t)
		options := fixture.options()
		options.Commit = true
		results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
		if err != nil {
			t.Fatal(err)
		}
		runEngineGit(t, results[0].WorktreeDir, "checkout", "-b", "some-other-branch")
		resume := fixture.options()
		resume.Commit = true
		resume.Resume = true
		_, err = Run(context.Background(), []Repository{fixture.repository}, textHandler{}, resume)
		if err == nil || !strings.Contains(err.Error(), "cannot resume worktree branch") {
			t.Fatalf("wrong-branch resume error = %v", err)
		}
	})
}
