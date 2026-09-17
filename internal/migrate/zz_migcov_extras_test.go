package migrate

import (
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigCovImportedGoPackageWithoutScopeOrBinding(t *testing.T) {
	// A selector whose package binding was never recorded resolves to no
	// package at all rather than falling back to a spelling-only match.
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}, Scopes: map[ast.Node]*types.Scope{}}
	if importedGoPackage(info, ast.NewIdent("dal"), "example.com/dal") {
		t.Fatal("importedGoPackage() bound an identifier with no recorded package")
	}
}

func TestMigCovReplaceGoModuleReportsGoEditFailure(t *testing.T) {
	dir := t.TempDir()
	goMod := migCovWriteGoMod(t, dir, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n\nreplace example.com/dep v1.0.0 => ../pinned\n")
	target := filepath.Join(dir, "dep")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	migCovInstallFakeGo(t)
	if err := replaceGoModule(dir, goMod, "example.com/dep", target); err == nil {
		t.Fatal("replaceGoModule() ignored a failing go mod edit")
	}
}

func TestMigCovPopulateGoModPathsReportsDecodeAndReplacementMetadata(t *testing.T) {
	root := t.TempDir()
	migCovInstallFakeGo(t)
	t.Setenv("GOMODCACHE", t.TempDir())

	t.Setenv("GO_FAKE_MODE", "ok")
	t.Setenv("GO_FAKE_LIST", "not json")
	modules := map[string]listedModule{"example.com/x": {Path: "example.com/x", Version: "v1.0.0"}}
	if err := populateGoModPaths(root, modules); err == nil || !strings.Contains(err.Error(), "decode module metadata") {
		t.Fatalf("populateGoModPaths(bad json) = %v", err)
	}

	// A reported replacement's go.mod is the one that is used.
	t.Setenv("GO_FAKE_LIST", `{"Path":"example.com/x","Version":"v1.0.0","GoMod":"/plain/go.mod","Replace":{"Path":"example.com/y","GoMod":"/replaced/go.mod"}}`)
	modules = map[string]listedModule{"example.com/x": {Path: "example.com/x", Version: "v1.0.0"}}
	if err := populateGoModPaths(root, modules); err != nil {
		t.Fatalf("populateGoModPaths(replacement) = %v", err)
	}
	if got := modules["example.com/x"].GoMod; got != "/replaced/go.mod" {
		t.Fatalf("resolved go.mod = %q, want the replacement's go.mod", got)
	}
}

func TestMigCovCycleSeedingReportsGitFailures(t *testing.T) {
	newCampaign := func(worktree string) *campaign {
		module := &campaignModule{path: "example.com/app", repository: "github.com/acme/app", migrate: true, root: worktree}
		repo := &campaignRepository{repository: "github.com/acme/app", worktree: worktree, branch: "main", modules: []*campaignModule{module}, report: &CampaignRepositoryReport{}}
		return &campaign{
			spec:    Spec{ID: "seed-git-failure"},
			modules: map[string]*campaignModule{"example.com/app": module},
			repos:   []*campaignRepository{repo},
		}
	}
	bootstrap := func() cycleBootstrap {
		return cycleBootstrap{
			repositories: []*campaignRepository{{}},
			modulePaths:  map[string]bool{"example.com/app": true},
		}
	}

	// A locked index makes staging fail.
	locked := migCovClone(t, "seed-add-fail", "module example.com/app\n\ngo 1.24\n")
	writeCampaignFile(t, filepath.Join(locked, "dirty.txt"), "dirty\n")
	writeCampaignFile(t, filepath.Join(locked, ".git", "index.lock"), "")
	lockedCampaign := newCampaign(locked)
	lockedBootstrap := bootstrap()
	lockedBootstrap.repositories = lockedCampaign.repos
	if _, err := lockedCampaign.seedCycleComponent(lockedBootstrap); err == nil {
		t.Fatal("seedCycleComponent() staged changes with a locked index")
	}
	if err := os.Remove(filepath.Join(locked, ".git", "index.lock")); err != nil {
		t.Fatal(err)
	}

	// Without a Git identity the seed commit is refused.
	migCovIsolateGitIdentity(t)
	unidentified := newCampaign(locked)
	unidentifiedBootstrap := bootstrap()
	unidentifiedBootstrap.repositories = unidentified.repos
	if _, err := unidentified.seedCycleComponent(unidentifiedBootstrap); err == nil {
		t.Fatal("seedCycleComponent() committed without a Git identity")
	}
}

func TestMigCovCommitAndPublishRepositoryReportsGitFailures(t *testing.T) {
	worktree := migCovClone(t, "commit-git-failure", "module example.com/app\n\ngo 1.24\n")
	writeCampaignFile(t, filepath.Join(worktree, "change.txt"), "changed\n")
	module := &campaignModule{path: "example.com/app", migrate: true, root: worktree}
	repo := &campaignRepository{repository: "example.com/app", worktree: worktree, ref: "main", branch: "main", modules: []*campaignModule{module}, report: &CampaignRepositoryReport{}}
	c := &campaign{spec: Spec{ID: "commit-git-failure"}, options: CampaignOptions{}}

	// A locked index makes staging fail.
	writeCampaignFile(t, filepath.Join(worktree, ".git", "index.lock"), "")
	if err := c.commitAndPublishRepository(repo); err == nil {
		t.Fatal("commitAndPublishRepository() staged changes with a locked index")
	}
	if err := os.Remove(filepath.Join(worktree, ".git", "index.lock")); err != nil {
		t.Fatal(err)
	}

	// Without a Git identity the review commit is refused.
	migCovIsolateGitIdentity(t)
	if err := c.commitAndPublishRepository(repo); err == nil {
		t.Fatal("commitAndPublishRepository() committed without a Git identity")
	}
	if repo.report.Commit != "" {
		t.Fatalf("failed commit was recorded: %q", repo.report.Commit)
	}
}

func migCovIsolateGitIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_AUTHOR_EMAIL", "")
	t.Setenv("GIT_COMMITTER_NAME", "")
	t.Setenv("GIT_COMMITTER_EMAIL", "")
}

func TestMigCovCampaignRegisteredWorktreesReportsUnreadableOwner(t *testing.T) {
	githubDir := t.TempDir()
	owner := filepath.Join(githubDir, "acme")
	if err := os.MkdirAll(owner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(owner, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(owner, 0o755) })
	if _, err := os.ReadDir(owner); err == nil {
		t.Skip("filesystem does not enforce directory read permissions")
	}
	if _, err := campaignRegisteredWorktrees(githubDir, "wb/migrate/unreadable"); err == nil {
		t.Fatal("campaignRegisteredWorktrees() ignored an unreadable owner directory")
	}
}

func TestMigCovCampaignPRReportsLatePhaseFailures(t *testing.T) {
	run := func(t *testing.T, mode string, mutate func(*campaignIntegrationFixture)) error {
		t.Helper()
		fixture := migCovPRFixture(t)
		migCovInstallFakeGH(t)
		t.Setenv("GH_FAKE_MODE", mode)
		if mutate != nil {
			mutate(&fixture)
		}
		_, err := RunCampaign(fixture.spec, fixture.sourceRoot, CampaignOptions{
			GitHubDir: fixture.githubDir,
			Apply:     true,
			PR:        true,
			Merge:     true,
			Verify:    VerifyNone,
			Parallel:  1,
			CloneURL:  fixture.cloneURL,
		})
		return err
	}

	t.Run("finalize", func(t *testing.T) {
		err := run(t, "ok", func(fixture *campaignIntegrationFixture) {
			fixture.spec.GoModuleReleases = []GoModuleRelease{{Path: "github.com/acme/provider", Version: "not a version"}}
		})
		if err == nil || !strings.Contains(err.Error(), "make go.mod publishable for github.com/acme/consumer") {
			t.Fatalf("RunCampaign() = %v, want a finalize refusal", err)
		}
	})

	t.Run("push", func(t *testing.T) {
		err := run(t, "fail-create", nil)
		if err == nil {
			t.Fatal("RunCampaign() succeeded although gh pr create failed")
		}
	})

	t.Run("checks-error", func(t *testing.T) {
		err := run(t, "fail-api", nil)
		if err == nil {
			t.Fatal("RunCampaign() succeeded although checks could not be read")
		}
	})

	t.Run("checks-not-green", func(t *testing.T) {
		err := run(t, "fail-checks", nil)
		if err == nil || !strings.Contains(err.Error(), "required checks are not successful") {
			t.Fatalf("RunCampaign() = %v, want a required-checks refusal", err)
		}
	})

	t.Run("merge", func(t *testing.T) {
		err := run(t, "fail-merge", nil)
		if err == nil {
			t.Fatal("RunCampaign() succeeded although gh pr merge failed")
		}
	})
}

func TestMigCovFinalizeGoModuleReportsGoEditFailure(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "app")
	depRoot := filepath.Join(parent, "dep")
	migCovWriteGoMod(t, root, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n\nreplace example.com/dep => ../dep\n")
	migCovWriteGoMod(t, depRoot, "module example.com/dep\n\ngo 1.24\n")
	migCovInstallFakeGo(t)
	_, err := finalizeGoModule(
		root,
		Spec{GoModuleReleases: []GoModuleRelease{{Path: "example.com/dep", Version: "v1.2.0"}}},
		"example.com/app",
		map[string]string{"example.com/dep": depRoot},
		nil,
	)
	if err == nil {
		t.Fatal("finalizeGoModule() ignored a failing go mod edit")
	}
}
