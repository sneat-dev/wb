package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// migCovApplyCampaign is one hand-built campaign whose repository is cloned
// and worktreed for real, so an individual apply() phase can be driven
// without depending on the planner's choices.
type migCovApplyCampaign struct {
	campaign *campaign
	repo     *campaignRepository
	module   *campaignModule
}

func migCovNewApplyCampaign(t *testing.T, declaredModule, modulePath string, files map[string]string, spec Spec, options CampaignOptions) migCovApplyCampaign {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "WB Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "wb@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "WB Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "wb@example.test")
	root := t.TempDir()
	githubDir := filepath.Join(root, "github")
	t.Setenv(wbhome.EnvOverride, filepath.Join(githubDir, ".wb"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	// The hand-built campaign bypasses normalizeCampaignOptions, so supply the
	// projects root the campaign derives placement and state from.
	if strings.TrimSpace(options.GitHubDir) == "" {
		options.GitHubDir = githubDir
	}

	source := filepath.Join(root, "source")
	writeCampaignFile(t, filepath.Join(source, "go.mod"), "module "+declaredModule+"\n\ngo 1.24\n")
	for name, contents := range files {
		writeCampaignFile(t, filepath.Join(source, name), contents)
	}
	remote := filepath.Join(root, "remote.git")
	commitCampaignRepository(t, source, remote)

	repo := &campaignRepository{
		repository: "github.com/acme/app", owner: "acme", name: "app",
		canonical: filepath.Join(root, "github", "acme", "app"),
		worktree:  filepath.Join(root, "campaign-worktree-placeholder"),
		branch:    "wb/migrate/" + slug(spec.ID),
		ref:       "main",
		cloneURL:  remote,
		report:    &CampaignRepositoryReport{Repository: "github.com/acme/app", Actions: []string{}},
	}
	module := &campaignModule{
		path: modulePath, repository: repo.repository, migrate: true,
		report: &CampaignModuleReport{Path: modulePath, MigrationEnabled: true},
	}
	repo.modules = []*campaignModule{module}
	c := &campaign{
		spec:    spec,
		options: options,
		modules: map[string]*campaignModule{modulePath: module},
		order:   []string{modulePath},
		repos:   []*campaignRepository{repo},
		report:  CampaignReport{Repositories: []CampaignRepositoryReport{{Repository: repo.repository}}},
	}
	return migCovApplyCampaign{campaign: c, repo: repo, module: module}
}

func TestMigCovApplyReportsPreparationAndModuleRootFailures(t *testing.T) {
	// A repository that cannot be cloned fails before any module is touched.
	spec := migCovTextReplaceSpec("apply-clone")
	broken := migCovNewApplyCampaign(t, "github.com/acme/app", "github.com/acme/app", nil, spec, CampaignOptions{Parallel: 1, Verify: VerifyNone})
	broken.repo.cloneURL = filepath.Join(t.TempDir(), "absent.git")
	if err := broken.campaign.apply(); err == nil {
		t.Fatal("apply() succeeded with an unreachable clone URL")
	}

	// A module the campaign expects but the worktree does not declare is
	// reported against its path.
	missing := migCovNewApplyCampaign(t, "github.com/acme/app", "example.com/absent", nil, migCovTextReplaceSpec("apply-missing-module"), CampaignOptions{Parallel: 1, Verify: VerifyNone})
	err := missing.campaign.apply()
	if err == nil || !strings.Contains(err.Error(), "example.com/absent") {
		t.Fatalf("apply() = %v, want a module-root error", err)
	}
}

func TestMigCovApplyReportsSourceAndManifestPhaseFailures(t *testing.T) {
	// A module whose Go source cannot be parsed aborts the source phase.
	brokenSource := migCovNewApplyCampaign(t, "github.com/acme/app", "github.com/acme/app", map[string]string{
		"app.go": "package app\nfunc (\n",
	}, Spec{
		Format: MigrationFormatV1, ID: "apply-source",
		Steps: []Step{{Kind: "import.replace", Language: "go", From: "example.com/old", To: "example.com/new"}},
	}, CampaignOptions{Parallel: 1, Verify: VerifyNone})
	err := brokenSource.campaign.apply()
	if err == nil || !strings.Contains(err.Error(), "plan github.com/acme/app") {
		t.Fatalf("apply() = %v, want a source-phase error", err)
	}

	// A manifest that cannot be normalized aborts the manifest phase. The
	// source phase succeeds first, which is exactly the ordering under test.
	t.Setenv("GOPROXY", "off")
	brokenManifest := migCovNewApplyCampaign(t, "github.com/acme/app", "github.com/acme/app", map[string]string{
		"app.go": "package app\n\nimport _ \"example.com/absent-module\"\n\nconst Value = \"old\"\n",
	}, migCovTextReplaceSpec("apply-manifest"), CampaignOptions{Parallel: 1, Verify: VerifyNone})
	err = brokenManifest.campaign.apply()
	if err == nil || !strings.Contains(err.Error(), "update go.mod for github.com/acme/app") {
		t.Fatalf("apply() = %v, want a manifest-phase error", err)
	}
	if got := mustReadCampaignFile(t, filepath.Join(brokenManifest.repo.worktree, "app.go")); !strings.Contains(got, "new") {
		t.Fatalf("source phase did not run before the manifest phase failed: %s", got)
	}
}

func TestMigCovApplyStopsAtVerificationWhenCommitting(t *testing.T) {
	fixture := migCovNewApplyCampaign(t, "github.com/acme/app", "github.com/acme/app", map[string]string{
		"app.go":          "package app\n\nconst Value = \"old\"\n",
		"failing_test.go": "package app\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"intentional\") }\n",
	}, migCovTextReplaceSpec("apply-verify"), CampaignOptions{Parallel: 1, Commit: true, Verify: VerifyTest})
	err := fixture.campaign.apply()
	if err == nil || !strings.Contains(err.Error(), "verification failed for github.com/acme/app") {
		t.Fatalf("apply() = %v, want a verification refusal", err)
	}
	if fixture.repo.report.Commit != "" {
		t.Fatalf("a repository that failed verification was committed: %q", fixture.repo.report.Commit)
	}
}

func TestMigCovApplyRepositorySourcesReportsReportWriteFailure(t *testing.T) {
	root := t.TempDir()
	migCovWriteGoMod(t, root, "module example.com/app\n\ngo 1.24\n")
	writeCampaignFile(t, filepath.Join(root, "app.go"), "package app\n\nconst Value = \"old\"\n")
	reportFile := filepath.Join(t.TempDir(), "report-file")
	if err := os.WriteFile(reportFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	module := &campaignModule{path: "example.com/app", repository: "example.com/app", migrate: true, root: root, report: &CampaignModuleReport{Path: "example.com/app"}}
	c := &campaign{
		spec:    migCovTextReplaceSpec("report-write"),
		options: CampaignOptions{ReportDir: reportFile},
		order:   []string{"example.com/app"},
		modules: map[string]*campaignModule{"example.com/app": module},
	}
	repo := &campaignRepository{repository: "example.com/app", modules: []*campaignModule{module}}
	if err := c.applyRepositorySources(repo); err == nil {
		t.Fatal("applyRepositorySources() ignored a report write failure")
	}
}

func TestMigCovRepositoryComponentLayersSortsPeerComponents(t *testing.T) {
	first := &campaignRepository{repository: "github.com/acme/first"}
	second := &campaignRepository{repository: "github.com/acme/second"}
	c := &campaign{
		repos: []*campaignRepository{second, first},
		modules: map[string]*campaignModule{
			"github.com/acme/first":  {repository: first.repository},
			"github.com/acme/second": {repository: second.repository},
		},
	}
	layers, err := c.repositoryComponentLayers()
	if err != nil {
		t.Fatalf("repositoryComponentLayers() = %v", err)
	}
	if len(layers) != 1 || len(layers[0]) != 2 {
		t.Fatalf("layers = %+v, want one layer with two peer components", layers)
	}
	if layers[0][0][0] != first || layers[0][1][0] != second {
		t.Fatalf("peer components are not sorted by repository: %+v", layers[0])
	}
}

func TestMigCovPublishRepositoryReportsPushAndPullRequestFailures(t *testing.T) {
	// A worktree that is not a repository cannot be pushed.
	push := &campaign{spec: Spec{ID: "push"}, options: CampaignOptions{Push: true}}
	pushRepo := &campaignRepository{repository: "example.com/app", worktree: t.TempDir(), branch: "wb/migrate/push", report: &CampaignRepositoryReport{}}
	if err := push.publishRepository(pushRepo); err == nil {
		t.Fatal("publishRepository() pushed from a non-repository worktree")
	}
	if pushRepo.report.Pushed {
		t.Fatal("a failed push was recorded as pushed")
	}

	// A failed gh invocation is reported and no pull request is recorded.
	binDir := t.TempDir()
	writeCampaignFile(t, filepath.Join(binDir, "gh"), "#!/bin/sh\nif [ \"$1 $2\" = \"pr list\" ]; then exit 0; fi\nexit 1\n")
	if err := os.Chmod(filepath.Join(binDir, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	pr := &campaign{spec: Spec{ID: "pr"}, options: CampaignOptions{PR: true}}
	prRepo := &campaignRepository{repository: "example.com/app", worktree: t.TempDir(), branch: "wb/migrate/pr", ref: "main", report: &CampaignRepositoryReport{}}
	if err := pr.publishRepository(prRepo); err == nil {
		t.Fatal("publishRepository() ignored a failed gh pr create")
	}
	if prRepo.report.PR != "" {
		t.Fatalf("failed pull request was recorded: %q", prRepo.report.PR)
	}
}

func TestMigCovCommitAndPublishRepositoryReportsMissingBaseRef(t *testing.T) {
	repository := t.TempDir()
	runCampaignGit(t, repository, "init", "--initial-branch=main")
	writeCampaignFile(t, filepath.Join(repository, "README.md"), "seed\n")
	runCampaignGit(t, repository, "add", ".")
	runCampaignGit(t, repository, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "-m", "seed")

	repo := &campaignRepository{
		repository: "example.com/app", worktree: repository, ref: "main", branch: "main",
		modules: []*campaignModule{{path: "example.com/app", migrate: true}},
		report:  &CampaignRepositoryReport{},
	}
	c := &campaign{spec: Spec{ID: "commit-base"}, options: CampaignOptions{Resume: true}}
	if err := c.commitAndPublishRepository(repo); err == nil {
		t.Fatal("commitAndPublishRepository() succeeded without a base ref")
	}
}

func TestMigCovSeedCycleComponentReportsUnreadableWorktree(t *testing.T) {
	module := &campaignModule{path: "example.com/app", repository: "github.com/acme/app", migrate: true, root: t.TempDir()}
	repo := &campaignRepository{repository: "github.com/acme/app", worktree: t.TempDir(), branch: "main", modules: []*campaignModule{module}, report: &CampaignRepositoryReport{}}
	c := &campaign{
		spec:    Spec{ID: "seed-unreadable"},
		modules: map[string]*campaignModule{"example.com/app": module},
		repos:   []*campaignRepository{repo},
	}
	if _, err := c.seedCycleComponent(cycleBootstrap{
		repositories: []*campaignRepository{repo},
		modulePaths:  map[string]bool{"example.com/app": true},
	}); err == nil {
		t.Fatal("seedCycleComponent() succeeded with an unreadable worktree")
	}
}

func TestMigCovCampaignRegisteredWorktreesReportsUnresolvableGitDirectory(t *testing.T) {
	githubDir := t.TempDir()
	canonical := filepath.Join(githubDir, "acme", "looping")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(canonical, ".git")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := campaignRegisteredWorktrees(githubDir, "wb/migrate/looping"); err == nil {
		t.Fatal("campaignRegisteredWorktrees() ignored an unresolvable .git entry")
	}
}

func TestMigCovCleanupCampaignWorktreesReportsMissingRegisteredWorktree(t *testing.T) {
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))
	canonical := migCovClone(t, "missing-worktree-canonical", "module github.com/acme/cleanup\n\ngo 1.24\n")
	githubDir := t.TempDir()
	target := filepath.Join(githubDir, "acme", "cleanup")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	runCampaignGit(t, filepath.Dir(target), "clone", canonical, target)
	worktree := filepath.Join(t.TempDir(), "gone-worktree")
	runCampaignGit(t, target, "worktree", "add", "-b", "wb/migrate/cleanup", worktree)
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanupCampaignWorktrees(githubDir, "cleanup"); err == nil {
		t.Fatal("CleanupCampaignWorktrees() ignored a registered but missing worktree")
	}
}
