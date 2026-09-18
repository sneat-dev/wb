package migrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

func migCovWriteGoMod(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "go.mod")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func migCovParseModfile(t *testing.T, contents string) *modfile.File {
	t.Helper()
	parsed, err := modfile.Parse("go.mod", []byte(contents), nil)
	if err != nil {
		t.Fatalf("parse test go.mod: %v", err)
	}
	return parsed
}

func TestMigCovNormalizeCampaignOptionsRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	if _, err := normalizeCampaignOptions(CampaignOptions{}); err == nil || !strings.Contains(err.Error(), "github directory is required") {
		t.Fatalf("normalizeCampaignOptions(no dir) = %v", err)
	}
	if _, err := normalizeCampaignOptions(CampaignOptions{GitHubDir: t.TempDir(), Parallel: -1}); err == nil || !strings.Contains(err.Error(), "parallelism must be at least 1") {
		t.Fatalf("normalizeCampaignOptions(negative parallelism) = %v", err)
	}
	if _, err := normalizeCampaignOptions(CampaignOptions{GitHubDir: t.TempDir(), Verify: Verification("sometimes")}); err == nil || !strings.Contains(err.Error(), "unknown verification mode") {
		t.Fatalf("normalizeCampaignOptions(unknown verification) = %v", err)
	}

	dir := t.TempDir()
	options, err := normalizeCampaignOptions(CampaignOptions{GitHubDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if options.Ref != "main" || options.Parallel != 1 || options.Verify != VerifyFull || options.ModuleRefs == nil {
		t.Fatalf("defaults = %+v", options)
	}
	if !filepath.IsAbs(options.GitHubDir) {
		t.Fatalf("GitHubDir = %q, want absolute", options.GitHubDir)
	}
}

func TestMigCovRunCampaignRejectsInvalidSpecAndMissingDirectory(t *testing.T) {
	t.Parallel()
	if _, err := RunCampaign(Spec{}, t.TempDir(), CampaignOptions{GitHubDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "missing id") {
		t.Fatalf("RunCampaign(invalid spec) = %v", err)
	}
	if _, err := RunCampaign(migCovTextReplaceSpec("no-dir"), t.TempDir(), CampaignOptions{}); err == nil || !strings.Contains(err.Error(), "github directory is required") {
		t.Fatalf("RunCampaign(no github dir) = %v", err)
	}
}

func TestMigCovRunCampaignRefusesAConcurrentlyHeldLock(t *testing.T) {
	githubDir := setupCampaignLockTest(t)
	const migrationID = "concurrent-campaign"
	held, err := acquireCampaignLock(githubDir, migrationID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.release() }()

	spec := migCovTextReplaceSpec(migrationID)
	_, err = RunCampaign(spec, t.TempDir(), CampaignOptions{GitHubDir: githubDir, Apply: true})
	if err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("RunCampaign(locked) = %v, want already-active error", err)
	}
}

func TestMigCovRunCampaignReportsPlanFailureAndReturnsPlanOnlyReport(t *testing.T) {
	t.Parallel()
	sourceRoot := t.TempDir()
	githubDir := t.TempDir()
	spec := migCovTextReplaceSpec("plan-only")

	// A source root without a go.mod cannot be inspected.
	if _, err := RunCampaign(spec, sourceRoot, CampaignOptions{GitHubDir: githubDir}); err == nil {
		t.Fatal("RunCampaign(source without go.mod) succeeded")
	}

	// A real scratch module plans without applying anything.
	migCovWriteGoMod(t, sourceRoot, "module github.com/acme/planner\n\ngo 1.24\n")
	before := "package main\n\nconst Old = \"github.com/acme/planner/old\"\n"
	if err := os.WriteFile(filepath.Join(sourceRoot, "main.go"), []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	spec = Spec{
		Format: MigrationFormatV1, ID: "plan-only",
		Steps: []Step{{Kind: "text.replace", From: "github.com/acme/planner/old", To: "github.com/acme/planner/new"}},
	}
	report, err := RunCampaign(spec, sourceRoot, CampaignOptions{GitHubDir: githubDir})
	if err != nil {
		t.Fatalf("RunCampaign(plan only) = %v", err)
	}
	if report.Status != "planned" || len(report.Repositories) != 1 || report.Repositories[0].Repository != "github.com/acme/planner" {
		t.Fatalf("plan report = %+v", report)
	}
	if got := mustReadCampaignFile(t, filepath.Join(sourceRoot, "main.go")); got != before {
		t.Fatalf("plan-only campaign changed the source: %q", got)
	}
	if _, err := os.Stat(filepath.Join(githubDir, "acme", "planner")); !os.IsNotExist(err) {
		t.Fatalf("plan-only campaign cloned a repository: %v", err)
	}
}

func TestMigCovCampaignDiscoveryRootSelectsOnlyAValidatedResumeWorktree(t *testing.T) {
	t.Parallel()
	githubDir := t.TempDir()
	sourceRoot := t.TempDir()
	migCovWriteGoMod(t, sourceRoot, "module github.com/acme/repo\n\ngo 1.24\n")
	spec := migCovTextReplaceSpec("resume-discovery")

	// Without --resume the source root is used unchanged.
	root, err := campaignDiscoveryRoot(spec, sourceRoot, CampaignOptions{GitHubDir: githubDir})
	if err != nil || root != sourceRoot {
		t.Fatalf("campaignDiscoveryRoot(no resume) = %q, %v", root, err)
	}

	// --resume without a readable source manifest is refused.
	if _, err := campaignDiscoveryRoot(spec, t.TempDir(), CampaignOptions{GitHubDir: githubDir, Resume: true}); err == nil ||
		!strings.Contains(err.Error(), "read source module for resume discovery") {
		t.Fatalf("campaignDiscoveryRoot(no go.mod) = %v", err)
	}

	// A source root whose go.mod has no module path is refused.
	moduleless := t.TempDir()
	migCovWriteGoMod(t, moduleless, "go 1.24\n")
	if _, err := campaignDiscoveryRoot(spec, moduleless, CampaignOptions{GitHubDir: githubDir, Resume: true}); err == nil ||
		!strings.Contains(err.Error(), "has no module path") {
		t.Fatalf("campaignDiscoveryRoot(moduleless) = %v", err)
	}

	// A non-GitHub module cannot be resolved to a canonical clone.
	nonGitHub := t.TempDir()
	migCovWriteGoMod(t, nonGitHub, "module example.com/local\n\ngo 1.24\n")
	if _, err := campaignDiscoveryRoot(spec, nonGitHub, CampaignOptions{GitHubDir: githubDir, Resume: true}); err == nil ||
		!strings.Contains(err.Error(), "not a resolvable GitHub module") {
		t.Fatalf("campaignDiscoveryRoot(non-GitHub) = %v", err)
	}

	// No canonical clone yet: the source root stays authoritative.
	root, err = campaignDiscoveryRoot(spec, sourceRoot, CampaignOptions{GitHubDir: githubDir, Resume: true})
	if err != nil || root != sourceRoot {
		t.Fatalf("campaignDiscoveryRoot(missing canonical) = %q, %v", root, err)
	}

	// A canonical path that cannot even be inspected is an error.
	if err := os.MkdirAll(filepath.Join(githubDir, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	loop := filepath.Join(githubDir, "acme", "repo")
	if err := os.Symlink("repo", loop); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := campaignDiscoveryRoot(spec, sourceRoot, CampaignOptions{GitHubDir: githubDir, Resume: true}); err == nil {
		t.Fatal("campaignDiscoveryRoot(uninspectable canonical) succeeded")
	}
	if err := os.Remove(loop); err != nil {
		t.Fatal(err)
	}

	// A canonical directory that is not a Git repository is an error, not a
	// silent fallback.
	canonical := filepath.Join(githubDir, "acme", "repo")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := campaignDiscoveryRoot(spec, sourceRoot, CampaignOptions{GitHubDir: githubDir, Resume: true}); err == nil {
		t.Fatal("campaignDiscoveryRoot(non-git canonical) succeeded")
	}

	// A real clone with no campaign worktree falls back to the source root.
	if err := os.RemoveAll(canonical); err != nil {
		t.Fatal(err)
	}
	migCovInitGitRepository(t, canonical, "module github.com/acme/repo\n\ngo 1.24\n")
	root, err = campaignDiscoveryRoot(spec, sourceRoot, CampaignOptions{GitHubDir: githubDir, Resume: true})
	if err != nil || root != sourceRoot {
		t.Fatalf("campaignDiscoveryRoot(no worktree) = %q, %v", root, err)
	}

	// A registered campaign worktree whose module no longer matches is refused.
	worktree := filepath.Join(t.TempDir(), "worktree")
	runCampaignGit(t, canonical, "worktree", "add", "-b", "wb/migrate/"+slug(spec.ID), worktree)
	migCovWriteGoMod(t, worktree, "module github.com/acme/renamed\n\ngo 1.24\n")
	if _, err := campaignDiscoveryRoot(spec, sourceRoot, CampaignOptions{GitHubDir: githubDir, Resume: true}); err == nil ||
		!strings.Contains(err.Error(), "locate resumed source module") {
		t.Fatalf("campaignDiscoveryRoot(module mismatch) = %v", err)
	}

	// A matching worktree is discovered and returned as the graph root.
	migCovWriteGoMod(t, worktree, "module github.com/acme/repo\n\ngo 1.24\n")
	resolved, err := campaignDiscoveryRoot(spec, sourceRoot, CampaignOptions{GitHubDir: githubDir, Resume: true})
	if err != nil {
		t.Fatalf("campaignDiscoveryRoot(matching worktree) = %v", err)
	}
	physicalWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(resolved) != filepath.Clean(physicalWorktree) {
		t.Fatalf("discovered root = %q, want %q", resolved, physicalWorktree)
	}

	// A registered worktree whose directory has vanished cannot be inspected.
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}
	if _, err := campaignDiscoveryRoot(spec, sourceRoot, CampaignOptions{GitHubDir: githubDir, Resume: true}); err == nil {
		t.Fatal("campaignDiscoveryRoot(missing registered worktree) succeeded")
	}
}

func migCovInitGitRepository(t *testing.T, dir, goMod string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runCampaignGit(t, dir, "init", "--initial-branch=main")
	if goMod != "" {
		writeCampaignFile(t, filepath.Join(dir, "go.mod"), goMod)
	}
	writeCampaignFile(t, filepath.Join(dir, "README.md"), "seed\n")
	runCampaignGit(t, dir, "add", ".")
	runCampaignGit(t, dir, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "-m", "seed")
}

func TestMigCovCampaignPureHelpers(t *testing.T) {
	t.Parallel()
	if owner, name, repository, err := githubRepository("example.com/acme/repo"); err == nil || repository != "" {
		t.Fatalf("githubRepository(non-GitHub) = %q %q %q %v", owner, name, repository, err)
	}
	if _, _, _, err := githubRepository("github.com"); err == nil {
		t.Fatal("githubRepository(short path) succeeded")
	}

	for input, want := range map[string]string{
		"A B/C":  "a-b-c",
		"-x-":    "x",
		"":       "",
		"café-1": "café-1",
	} {
		if got := slug(input); got != want {
			t.Errorf("slug(%q) = %q, want %q", input, got, want)
		}
	}

	if got := lastNonEmptyLine("first\n\n   \n"); got != "first" {
		t.Errorf("lastNonEmptyLine() = %q, want first", got)
	}
	if got := lastNonEmptyLine("   \n\n"); got != "" {
		t.Errorf("lastNonEmptyLine(blank) = %q, want empty", got)
	}

	long := strings.Repeat("x", 1001)
	if got := shortenedDetail(long); len(got) != 1000+len("…") || !strings.HasSuffix(got, "…") {
		t.Errorf("shortenedDetail(long) length = %d", len(got))
	}
	if got := shortenedDetail("  short  "); got != "short" {
		t.Errorf("shortenedDetail(short) = %q", got)
	}

	provider := &campaignRepository{repository: "github.com/acme/provider"}
	consumer := &campaignRepository{repository: "github.com/acme/consumer"}
	if got := orderedRepositories([][]*campaignRepository{{provider}, {consumer}}); len(got) != 2 || got[0] != provider || got[1] != consumer {
		t.Fatalf("orderedRepositories() = %+v", got)
	}

	c := campaign{repos: []*campaignRepository{provider, consumer}}
	if c.repositoryByName("github.com/acme/consumer") != consumer {
		t.Fatal("repositoryByName found the wrong repository")
	}
	if c.repositoryByName("github.com/acme/absent") != nil {
		t.Fatal("repositoryByName invented a repository")
	}
}

func TestMigCovRunRepositoriesParallelPropagatesTheFirstError(t *testing.T) {
	t.Parallel()
	first := &campaignRepository{repository: "github.com/acme/first"}
	second := &campaignRepository{repository: "github.com/acme/second"}
	repos := []*campaignRepository{first, second}

	err := runRepositoriesParallel(repos, 1, func(repo *campaignRepository) error {
		if repo == second {
			return errors.New("second failed")
		}
		return nil
	})
	if err == nil || err.Error() != "second failed" {
		t.Fatalf("runRepositoriesParallel() = %v", err)
	}
	if err := runRepositoriesParallel(repos, 2, func(*campaignRepository) error { return nil }); err != nil {
		t.Fatalf("runRepositoriesParallel(success) = %v", err)
	}
	if errs := runRepositoriesParallelErrors(nil, 2, func(*campaignRepository) error { return nil }); errs != nil {
		t.Fatalf("runRepositoriesParallelErrors(empty) = %v, want nil", errs)
	}
}

func TestMigCovPseudoVersionForCommitRejectsImpossibleSeeds(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	runCampaignGit(t, repository, "init", "--initial-branch=main")
	migCovWriteGoMod(t, repository, "module github.com/acme/module\n\ngo 1.24\n")
	runCampaignGit(t, repository, "add", ".")
	runCampaignGit(t, repository, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "-m", "seed")
	revision := strings.TrimSpace(runCampaignGit(t, repository, "rev-parse", "HEAD"))

	if _, err := pseudoVersionForCommit(repository, "github.com/acme/module", "v1.0.0", "short"); err == nil ||
		!strings.Contains(err.Error(), "too short") {
		t.Fatalf("pseudoVersionForCommit(short revision) = %v", err)
	}
	if _, err := pseudoVersionForCommit(repository, "github.com/acme/module", "not-a-version", revision); err == nil ||
		!strings.Contains(err.Error(), "invalid version") {
		t.Fatalf("pseudoVersionForCommit(invalid version) = %v", err)
	}
	if _, err := pseudoVersionForCommit(repository, "github.com/acme/module", "v1.0.0", strings.Repeat("0", 12)); err == nil {
		t.Fatal("pseudoVersionForCommit(unknown revision) succeeded")
	}

	// A v2 module path with a v1 base version produces a pseudo-version that
	// Go's own module checker rejects.
	if _, err := pseudoVersionForCommit(repository, "github.com/acme/module/v2", "v1.0.0", revision); err == nil ||
		!strings.Contains(err.Error(), "validate seed pseudo-version") {
		t.Fatalf("pseudoVersionForCommit(major mismatch) = %v", err)
	}
	// A module path with an invalid mandatory version suffix cannot determine
	// a major version.
	if _, err := pseudoVersionForCommit(repository, "github.com/acme/module/v1", "", revision); err == nil ||
		!strings.Contains(err.Error(), "cannot determine module path major") {
		t.Fatalf("pseudoVersionForCommit(invalid path major) = %v", err)
	}
	// A v2 module with no base version derives v2 from the path.
	version, err := pseudoVersionForCommit(repository, "github.com/acme/module/v2", "", revision)
	if err != nil {
		t.Fatalf("pseudoVersionForCommit(v2 path) = %v", err)
	}
	if !strings.HasPrefix(version, "v2.0.0-") {
		t.Fatalf("pseudo-version = %q, want v2.0.0- prefix", version)
	}
}

func TestMigCovCachedGoModPathRejectsMalformedInputs(t *testing.T) {
	t.Parallel()
	if _, err := cachedGoModPath("/cache", "example.com/bad path", "v1.0.0"); err == nil {
		t.Fatal("cachedGoModPath(malformed module path) succeeded")
	}
	if _, err := cachedGoModPath("/cache", "example.com/mod", ""); err == nil {
		t.Fatal("cachedGoModPath(empty version) succeeded")
	}
}

func TestMigCovAddGoModRequirementEdgesHandlesUnreadableManifests(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// A declared module whose go.mod is gone is simply skipped.
	modules := map[string]listedModule{
		"example.com/missing": {Path: "example.com/missing", GoMod: filepath.Join(root, "gone.mod")},
	}
	edges := map[string]map[string]bool{}
	if err := addGoModRequirementEdges(modules, edges); err != nil {
		t.Fatalf("addGoModRequirementEdges(missing file) = %v", err)
	}

	// A directory where a go.mod file is expected is a real read failure.
	directory := filepath.Join(root, "dir.mod")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	modules = map[string]listedModule{"example.com/dir": {Path: "example.com/dir", GoMod: directory}}
	if err := addGoModRequirementEdges(modules, edges); err == nil || !strings.Contains(err.Error(), "read go.mod for example.com/dir") {
		t.Fatalf("addGoModRequirementEdges(directory) = %v", err)
	}

	// An unparseable go.mod is reported against the module it belongs to.
	broken := filepath.Join(root, "broken.mod")
	if err := os.WriteFile(broken, []byte("module example.com/broken\n\nrequire (\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	modules = map[string]listedModule{"example.com/broken": {Path: "example.com/broken", GoMod: broken}}
	if err := addGoModRequirementEdges(modules, edges); err == nil || !strings.Contains(err.Error(), "parse go.mod requirements for example.com/broken") {
		t.Fatalf("addGoModRequirementEdges(broken) = %v", err)
	}

	// Only requirements that the graph already selected become edges.
	good := filepath.Join(root, "good.mod")
	if err := os.WriteFile(good, []byte("module example.com/good\n\ngo 1.24\n\nrequire (\n\texample.com/selected v1.0.0\n\texample.com/unselected v1.0.0\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	modules = map[string]listedModule{
		"example.com/good":     {Path: "example.com/good", GoMod: good},
		"example.com/selected": {Path: "example.com/selected"},
	}
	edges = map[string]map[string]bool{}
	if err := addGoModRequirementEdges(modules, edges); err != nil {
		t.Fatalf("addGoModRequirementEdges(good) = %v", err)
	}
	if !edges["example.com/good"]["example.com/selected"] {
		t.Fatalf("requirement edge missing: %+v", edges)
	}
	if edges["example.com/good"]["example.com/unselected"] {
		t.Fatalf("unselected module became an edge: %+v", edges)
	}
}

func TestMigCovFindModuleRootAndParseGoMod(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent")
	if _, err := findModuleRoot(missing, "example.com/mod"); err == nil {
		t.Fatal("findModuleRoot(missing root) succeeded")
	}
	if _, err := parseGoMod(filepath.Join(t.TempDir(), "go.mod")); err == nil {
		t.Fatal("parseGoMod(missing file) succeeded")
	}

	// A matching module inside a skipped directory is not discovered.
	root := t.TempDir()
	writeCampaignFile(t, filepath.Join(root, ".git", "go.mod"), "module example.com/mod\n\ngo 1.24\n")
	if _, err := findModuleRoot(root, "example.com/mod"); err == nil || !strings.Contains(err.Error(), "no go.mod declares module") {
		t.Fatalf("findModuleRoot(skipped dir) = %v", err)
	}

	// An unparseable go.mod anywhere in the walk aborts discovery.
	brokenRoot := t.TempDir()
	writeCampaignFile(t, filepath.Join(brokenRoot, "go.mod"), "not a go.mod\n")
	if _, err := findModuleRoot(brokenRoot, "example.com/mod"); err == nil {
		t.Fatal("findModuleRoot(broken go.mod) succeeded")
	}

	// The matching module root is returned.
	nested := filepath.Join(root, "sub", "module")
	writeCampaignFile(t, filepath.Join(nested, "go.mod"), "module example.com/other\n\ngo 1.24\n")
	writeCampaignFile(t, filepath.Join(root, "sub", "go.mod"), "module example.com/mod\n\ngo 1.24\n")
	if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	found, err := findModuleRoot(root, "example.com/mod")
	if err != nil {
		t.Fatalf("findModuleRoot() error = %v", err)
	}
	if filepath.Clean(found) != filepath.Clean(filepath.Join(root, "sub")) {
		t.Fatalf("findModuleRoot() = %q", found)
	}
}
