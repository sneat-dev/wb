package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// migCovClone creates a local bare remote with one commit and returns a clone
// of it whose origin/main tracking ref exists.
func migCovClone(t *testing.T, name string, goMod string) string {
	t.Helper()
	// Give the clone a deterministic commit identity: later phase code commits
	// without -c, so an ambient identity would make this non-hermetic.
	t.Setenv("GIT_AUTHOR_NAME", "WB Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "wb@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "WB Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "wb@example.test")
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if goMod != "" {
		writeCampaignFile(t, filepath.Join(source, "go.mod"), goMod)
	}
	writeCampaignFile(t, filepath.Join(source, "README.md"), "seed\n")
	runCampaignGit(t, source, "init", "--initial-branch=main")
	runCampaignGit(t, source, "add", ".")
	runCampaignGit(t, source, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "-m", "seed")
	remote := filepath.Join(root, name+".git")
	runCampaignGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	runCampaignGit(t, source, "remote", "add", "origin", remote)
	runCampaignGit(t, source, "push", "-u", "origin", "main")
	worktree := filepath.Join(root, name)
	runCampaignGit(t, root, "clone", remote, worktree)
	return worktree
}

func TestMigCovPreflightRepositoryCollectsCycleBootstraps(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	migCovWriteGoMod(t, root, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v0.0.0\n")
	depRoot := t.TempDir()
	appModule := &campaignModule{path: "example.com/app", migrate: true, root: root}
	provided := &campaignModule{path: "example.com/provided", migrate: false, root: t.TempDir()}
	repo := &campaignRepository{
		repository: "example.com/app",
		modules:    []*campaignModule{appModule, provided},
	}
	c := &campaign{spec: Spec{}}

	bootstrap, err := c.preflightRepository(repo, map[string]string{"example.com/dep": depRoot}, map[string]bool{"example.com/dep": true})
	if err != nil {
		t.Fatalf("preflightRepository() error = %v", err)
	}
	if !bootstrap["example.com/dep"] {
		t.Fatalf("bootstrap = %v, want example.com/dep", bootstrap)
	}

	_, err = c.preflightRepository(repo, map[string]string{"example.com/dep": depRoot}, nil)
	if err == nil || !strings.Contains(err.Error(), "make go.mod publishable for example.com/app") {
		t.Fatalf("preflightRepository(blocked) = %v", err)
	}
}

func TestMigCovApplyRepositorySourcesWritesPerModuleReports(t *testing.T) {
	root := t.TempDir()
	migCovWriteGoMod(t, root, "module example.com/app\n\ngo 1.24\n")
	writeCampaignFile(t, filepath.Join(root, "app.go"), "package app\n\nconst Value = \"old\"\n")
	reportDir := t.TempDir()

	appModule := &campaignModule{path: "example.com/app", repository: "example.com/app", migrate: true, root: root, report: &CampaignModuleReport{Path: "example.com/app", MigrationEnabled: true}}
	provided := &campaignModule{path: "example.com/provided", repository: "example.com/app", migrate: false, root: t.TempDir(), report: &CampaignModuleReport{Path: "example.com/provided"}}
	elsewhere := &campaignModule{path: "example.com/elsewhere", repository: "example.com/other", migrate: true, root: t.TempDir(), report: &CampaignModuleReport{Path: "example.com/elsewhere"}}
	c := &campaign{
		spec:    migCovTextReplaceSpec("phase"),
		options: CampaignOptions{ReportDir: reportDir},
		order:   []string{"example.com/elsewhere", "example.com/provided", "example.com/app"},
		modules: map[string]*campaignModule{
			"example.com/elsewhere": elsewhere,
			"example.com/provided":  provided,
			"example.com/app":       appModule,
		},
	}
	repo := &campaignRepository{repository: "example.com/app", modules: []*campaignModule{appModule, provided}}

	if err := c.applyRepositorySources(repo); err != nil {
		t.Fatalf("applyRepositorySources() error = %v", err)
	}
	if appModule.report.Status != "applied" || appModule.report.PlanState != "complete" {
		t.Fatalf("app module report = %+v", appModule.report)
	}
	if appModule.report.ChangedFiles == nil || *appModule.report.ChangedFiles != 1 {
		t.Fatalf("changed files = %v", appModule.report.ChangedFiles)
	}
	if provided.report.Status != "provided" {
		t.Fatalf("provided module report = %+v", provided.report)
	}
	if elsewhere.report.Status == "applied" {
		t.Fatalf("a module from another repository was migrated: %+v", elsewhere.report)
	}
	if appModule.report.MigrationReportPath == "" {
		t.Fatal("per-module migration report path was not recorded")
	}
	for _, name := range []string{"migration.md", "migration.yaml"} {
		if _, err := os.Stat(filepath.Join(appModule.report.MigrationReportPath, name)); err != nil {
			t.Errorf("missing per-module report %s: %v", name, err)
		}
	}
	if got := mustReadCampaignFile(t, filepath.Join(root, "app.go")); !strings.Contains(got, "new") {
		t.Fatalf("source was not rewritten: %s", got)
	}

	// A module root that cannot be scanned aborts the phase.
	broken := &campaignModule{path: "example.com/broken", repository: "example.com/app", migrate: true, root: filepath.Join(t.TempDir(), "absent"), report: &CampaignModuleReport{Path: "example.com/broken"}}
	c.modules["example.com/broken"] = broken
	c.order = []string{"example.com/broken"}
	repo.modules = []*campaignModule{broken}
	if err := c.applyRepositorySources(repo); err == nil || !strings.Contains(err.Error(), "plan example.com/broken") {
		t.Fatalf("applyRepositorySources(broken) = %v", err)
	}
}

func TestMigCovUpdateRepositoryManifestsAndChangeIndex(t *testing.T) {
	worktree := migCovClone(t, "app", "module example.com/app\n\ngo 1.24\n")
	module := &campaignModule{path: "example.com/app", migrate: true, root: worktree, report: &CampaignModuleReport{Path: "example.com/app"}}
	provided := &campaignModule{path: "example.com/provided", migrate: false, root: t.TempDir(), report: &CampaignModuleReport{Path: "example.com/provided"}}
	repo := &campaignRepository{repository: "example.com/app", worktree: worktree, ref: "main", modules: []*campaignModule{module, provided}, report: &CampaignRepositoryReport{}}
	c := &campaign{spec: Spec{}}

	if err := c.updateRepositoryManifests(repo, nil); err != nil {
		t.Fatalf("updateRepositoryManifests() error = %v", err)
	}
	if repo.report.ChangedFiles == nil || len(*repo.report.ChangedFiles) != 0 {
		t.Fatalf("change index = %v, want an empty index", repo.report.ChangedFiles)
	}

	// A module without a manifest is reported against its path.
	module.root = t.TempDir()
	if err := c.updateRepositoryManifests(repo, nil); err == nil || !strings.Contains(err.Error(), "update go.mod for example.com/app") {
		t.Fatalf("updateRepositoryManifests(missing manifest) = %v", err)
	}

	// An unregistered worktree cannot index its changes.
	module.root = t.TempDir()
	migCovWriteGoMod(t, module.root, "module example.com/app\n\ngo 1.24\n")
	repo.worktree = t.TempDir()
	if err := c.updateRepositoryManifests(repo, nil); err == nil || !strings.Contains(err.Error(), "index changed files for example.com/app") {
		t.Fatalf("updateRepositoryManifests(non-git worktree) = %v", err)
	}
}

func TestMigCovFinalizeRepositoryManifestsRecordsPublishableUpdates(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "app")
	migCovWriteGoMod(t, root, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ../dep\n")
	depRoot := filepath.Join(parent, "dep")
	migCovWriteGoMod(t, depRoot, "module example.com/dep\n\ngo 1.24\n")
	module := &campaignModule{path: "example.com/app", migrate: true, root: root, report: &CampaignModuleReport{Path: "example.com/app"}}
	provided := &campaignModule{path: "example.com/provided", migrate: false, root: t.TempDir(), report: &CampaignModuleReport{Path: "example.com/provided"}}
	repo := &campaignRepository{repository: "example.com/app", modules: []*campaignModule{module, provided}}
	c := &campaign{spec: Spec{GoModuleReleases: []GoModuleRelease{{Path: "example.com/dep", Version: "v1.2.0"}}}}

	if err := c.finalizeRepositoryManifests(repo, map[string]string{"example.com/dep": depRoot}, nil); err != nil {
		t.Fatalf("finalizeRepositoryManifests() error = %v", err)
	}
	if !module.report.PublishableManifest {
		t.Fatal("a dropped worktree replacement was not reported as a publishable change")
	}
	if len(module.report.DependencyDecisions) == 0 {
		t.Fatal("no dependency decisions were recorded")
	}
	if got := mustReadCampaignFile(t, filepath.Join(root, "go.mod")); strings.Contains(got, "../dep") {
		t.Fatalf("campaign replacement survived finalization: %s", got)
	}

	module.root = t.TempDir()
	if err := c.finalizeRepositoryManifests(repo, nil, nil); err == nil || !strings.Contains(err.Error(), "make go.mod publishable for example.com/app") {
		t.Fatalf("finalizeRepositoryManifests(missing manifest) = %v", err)
	}
}

func TestMigCovSeedCycleComponentPublishesSeedsAndDerivesVersions(t *testing.T) {
	worktree := migCovClone(t, "cycle", "module example.com/app\n\ngo 1.24\n")
	writeCampaignFile(t, filepath.Join(worktree, "seed.txt"), "dirty\n")
	module := &campaignModule{path: "example.com/app", version: "v1.2.3", repository: "github.com/acme/app", migrate: true, root: worktree}
	repo := &campaignRepository{
		repository: "github.com/acme/app", worktree: worktree, branch: "main",
		modules: []*campaignModule{module}, report: &CampaignRepositoryReport{},
	}
	c := &campaign{
		spec:    Spec{ID: "cycle-seed", Title: "Cycle seed."},
		modules: map[string]*campaignModule{"example.com/app": module},
		repos:   []*campaignRepository{repo},
	}
	versions, err := c.seedCycleComponent(cycleBootstrap{
		repositories: []*campaignRepository{repo},
		modulePaths:  map[string]bool{"example.com/app": true},
	})
	if err != nil {
		t.Fatalf("seedCycleComponent() error = %v", err)
	}
	if !strings.HasPrefix(versions["example.com/app"], "v1.2.4-0.") {
		t.Fatalf("seed version = %q", versions["example.com/app"])
	}
	if !repo.report.Pushed {
		t.Fatal("cycle seed was not pushed")
	}
	if status := runCampaignGit(t, worktree, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Fatalf("cycle seed left the worktree dirty:\n%s", status)
	}

	// An unknown cyclic module is refused.
	if _, err := c.seedCycleComponent(cycleBootstrap{
		repositories: []*campaignRepository{repo},
		modulePaths:  map[string]bool{"example.com/unknown": true},
	}); err == nil || !strings.Contains(err.Error(), "cannot seed unknown cyclic module") {
		t.Fatalf("seedCycleComponent(unknown module) = %v", err)
	}

	// A component with no migrating repository cannot supply a commit.
	idleModule := &campaignModule{path: "example.com/idle", repository: "github.com/acme/idle", migrate: false}
	idle := &campaignRepository{repository: "github.com/acme/idle", worktree: t.TempDir(), branch: "main", modules: []*campaignModule{idleModule}, report: &CampaignRepositoryReport{}}
	c.modules["example.com/idle"] = idleModule
	c.repos = append(c.repos, idle)
	if _, err := c.seedCycleComponent(cycleBootstrap{
		repositories: []*campaignRepository{idle},
		modulePaths:  map[string]bool{"example.com/idle": true},
	}); err == nil || !strings.Contains(err.Error(), "without a migrating repository commit") {
		t.Fatalf("seedCycleComponent(idle) = %v", err)
	}

	// A failed push is reported.
	plain := t.TempDir()
	runCampaignGit(t, plain, "init", "--initial-branch=main")
	writeCampaignFile(t, filepath.Join(plain, "go.mod"), "module example.com/plain\n\ngo 1.24\n")
	runCampaignGit(t, plain, "add", ".")
	runCampaignGit(t, plain, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "-m", "seed")
	plainModule := &campaignModule{path: "example.com/plain", repository: "github.com/acme/plain", migrate: true, root: plain}
	plainRepo := &campaignRepository{repository: "github.com/acme/plain", worktree: plain, branch: "main", modules: []*campaignModule{plainModule}, report: &CampaignRepositoryReport{}}
	c.modules["example.com/plain"] = plainModule
	c.repos = append(c.repos, plainRepo)
	if _, err := c.seedCycleComponent(cycleBootstrap{
		repositories: []*campaignRepository{plainRepo},
		modulePaths:  map[string]bool{"example.com/plain": true},
	}); err == nil {
		t.Fatal("seedCycleComponent() succeeded without a remote")
	}

	// An impossible seed version is reported against its module.
	badVersion := &campaignModule{path: "example.com/app", version: "not-a-version", repository: "github.com/acme/app", migrate: true, root: worktree}
	c.modules["example.com/app"] = badVersion
	repo.modules = []*campaignModule{badVersion}
	if _, err := c.seedCycleComponent(cycleBootstrap{
		repositories: []*campaignRepository{repo},
		modulePaths:  map[string]bool{"example.com/app": true},
	}); err == nil || !strings.Contains(err.Error(), "invalid version") {
		t.Fatalf("seedCycleComponent(bad version) = %v", err)
	}
}

func TestMigCovCommitAndPublishRepositoryFollowsWorktreeState(t *testing.T) {
	worktree := migCovClone(t, "commit", "module example.com/app\n\ngo 1.24\n")
	module := &campaignModule{path: "example.com/app", migrate: true, root: worktree}
	provided := &campaignModule{path: "example.com/provided", migrate: false}
	repo := &campaignRepository{repository: "example.com/app", worktree: worktree, ref: "main", branch: "main", modules: []*campaignModule{module, provided}, report: &CampaignRepositoryReport{}}
	c := &campaign{spec: Spec{ID: "commit-phase", Title: "Commit phase."}, options: CampaignOptions{}}

	// No migrating modules means nothing to publish.
	idle := &campaignRepository{repository: "example.com/idle", worktree: t.TempDir(), modules: []*campaignModule{provided}, report: &CampaignRepositoryReport{}}
	if err := c.commitAndPublishRepository(idle); err != nil {
		t.Fatalf("commitAndPublishRepository(idle) = %v", err)
	}
	if idle.report.Commit != "" {
		t.Fatal("idle repository recorded a commit")
	}

	// A clean worktree without --resume is a no-op.
	if err := c.commitAndPublishRepository(repo); err != nil {
		t.Fatalf("commitAndPublishRepository(clean) = %v", err)
	}
	if repo.report.Commit != "" {
		t.Fatalf("clean repository recorded a commit: %q", repo.report.Commit)
	}

	// A dirty worktree is committed and published.
	writeCampaignFile(t, filepath.Join(worktree, "change.txt"), "changed\n")
	if err := c.commitAndPublishRepository(repo); err != nil {
		t.Fatalf("commitAndPublishRepository(dirty) = %v", err)
	}
	if repo.report.Commit == "" {
		t.Fatal("dirty repository was not committed")
	}
	if status := runCampaignGit(t, worktree, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Fatalf("worktree is still dirty:\n%s", status)
	}

	// A clean --resume with nothing ahead of the base ref is a no-op.
	clean := migCovClone(t, "resume-clean", "module example.com/app\n\ngo 1.24\n")
	cleanModule := &campaignModule{path: "example.com/app", migrate: true, root: clean}
	cleanRepo := &campaignRepository{repository: "example.com/app", worktree: clean, ref: "main", branch: "main", modules: []*campaignModule{cleanModule}, report: &CampaignRepositoryReport{}}
	c.options = CampaignOptions{Resume: true}
	if err := c.commitAndPublishRepository(cleanRepo); err != nil {
		t.Fatalf("commitAndPublishRepository(resume clean) = %v", err)
	}
	if cleanRepo.report.Commit != "" {
		t.Fatalf("clean resume recorded a commit: %q", cleanRepo.report.Commit)
	}

	// A clean --resume that is ahead of the base ref resumes publishing, and
	// --push reaches the remote.
	runCampaignGit(t, clean, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "--allow-empty", "-m", "manual fix")
	c.options = CampaignOptions{Resume: true, Push: true}
	if err := c.commitAndPublishRepository(cleanRepo); err != nil {
		t.Fatalf("commitAndPublishRepository(resume ahead) = %v", err)
	}
	if cleanRepo.report.Commit == "" || !cleanRepo.report.Pushed {
		t.Fatalf("resumed repository report = %+v", cleanRepo.report)
	}

	// An unregistered worktree cannot report its state.
	broken := &campaignRepository{repository: "example.com/broken", worktree: t.TempDir(), ref: "main", branch: "main", modules: []*campaignModule{{path: "example.com/broken", migrate: true}}, report: &CampaignRepositoryReport{}}
	c.options = CampaignOptions{}
	if err := c.commitAndPublishRepository(broken); err == nil {
		t.Fatal("commitAndPublishRepository(non-git worktree) succeeded")
	}
}

func TestMigCovPublishRepositoryOpensPullRequest(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "gh.log")
	writeCampaignFile(t, filepath.Join(binDir, "gh"), `#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
if [ "$1 $2" = "pr list" ]; then
	exit 0
fi
if [ "$1 $2" = "pr create" ]; then
	printf '%s\n' 'https://github.com/example.com/app/pull/3'
	exit 0
fi
exit 1
`)
	if err := os.Chmod(filepath.Join(binDir, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_LOG", logPath)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	repo := &campaignRepository{repository: "example.com/app", worktree: t.TempDir(), ref: "main", branch: "wb/migrate/publish", report: &CampaignRepositoryReport{}}
	c := &campaign{spec: Spec{ID: "publish", Title: "Publish."}, options: CampaignOptions{PR: true}}
	if err := c.publishRepository(repo); err != nil {
		t.Fatalf("publishRepository(pr) = %v", err)
	}
	if repo.report.PR != "https://github.com/example.com/app/pull/3" {
		t.Fatalf("pull request = %q", repo.report.PR)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("gh was not invoked: %v", err)
	}

	// Neither --push nor --pr publishes anything.
	quiet := &campaignRepository{repository: "example.com/app", worktree: t.TempDir(), report: &CampaignRepositoryReport{}}
	if err := (&campaign{spec: Spec{ID: "quiet"}, options: CampaignOptions{}}).publishRepository(quiet); err != nil {
		t.Fatalf("publishRepository(quiet) = %v", err)
	}
	if quiet.report.Pushed || quiet.report.PR != "" {
		t.Fatalf("quiet repository report = %+v", quiet.report)
	}
}

func TestMigCovVerifyRepositoryRunsAndReportsVerification(t *testing.T) {
	worktree := migCovClone(t, "verify", "module example.com/app\n\ngo 1.24\n")
	writeCampaignFile(t, filepath.Join(worktree, "app.go"), "package app\n\nconst Value = 1\n")
	writeCampaignFile(t, filepath.Join(worktree, "app_test.go"), "package app\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value != 1 { t.Fatal(\"value\") } }\n")
	module := &campaignModule{path: "example.com/app", migrate: true, root: worktree, report: &CampaignModuleReport{Path: "example.com/app"}}
	provided := &campaignModule{path: "example.com/provided", migrate: false, report: &CampaignModuleReport{Path: "example.com/provided"}}
	repo := &campaignRepository{repository: "example.com/app", worktree: worktree, modules: []*campaignModule{module, provided}}

	// --verify none skips everything.
	c := &campaign{options: CampaignOptions{Verify: VerifyNone}}
	if err := c.verifyRepository(repo, false); err != nil {
		t.Fatalf("verifyRepository(none) = %v", err)
	}
	if len(module.report.Verifications) != 0 {
		t.Fatal("verify none recorded verification results")
	}

	// A publishable-only pass ignores modules with nothing publishable.
	c.options = CampaignOptions{Verify: VerifyTest}
	if err := c.verifyRepository(repo, true); err != nil {
		t.Fatalf("verifyRepository(publishable skip) = %v", err)
	}
	if len(module.report.Verifications) != 0 {
		t.Fatal("publishable-only pass verified an unpublished module")
	}

	// A changed module is verified and the result recorded.
	changed := 1
	module.report.ChangedFiles = &changed
	if err := c.verifyRepository(repo, false); err != nil {
		t.Fatalf("verifyRepository(changed) = %v", err)
	}
	if len(module.report.Verifications) != 1 || !module.report.Verifications[0].Passed || module.report.Verifications[0].Command != "go test ./..." {
		t.Fatalf("verifications = %+v", module.report.Verifications)
	}

	// A failing test aborts the phase with the module and command named.
	writeCampaignFile(t, filepath.Join(worktree, "failing_test.go"), "package app\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"intentional\") }\n")
	c.options = CampaignOptions{Verify: VerifyTest}
	err := c.verifyRepository(repo, false)
	if err == nil || !strings.Contains(err.Error(), "verification failed for example.com/app: go test ./...") {
		t.Fatalf("verifyRepository(failing) = %v", err)
	}

	// An unregistered module root cannot be inspected.
	broken := &campaignRepository{repository: "example.com/broken", modules: []*campaignModule{{path: "example.com/broken", migrate: true, root: t.TempDir(), report: &CampaignModuleReport{}}}}
	if err := c.verifyRepository(broken, false); err == nil {
		t.Fatal("verifyRepository(non-git module) succeeded")
	}
}

func TestMigCovRepositoryComponentLayersSkipsUnknownModules(t *testing.T) {
	t.Parallel()
	// Children whose parent is unknown are ignored entirely.
	c := &campaign{
		modules:  map[string]*campaignModule{},
		children: map[string][]string{"example.com/absent": {"example.com/alsomissing"}},
	}
	layers, err := c.repositoryComponentLayers()
	if err != nil {
		t.Fatalf("repositoryComponentLayers(unknown parent) = %v", err)
	}
	if len(layers) == 0 || len(layers[0]) != 0 {
		t.Fatalf("layers = %+v", layers)
	}

	// A known parent with an unknown child is ignored.
	parent := &campaignModule{path: "example.com/parent", repository: "github.com/acme/parent"}
	c.modules["example.com/parent"] = parent
	c.children = map[string][]string{"example.com/parent": {"example.com/alsomissing"}}
	if _, err := c.repositoryComponentLayers(); err != nil {
		t.Fatalf("repositoryComponentLayers(unknown child) = %v", err)
	}

	// Two modules in the same repository never become a dependency edge.
	sibling := &campaignModule{path: "example.com/parent/sub", repository: "github.com/acme/parent"}
	c.modules["example.com/parent/sub"] = sibling
	c.children = map[string][]string{"example.com/parent": {"example.com/parent/sub"}}
	if _, err := c.repositoryComponentLayers(); err != nil {
		t.Fatalf("repositoryComponentLayers(same repository) = %v", err)
	}

	// A module whose repository is not part of the campaign is ignored.
	ghost := &campaignModule{path: "example.com/ghost", repository: "github.com/acme/ghost"}
	c.modules["example.com/ghost"] = ghost
	c.children = map[string][]string{"example.com/ghost": {"example.com/parent"}}
	if _, err := c.repositoryComponentLayers(); err != nil {
		t.Fatalf("repositoryComponentLayers(ghost repository) = %v", err)
	}
}

func TestMigCovRepositoryComponentsCollapsesCycles(t *testing.T) {
	t.Parallel()
	repositories := map[string]*campaignRepository{
		"github.com/acme/a": {repository: "github.com/acme/a"},
		"github.com/acme/b": {repository: "github.com/acme/b"},
		"github.com/acme/c": {repository: "github.com/acme/c"},
	}
	dependencies := map[string]map[string]bool{
		"github.com/acme/a": {"github.com/acme/b": true},
		"github.com/acme/b": {"github.com/acme/c": true},
		"github.com/acme/c": {"github.com/acme/a": true},
	}
	componentByRepository, count := repositoryComponents(repositories, dependencies)
	if count != 1 {
		t.Fatalf("component count = %d, want 1: %v", count, componentByRepository)
	}
	for _, repo := range repositories {
		if componentByRepository[repo.repository] != 0 {
			t.Fatalf("repository %s is not in the single component: %v", repo.repository, componentByRepository)
		}
	}
}

func TestMigCovValidateResumeWorktreeRefusesWrongCheckouts(t *testing.T) {
	repo := &campaignRepository{repository: "example.com/app", branch: "wb/migrate/resume", worktree: t.TempDir()}
	if err := validateResumeWorktree(repo); err == nil {
		t.Fatal("validateResumeWorktree(non-git worktree) succeeded")
	}

	worktree := migCovClone(t, "resume-validate", "module example.com/app\n\ngo 1.24\n")
	repo.worktree = worktree
	if err := validateResumeWorktree(repo); err == nil || !strings.Contains(err.Error(), "worktree branch is") {
		t.Fatalf("validateResumeWorktree(wrong branch) = %v", err)
	}

	repo.branch = "main"
	if err := validateResumeWorktree(repo); err != nil {
		t.Fatalf("validateResumeWorktree(matching) = %v", err)
	}
}

func TestMigCovPrepareCampaignRepositoryReportsPreparationFailures(t *testing.T) {
	// A clone that cannot be reached is reported instead of creating a
	// worktree from nothing.
	githubDir := t.TempDir()
	repo := &campaignRepository{
		repository: "github.com/acme/missing", owner: "acme", name: "missing",
		canonical: filepath.Join(githubDir, "acme", "missing"),
		worktree:  filepath.Join(githubDir, "acme", "missing", ".worktrees", "x"),
		cloneURL:  filepath.Join(t.TempDir(), "absent.git"),
		report:    &CampaignRepositoryReport{},
	}
	if err := prepareCampaignRepository(repo, githubDir); err == nil {
		t.Fatal("prepareCampaignRepository(absent remote) succeeded")
	}

	// An existing canonical clone with no origin cannot be fetched.
	local := migCovClone(t, "canonical", "module github.com/acme/repo\n\ngo 1.24\n")
	runCampaignGit(t, local, "remote", "remove", "origin")
	localRepo := &campaignRepository{
		repository: "github.com/acme/repo", owner: "acme", name: "repo",
		canonical: local,
		worktree:  filepath.Join(t.TempDir(), "worktree"),
		branch:    "wb/migrate/prepare",
		ref:       "main",
		report:    &CampaignRepositoryReport{},
	}
	if err := prepareCampaignRepository(localRepo, githubDir); err == nil {
		t.Fatal("prepareCampaignRepository(no origin) succeeded")
	}

	// A missing base ref is reported before any worktree work happens.
	pinned := migCovClone(t, "pinned", "module github.com/acme/pinned\n\ngo 1.24\n")
	pinnedRepo := &campaignRepository{
		repository: "github.com/acme/pinned", owner: "acme", name: "pinned",
		canonical: pinned, worktree: filepath.Join(t.TempDir(), "worktree"),
		branch: "wb/migrate/prepare", ref: "release", report: &CampaignRepositoryReport{},
	}
	if err := prepareCampaignRepository(pinnedRepo, githubDir); err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("prepareCampaignRepository(missing ref) = %v", err)
	}

	// An existing worktree without --resume is refused rather than reused.
	reusable := migCovClone(t, "reusable", "module github.com/acme/reusable\n\ngo 1.24\n")
	existing := filepath.Join(t.TempDir(), "existing-worktree")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	existingRepo := &campaignRepository{
		repository: "github.com/acme/reusable", owner: "acme", name: "reusable",
		canonical: reusable, worktree: existing, branch: "wb/migrate/prepare", ref: "main",
		report: &CampaignRepositoryReport{},
	}
	if err := prepareCampaignRepository(existingRepo, githubDir); err == nil || !strings.Contains(err.Error(), "worktree already exists") {
		t.Fatalf("prepareCampaignRepository(existing worktree) = %v", err)
	}

	// A branch that already exists in the canonical clone is refused.
	branchExists := migCovClone(t, "branch-exists", "module github.com/acme/branch\n\ngo 1.24\n")
	runCampaignGit(t, branchExists, "branch", "wb/migrate/prepare")
	branchRepo := &campaignRepository{
		repository: "github.com/acme/branch", owner: "acme", name: "branch",
		canonical: branchExists, worktree: filepath.Join(t.TempDir(), "worktree"),
		branch: "wb/migrate/prepare", ref: "main", report: &CampaignRepositoryReport{},
	}
	if err := prepareCampaignRepository(branchRepo, githubDir); err == nil || !strings.Contains(err.Error(), "campaign branch already exists") {
		t.Fatalf("prepareCampaignRepository(existing branch) = %v", err)
	}
}

func TestMigCovCampaignRegisteredWorktreesSkipsNonRepositories(t *testing.T) {
	if worktrees, err := campaignRegisteredWorktrees(filepath.Join(t.TempDir(), "absent"), "wb/migrate/x"); err != nil || worktrees != nil {
		t.Fatalf("campaignRegisteredWorktrees(absent) = %v, %v", worktrees, err)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := campaignRegisteredWorktrees(file, "wb/migrate/x"); err == nil {
		t.Fatal("campaignRegisteredWorktrees(file) succeeded")
	}

	githubDir := t.TempDir()
	// A plain file in the owner slot and a plain file in the repository slot
	// are both skipped.
	if err := os.WriteFile(filepath.Join(githubDir, "owner-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	owner := filepath.Join(githubDir, "acme")
	if err := os.MkdirAll(owner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, "repo-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory without .git is not a canonical clone.
	if err := os.MkdirAll(filepath.Join(owner, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A .git file (worktree checkout) is not a canonical clone.
	worktreeLike := filepath.Join(owner, "worktree-like")
	if err := os.MkdirAll(worktreeLike, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeLike, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A .git directory that is not a repository is reported.
	broken := filepath.Join(owner, "broken")
	if err := os.MkdirAll(filepath.Join(broken, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := campaignRegisteredWorktrees(githubDir, "wb/migrate/x"); err == nil {
		t.Fatal("campaignRegisteredWorktrees(broken clone) succeeded")
	}

	if err := os.RemoveAll(broken); err != nil {
		t.Fatal(err)
	}
	worktrees, err := campaignRegisteredWorktrees(githubDir, "wb/migrate/x")
	if err != nil || len(worktrees) != 0 {
		t.Fatalf("campaignRegisteredWorktrees(none registered) = %v, %v", worktrees, err)
	}
}

func TestMigCovCleanupCampaignWorktreesRefusesLockedAndDirty(t *testing.T) {
	githubDir := t.TempDir()
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))
	home, err := wbhome.Root(githubDir)
	if err != nil {
		t.Fatal(err)
	}
	lockDir := filepath.Join(home, "worktrees", slug("cleanup"))
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, ".lock"), []byte("lock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanupCampaignWorktrees(githubDir, "cleanup"); err == nil || !strings.Contains(err.Error(), "is locked") {
		t.Fatalf("CleanupCampaignWorktrees(locked) = %v", err)
	}
	if err := os.RemoveAll(filepath.Join(home, "worktrees")); err != nil {
		t.Fatal(err)
	}

	// A github directory that is really a file cannot be enumerated.
	fileGithub := filepath.Join(t.TempDir(), "github")
	if err := os.WriteFile(fileGithub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanupCampaignWorktrees(fileGithub, "cleanup"); err == nil {
		t.Fatal("CleanupCampaignWorktrees(file github dir) succeeded")
	}

	// A registered, dirty campaign worktree is refused.
	canonical := migCovClone(t, "cleanup-canonical", "module github.com/acme/cleanup\n\ngo 1.24\n")
	campaignGithub := t.TempDir()
	ownerDir := filepath.Join(campaignGithub, "acme")
	if err := os.MkdirAll(ownerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(ownerDir, "cleanup")
	runCampaignGit(t, filepath.Dir(target), "clone", canonical, target)
	worktree := filepath.Join(t.TempDir(), "cleanup-worktree")
	runCampaignGit(t, target, "worktree", "add", "-b", "wb/migrate/cleanup", worktree)
	writeCampaignFile(t, filepath.Join(worktree, "dirty.txt"), "dirty\n")
	if _, err := CleanupCampaignWorktrees(campaignGithub, "cleanup"); err == nil || !strings.Contains(err.Error(), "refusing to clean dirty") {
		t.Fatalf("CleanupCampaignWorktrees(dirty) = %v", err)
	}

	// A clean campaign worktree is removed and reported.
	runCampaignGit(t, worktree, "clean", "-fd")
	removed, err := CleanupCampaignWorktrees(campaignGithub, "cleanup")
	if err != nil {
		t.Fatalf("CleanupCampaignWorktrees(clean) = %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed = %v", removed)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
}

func TestMigCovAcquireCampaignLockReportsUnusableRoots(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The state home derives from the projects root now, so an unusable root
	// is passed as githubDir instead of through WB_HOME.
	if _, err := acquireCampaignLock(filepath.Join(file, "projects"), "unusable-root"); err == nil {
		t.Fatal("acquireCampaignLock(unusable projects root) succeeded")
	}

	// A symlinked worktrees directory is refused by the operation lock
	// directory rather than followed.
	githubDir := t.TempDir()
	realWorktrees := t.TempDir()
	if err := os.MkdirAll(filepath.Join(githubDir, ".wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realWorktrees, filepath.Join(githubDir, ".wb", "worktrees")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := acquireCampaignLock(githubDir, "symlinked-worktrees"); err == nil {
		t.Fatal("acquireCampaignLock(symlinked worktrees root) succeeded")
	}
}

func TestMigCovValidCampaignLockMetadataRejectsMalformedContents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	for name, contents := range map[string]string{
		"empty":            "",
		"wrong migration":  "migration=other\npid=1\n",
		"missing pid line": "migration=exact\n",
		"non-numeric pid":  "migration=exact\npid=abc\n",
		"zero pid":         "migration=exact\npid=0\n",
		"trailing line":    "migration=exact\npid=1\n\n",
		"oversized":        "migration=exact\npid=1\n" + strings.Repeat("x", 5000),
	} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if validCampaignLockMetadata(file, "exact") {
			t.Errorf("validCampaignLockMetadata(%s) = true, want false", name)
		}
		_ = file.Close()
	}

	if err := os.WriteFile(path, []byte("migration=exact\npid=4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if !validCampaignLockMetadata(file, "exact") {
		t.Fatal("validCampaignLockMetadata(valid) = false")
	}
}
