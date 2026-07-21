package migrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/mod/modfile"
	modmodule "golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

// Verification selects the built-in, non-arbitrary checks a hierarchical Go
// campaign runs after applying changes. Full is the default for apply runs.
type Verification string

const (
	VerifyNone    Verification = "none"
	VerifyCompile Verification = "compile"
	VerifyTest    Verification = "test"
	VerifyFull    Verification = "full"
)

// CampaignOptions controls a hierarchical migration. Canonical clones are
// never checked out or altered; PR and merge phases are strictly opt-in.
type CampaignOptions struct {
	GitHubDir  string
	Ref        string
	ModuleRefs map[string]string
	Apply      bool
	Resume     bool
	Verify     Verification
	Commit     bool
	Push       bool
	PR         bool
	Merge      bool
	Parallel   int
	ReportDir  string

	// CloneURL overrides the canonical GitHub clone URL for a repository. It is
	// primarily useful for hermetic integration tests; normal campaigns use
	// https://<repository>.git.
	CloneURL func(repository string) string
}

// CampaignReport is the Markdown/YAML index for a migration campaign.
type CampaignReport struct {
	SchemaVersion int                        `yaml:"schema_version"`
	Migration     ReportMigration            `yaml:"migration"`
	Status        string                     `yaml:"status"`
	SourceRoot    string                     `yaml:"source_root"`
	GitHubDir     string                     `yaml:"github_dir"`
	BaseRef       string                     `yaml:"base_ref"`
	Verification  Verification               `yaml:"verification"`
	Parallel      int                        `yaml:"parallel"`
	Repositories  []CampaignRepositoryReport `yaml:"repositories"`
}

// CampaignRepositoryReport records the isolated worktree used for one GitHub
// repository. Modules can share a repository/worktree when a repo contains
// more than one go.mod.
type CampaignRepositoryReport struct {
	Repository     string                 `yaml:"repository"`
	CanonicalDir   string                 `yaml:"canonical_dir"`
	WorktreeDir    string                 `yaml:"worktree_dir"`
	Branch         string                 `yaml:"branch"`
	Ref            string                 `yaml:"ref"`
	Actions        []string               `yaml:"actions"`
	ChangedFiles   *[]string              `yaml:"changed_files,omitempty"`
	Modules        []CampaignModuleReport `yaml:"modules"`
	Commit         string                 `yaml:"commit,omitempty"`
	Pushed         bool                   `yaml:"pushed,omitempty"`
	PR             string                 `yaml:"pr,omitempty"`
	RequiredChecks []RemoteCheck          `yaml:"required_checks,omitempty"`
	Merged         bool                   `yaml:"merged,omitempty"`
}

// CampaignModuleReport identifies the submodule work done inside a repository.
type CampaignModuleReport struct {
	Path                string               `yaml:"path"`
	WorktreeDir         string               `yaml:"worktree_dir,omitempty"`
	MigrationEnabled    bool                 `yaml:"migration_enabled"`
	PlanState           string               `yaml:"plan_state"`
	ChangedFiles        *int                 `yaml:"changed_files,omitempty"`
	ManifestChanged     bool                 `yaml:"manifest_changed"`
	PublishableManifest bool                 `yaml:"publishable_manifest,omitempty"`
	ReviewItems         int                  `yaml:"review_items"`
	MigrationReportPath string               `yaml:"migration_report_path,omitempty"`
	Verifications       []VerificationResult `yaml:"verifications,omitempty"`
	Status              string               `yaml:"status"`
}

// VerificationResult is deliberately compact: command output remains in the
// process terminal while this index gives reviewers and agents the result.
type VerificationResult struct {
	Command string `yaml:"command"`
	Passed  bool   `yaml:"passed"`
	Detail  string `yaml:"detail,omitempty"`
}

// RemoteCheck is the required-check state reported by GitHub before --merge
// performs any merge. WB records it so a campaign report explains a block.
type RemoteCheck struct {
	Name   string `yaml:"name"`
	Bucket string `yaml:"bucket"`
}

type campaign struct {
	spec     Spec
	options  CampaignOptions
	report   CampaignReport
	modules  map[string]*campaignModule
	repos    []*campaignRepository
	order    []string
	children map[string][]string
}

type campaignModule struct {
	path       string
	version    string
	ref        string
	repository string
	migrate    bool
	root       string
	report     *CampaignModuleReport
}

type campaignRepository struct {
	repository string
	owner      string
	name       string
	canonical  string
	worktree   string
	branch     string
	ref        string
	cloneURL   string
	resume     bool
	modules    []*campaignModule
	report     *CampaignRepositoryReport
}

type cycleBootstrap struct {
	repositories []*campaignRepository
	modulePaths  map[string]bool
}

type listedModule struct {
	Path    string
	Version string
	Main    bool
	GoMod   string
}

type goListModule struct {
	Path    string
	Version string
	Main    bool
	GoMod   string
	Replace *goListModule
}

// RunCampaign plans or applies a hierarchy-aware Go migration. On apply it
// clones missing repositories into <github-dir>/<org>/<repo>, creates one
// dedicated worktree per repository, and modifies only those worktrees.
func RunCampaign(spec Spec, sourceRoot string, options CampaignOptions) (CampaignReport, error) {
	if err := spec.Validate(); err != nil {
		return CampaignReport{}, err
	}
	options, err := normalizeCampaignOptions(options)
	if err != nil {
		return CampaignReport{}, err
	}
	if options.Apply {
		lock, err := acquireCampaignLock(options.GitHubDir, spec.ID)
		if err != nil {
			return CampaignReport{}, err
		}
		defer lock.release()
	}
	c, err := planCampaign(spec, sourceRoot, options)
	if err != nil {
		return CampaignReport{}, err
	}
	if !options.Apply {
		return c.report, nil
	}
	if err := c.apply(); err != nil {
		c.report.Status = "failed"
		return c.report, err
	}
	c.report.Status = "applied"
	return c.report, nil
}

func normalizeCampaignOptions(options CampaignOptions) (CampaignOptions, error) {
	if strings.TrimSpace(options.GitHubDir) == "" {
		return CampaignOptions{}, fmt.Errorf("github directory is required")
	}
	abs, err := filepath.Abs(options.GitHubDir)
	if err != nil {
		return CampaignOptions{}, err
	}
	options.GitHubDir = abs
	if strings.TrimSpace(options.Ref) == "" {
		options.Ref = "main"
	}
	if options.ModuleRefs == nil {
		options.ModuleRefs = map[string]string{}
	}
	if options.Parallel == 0 {
		options.Parallel = 1
	}
	if options.Parallel < 1 {
		return CampaignOptions{}, fmt.Errorf("parallelism must be at least 1")
	}
	if options.Push {
		options.Commit = true
	}
	if options.PR {
		options.Push = true
		options.Commit = true
	}
	if options.Merge {
		options.PR = true
		options.Push = true
		options.Commit = true
	}
	if (options.Commit || options.Push || options.PR || options.Merge) && !options.Apply {
		return CampaignOptions{}, fmt.Errorf("--commit, --push, --pr, and --merge require --apply")
	}
	if options.Resume && !options.Apply {
		return CampaignOptions{}, fmt.Errorf("--resume requires --apply")
	}
	if options.Verify == "" {
		options.Verify = VerifyFull
	}
	switch options.Verify {
	case VerifyNone, VerifyCompile, VerifyTest, VerifyFull:
	default:
		return CampaignOptions{}, fmt.Errorf("unknown verification mode %q (want none, compile, test, or full)", options.Verify)
	}
	return options, nil
}

func planCampaign(spec Spec, sourceRoot string, options CampaignOptions) (*campaign, error) {
	sourceRoot, err := filepath.Abs(sourceRoot)
	if err != nil {
		return nil, err
	}
	discoveryRoot, err := campaignDiscoveryRoot(spec, sourceRoot, options)
	if err != nil {
		return nil, err
	}
	listed, children, err := inspectGoModuleGraph(discoveryRoot, options.Resume && options.Apply)
	if err != nil {
		return nil, err
	}
	targets := migrationTargetModules(spec, listed)
	if len(targets) == 0 {
		return nil, fmt.Errorf("migration does not reference a Go module in %s", sourceRoot)
	}
	affected := reverseClosure(targets, children)
	for target := range targets {
		affected[target] = true
	}
	requirementModules := map[string]bool{}
	for _, requirement := range spec.GoModuleRequires {
		if _, ok := listed[requirement.Path]; !ok {
			listed[requirement.Path] = listedModule{Path: requirement.Path, Version: requirement.Version}
		}
		affected[requirement.Path] = true
		requirementModules[requirement.Path] = true
	}

	order := dependencyOrder(affected, children)
	c := &campaign{
		spec:     spec,
		options:  options,
		modules:  map[string]*campaignModule{},
		order:    order,
		children: children,
		report: CampaignReport{
			SchemaVersion: 1,
			Migration:     ReportMigration{ID: spec.ID, Title: spec.Title, Format: spec.Format},
			Status:        "planned",
			SourceRoot:    sourceRoot,
			GitHubDir:     options.GitHubDir,
			BaseRef:       options.Ref,
			Verification:  options.Verify,
			Parallel:      options.Parallel,
		},
	}
	repositories := map[string]*campaignRepository{}
	for _, modulePath := range order {
		module := listed[modulePath]
		owner, name, repository, err := githubRepository(modulePath)
		if err != nil {
			return nil, err
		}
		ref := options.Ref
		if configuredRef := strings.TrimSpace(options.ModuleRefs[modulePath]); configuredRef != "" {
			ref = configuredRef
		}
		repo := repositories[repository]
		if repo == nil {
			worktree := filepath.Join(options.GitHubDir, ".wb", "worktrees", slug(spec.ID), owner, name)
			repo = &campaignRepository{
				repository: repository,
				owner:      owner,
				name:       name,
				canonical:  filepath.Join(options.GitHubDir, owner, name),
				worktree:   worktree,
				branch:     "wb/migrate/" + slug(spec.ID),
				ref:        ref,
				resume:     options.Resume,
			}
			repo.cloneURL = "https://" + repository + ".git"
			if options.CloneURL != nil {
				repo.cloneURL = options.CloneURL(repository)
			}
			repo.report = &CampaignRepositoryReport{
				Repository: repository, CanonicalDir: repo.canonical, WorktreeDir: repo.worktree,
				Branch: repo.branch, Ref: ref, Actions: []string{"fetch origin", "create isolated worktree"},
			}
			repositories[repository] = repo
			c.repos = append(c.repos, repo)
		} else if repo.ref != ref {
			return nil, fmt.Errorf("repository %s needs both refs %q and %q; use one ref per repository", repository, repo.ref, ref)
		}
		entry := &campaignModule{
			path: modulePath, version: module.Version, ref: ref, repository: repository,
			migrate: !targets[modulePath] && !requirementModules[modulePath],
		}
		entry.report = &CampaignModuleReport{Path: modulePath, MigrationEnabled: entry.migrate}
		if entry.migrate {
			entry.report.Status = "planned"
			entry.report.PlanState = "deferred"
		} else {
			entry.report.Status = "provided"
			entry.report.PlanState = "not_applicable"
		}
		c.modules[modulePath] = entry
		repo.modules = append(repo.modules, entry)
		repo.report.Modules = append(repo.report.Modules, *entry.report)
	}
	for _, repo := range c.repos {
		if _, err := os.Stat(repo.canonical); os.IsNotExist(err) {
			repo.report.Actions = append([]string{"clone " + repo.repository}, repo.report.Actions...)
		}
		c.report.Repositories = append(c.report.Repositories, *repo.report)
	}
	sort.Slice(c.report.Repositories, func(i, j int) bool { return c.report.Repositories[i].Repository < c.report.Repositories[j].Repository })
	return c, nil
}

// campaignDiscoveryRoot lets a resumed campaign evolve after manual fixes or
// prerequisite branches add dependencies. The original source root remains
// the report identity, while graph inspection uses the validated campaign
// worktree when it already exists.
func campaignDiscoveryRoot(spec Spec, sourceRoot string, options CampaignOptions) (string, error) {
	if !options.Resume {
		return sourceRoot, nil
	}
	parsed, err := parseGoMod(filepath.Join(sourceRoot, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read source module for resume discovery: %w", err)
	}
	if parsed.Module == nil || parsed.Module.Mod.Path == "" {
		return "", fmt.Errorf("source root %s has no module path", sourceRoot)
	}
	owner, name, repository, err := githubRepository(parsed.Module.Mod.Path)
	if err != nil {
		return "", err
	}
	worktree := filepath.Join(options.GitHubDir, ".wb", "worktrees", slug(spec.ID), owner, name)
	if _, err := os.Stat(worktree); os.IsNotExist(err) {
		return sourceRoot, nil
	} else if err != nil {
		return "", err
	}
	expectedBranch := "wb/migrate/" + slug(spec.ID)
	branch, err := runIn(worktree, "git", "branch", "--show-current")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(branch) != expectedBranch {
		return "", fmt.Errorf("cannot discover resumed %s from branch %q, want %q", repository, strings.TrimSpace(branch), expectedBranch)
	}
	root, err := findModuleRoot(worktree, parsed.Module.Mod.Path)
	if err != nil {
		return "", fmt.Errorf("locate resumed source module: %w", err)
	}
	return root, nil
}

func (c *campaign) apply() error {
	defer c.syncReport()
	if err := runRepositoriesParallel(c.repos, c.options.Parallel, prepareCampaignRepository); err != nil {
		return err
	}
	moduleRoots := map[string]string{}
	for _, repo := range c.repos {
		for _, module := range repo.modules {
			root, err := findModuleRoot(repo.worktree, module.path)
			if err != nil {
				return fmt.Errorf("%s: %w", module.path, err)
			}
			module.root = root
			moduleRoots[module.path] = root
		}
	}
	componentLayers, err := c.repositoryComponentLayers()
	if err != nil {
		return err
	}
	layers := flattenRepositoryComponentLayers(componentLayers)
	var localVerificationErrors []error
	for _, componentLayer := range componentLayers {
		layer := flattenRepositoryComponents(componentLayer)
		var preflightErrors []error
		var cycleBootstraps []cycleBootstrap
		// Preflight only the layer that is about to be changed. Earlier ready
		// layers can then be published while a dependent layer waits for their
		// release tags. Independent ready components in the same layer also
		// proceed, while blocked components remain untouched for --resume.
		if c.options.PR {
			layer, cycleBootstraps, preflightErrors = readyRepositoryComponents(componentLayer, func(
				repo *campaignRepository,
				allowedUnreleased map[string]bool,
			) (map[string]bool, error) {
				return c.preflightRepository(repo, moduleRoots, allowedUnreleased)
			})
			if len(layer) == 0 {
				return errors.Join(preflightErrors...)
			}
		}
		if err := runRepositoriesParallel(layer, c.options.Parallel, func(repo *campaignRepository) error {
			return c.applyRepositorySources(repo)
		}); err != nil {
			return err
		}
		if err := runRepositoriesParallel(layer, c.options.Parallel, func(repo *campaignRepository) error {
			return c.updateRepositoryManifests(repo, moduleRoots)
		}); err != nil {
			return err
		}
		verificationErrors := runRepositoriesParallelErrors(layer, c.options.Parallel, func(repo *campaignRepository) error {
			return c.verifyRepository(repo, false)
		})
		if len(verificationErrors) > 0 {
			if c.options.Commit {
				return errors.Join(verificationErrors...)
			}
			localVerificationErrors = append(localVerificationErrors, verificationErrors...)
		}
		if c.options.PR {
			bootstrapVersions := map[string]string{}
			for _, bootstrap := range cycleBootstraps {
				versions, err := c.seedCycleComponent(bootstrap)
				if err != nil {
					return err
				}
				for path, version := range versions {
					bootstrapVersions[path] = version
				}
			}
			if err := runRepositoriesParallel(layer, c.options.Parallel, func(repo *campaignRepository) error {
				return c.finalizeRepositoryManifests(repo, moduleRoots, bootstrapVersions)
			}); err != nil {
				return err
			}
			if err := runRepositoriesParallel(layer, c.options.Parallel, func(repo *campaignRepository) error {
				return c.verifyRepository(repo, true)
			}); err != nil {
				return err
			}
		}
		if c.options.Commit {
			if err := runRepositoriesParallel(layer, c.options.Parallel, c.commitAndPublishRepository); err != nil {
				return err
			}
		}
		if len(cycleBootstraps) > 0 {
			bootstrapError := fmt.Errorf(
				"published %d cyclic repository component(s) with seed pseudo-versions; merge their PRs, publish releases, and add go_module_release entries before --resume",
				len(cycleBootstraps),
			)
			return errors.Join(append([]error{bootstrapError}, preflightErrors...)...)
		}
		if len(preflightErrors) > 0 {
			return errors.Join(preflightErrors...)
		}
	}
	if len(localVerificationErrors) > 0 {
		return errors.Join(localVerificationErrors...)
	}
	if c.options.Merge {
		// Preflight every PR before merging any of them. A pending or failing
		// check therefore leaves the entire campaign reviewable and unmerged.
		for _, repo := range orderedRepositories(layers) {
			if repo.report.PR == "" {
				continue
			}
			checks, ready, err := requiredChecksGreen(repo.worktree, repo.report.PR)
			repo.report.RequiredChecks = checks
			if err != nil {
				return err
			}
			if !ready {
				return fmt.Errorf("required checks are not successful for %s", repo.report.PR)
			}
		}
		for _, repo := range orderedRepositories(layers) {
			if repo.report.PR == "" {
				continue
			}
			if _, err := runIn(repo.worktree, "gh", "pr", "merge", repo.report.PR, "--merge"); err != nil {
				return err
			}
			repo.report.Merged = true
		}
	}
	c.syncReport()
	return nil
}

func (c *campaign) preflightRepository(
	repo *campaignRepository,
	moduleRoots map[string]string,
	allowedUnreleased map[string]bool,
) (map[string]bool, error) {
	bootstrap := map[string]bool{}
	for _, module := range repo.modules {
		if !module.migrate {
			continue
		}
		dependencies, err := preflightPublishedReleases(module.root, c.spec, module.path, moduleRoots, allowedUnreleased)
		if err != nil {
			return nil, fmt.Errorf("make go.mod publishable for %s: %w", module.path, err)
		}
		for dependency := range dependencies {
			bootstrap[dependency] = true
		}
	}
	return bootstrap, nil
}

// applyRepositorySources is a distinct campaign phase. Every repository in a
// dependency layer finishes source rewriting before any peer normalizes its
// manifest, so cyclic modules never observe a half-rewritten dependency.
func (c *campaign) applyRepositorySources(repo *campaignRepository) error {
	for _, modulePath := range c.order {
		module := c.modules[modulePath]
		if module.repository != repo.repository {
			continue
		}
		if !module.migrate {
			module.report.Status = "provided"
			continue
		}
		plan, err := BuildPlan(c.spec, module.root)
		if err != nil {
			return fmt.Errorf("plan %s: %w", module.path, err)
		}
		if err := Apply(plan); err != nil {
			return fmt.Errorf("apply %s: %w", module.path, err)
		}
		changedFiles := len(plan.Changes)
		module.report.ChangedFiles = &changedFiles
		module.report.PlanState = "complete"
		module.report.ReviewItems = len(plan.Findings)
		module.report.Status = "applied"
		if c.options.ReportDir != "" {
			dir := filepath.Join(c.options.ReportDir, "modules", slug(module.path))
			if err := WriteReports(dir, NewReport(c.spec, plan, []string{module.root}, "applied")); err != nil {
				return err
			}
			module.report.MigrationReportPath = dir
		}
	}
	return nil
}

func (c *campaign) updateRepositoryManifests(repo *campaignRepository, moduleRoots map[string]string) error {
	for _, module := range repo.modules {
		if !module.migrate {
			continue
		}
		changed, err := updateGoModule(module.root, c.spec, module.path, moduleRoots)
		if err != nil {
			return fmt.Errorf("update go.mod for %s: %w", module.path, err)
		}
		module.report.ManifestChanged = changed
	}
	return refreshRepositoryChangeIndex(repo)
}

// refreshRepositoryChangeIndex records the complete review surface relative
// to the campaign's base ref. This deliberately differs from a module plan's
// ChangedFiles count: on --resume the latest mechanical pass can be
// idempotent while the campaign branch still contains all earlier edits and
// manual fixes that a reviewer must inspect.
func refreshRepositoryChangeIndex(repo *campaignRepository) error {
	base := "origin/" + repo.ref
	tracked, err := runIn(repo.worktree, "git", "diff", "--name-only", "-z", base)
	if err != nil {
		return fmt.Errorf("index changed files for %s: %w", repo.repository, err)
	}
	untracked, err := runIn(repo.worktree, "git", "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return fmt.Errorf("index untracked files for %s: %w", repo.repository, err)
	}
	seen := map[string]bool{}
	files := make([]string, 0)
	for _, path := range append(splitNUL(tracked), splitNUL(untracked)...) {
		path = filepath.ToSlash(path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		files = append(files, path)
	}
	sort.Strings(files)
	repo.report.ChangedFiles = &files
	return nil
}

func splitNUL(value string) []string {
	return strings.Split(strings.TrimSuffix(value, "\x00"), "\x00")
}

func (c *campaign) finalizeRepositoryManifests(
	repo *campaignRepository,
	moduleRoots map[string]string,
	releaseOverrides map[string]string,
) error {
	for _, module := range repo.modules {
		if !module.migrate {
			continue
		}
		changed, err := finalizeGoModule(module.root, c.spec, module.path, moduleRoots, releaseOverrides)
		if err != nil {
			return fmt.Errorf("make go.mod publishable for %s: %w", module.path, err)
		}
		module.report.PublishableManifest = changed
	}
	return nil
}

// seedCycleComponent publishes an intermediate commit for each repository in
// a strongly connected component, then derives valid Go pseudo-versions for
// the exact module commits needed by cyclic peers. Replace directives in a
// dependency module are ignored by Go, so the final review commits can safely
// depend on these seeds while removing every local path from their own go.mod.
func (c *campaign) seedCycleComponent(bootstrap cycleBootstrap) (map[string]string, error) {
	heads := make(map[string]string, len(bootstrap.repositories))
	for _, repo := range bootstrap.repositories {
		if !repo.hasMigratingModules() {
			continue
		}
		changed, err := worktreeChanged(repo.worktree)
		if err != nil {
			return nil, err
		}
		if changed {
			if _, err := runIn(repo.worktree, "git", "add", "-A"); err != nil {
				return nil, err
			}
			if _, err := runIn(repo.worktree, "git", "commit", "-m", campaignChangeTitle(c.spec)+" (cycle seed)"); err != nil {
				return nil, err
			}
		}
		head, err := runIn(repo.worktree, "git", "rev-parse", "HEAD")
		if err != nil {
			return nil, err
		}
		head = strings.TrimSpace(head)
		heads[repo.repository] = head
		if _, err := runIn(repo.worktree, "git", "push", "-u", "origin", repo.branch); err != nil {
			return nil, err
		}
		repo.report.Pushed = true
	}

	versions := make(map[string]string, len(bootstrap.modulePaths))
	for modulePath := range bootstrap.modulePaths {
		module := c.modules[modulePath]
		if module == nil {
			return nil, fmt.Errorf("cannot seed unknown cyclic module %s", modulePath)
		}
		head := heads[module.repository]
		if head == "" {
			return nil, fmt.Errorf("cannot seed cyclic module %s without a migrating repository commit", modulePath)
		}
		repo := c.repositoryByName(module.repository)
		if repo == nil {
			return nil, fmt.Errorf("cannot find repository %s for cyclic module %s", module.repository, modulePath)
		}
		version, err := pseudoVersionForCommit(repo.worktree, module.path, module.version, head)
		if err != nil {
			return nil, err
		}
		versions[module.path] = version
	}
	return versions, nil
}

func (c *campaign) repositoryByName(repository string) *campaignRepository {
	for _, repo := range c.repos {
		if repo.repository == repository {
			return repo
		}
	}
	return nil
}

func pseudoVersionForCommit(worktree, modulePath, olderVersion, revision string) (string, error) {
	if len(revision) < 12 {
		return "", fmt.Errorf("revision for %s is too short: %q", modulePath, revision)
	}
	timestamp, err := runIn(worktree, "git", "show", "-s", "--format=%cI", revision)
	if err != nil {
		return "", err
	}
	commitTime, err := time.Parse(time.RFC3339, strings.TrimSpace(timestamp))
	if err != nil {
		return "", fmt.Errorf("parse commit time for %s: %w", modulePath, err)
	}
	if modmodule.IsPseudoVersion(olderVersion) {
		olderVersion, err = modmodule.PseudoVersionBase(olderVersion)
		if err != nil {
			return "", fmt.Errorf("find pseudo-version base for %s: %w", modulePath, err)
		}
	}
	if olderVersion != "" && !semver.IsValid(olderVersion) {
		return "", fmt.Errorf("cannot seed %s from invalid version %q", modulePath, olderVersion)
	}
	major := semver.Major(olderVersion)
	if major == "" {
		major = "v0"
		_, pathMajor, ok := modmodule.SplitPathVersion(modulePath)
		if !ok {
			return "", fmt.Errorf("cannot determine module path major for %s", modulePath)
		}
		if strings.HasPrefix(pathMajor, "/v") {
			major = strings.TrimPrefix(pathMajor, "/")
		} else if strings.HasPrefix(pathMajor, ".v") {
			major = strings.TrimPrefix(pathMajor, ".")
		}
	}
	version := modmodule.PseudoVersion(major, olderVersion, commitTime.UTC(), revision[:12])
	if err := modmodule.Check(modulePath, version); err != nil {
		return "", fmt.Errorf("validate seed pseudo-version for %s: %w", modulePath, err)
	}
	return version, nil
}

// commitAndPublishRepository runs only after every repository in the current
// dependency layer has completed source, manifest, and verification phases.
// GitHub CI can then run while later consumer layers continue locally.
func (c *campaign) commitAndPublishRepository(repo *campaignRepository) error {
	if !repo.hasMigratingModules() {
		return nil
	}
	changed, err := worktreeChanged(repo.worktree)
	if err != nil {
		return err
	}
	if !changed {
		if !c.options.Resume {
			return nil
		}
		ahead, err := runIn(repo.worktree, "git", "rev-list", "origin/"+repo.ref+"..HEAD")
		if err != nil {
			return err
		}
		if strings.TrimSpace(ahead) == "" {
			return nil
		}
		head, err := runIn(repo.worktree, "git", "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		repo.report.Commit = strings.TrimSpace(head)
		return c.publishRepository(repo)
	}
	if _, err := runIn(repo.worktree, "git", "add", "-A"); err != nil {
		return err
	}
	if _, err := runIn(repo.worktree, "git", "commit", "-m", campaignChangeTitle(c.spec)); err != nil {
		return err
	}
	head, err := runIn(repo.worktree, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	repo.report.Commit = strings.TrimSpace(head)
	return c.publishRepository(repo)
}

func (repo *campaignRepository) hasMigratingModules() bool {
	for _, module := range repo.modules {
		if module.migrate {
			return true
		}
	}
	return false
}

func (c *campaign) publishRepository(repo *campaignRepository) error {
	if c.options.Push {
		if _, err := runIn(repo.worktree, "git", "push", "-u", "origin", repo.branch); err != nil {
			return err
		}
		repo.report.Pushed = true
	}
	if c.options.PR {
		prURL, err := openCampaignPR(repo, c.spec)
		if err != nil {
			return err
		}
		repo.report.PR = prURL
	}
	return nil
}

func (c *campaign) verifyRepository(repo *campaignRepository, publishableOnly bool) error {
	if c.options.Verify == VerifyNone {
		return nil
	}
	for _, module := range repo.modules {
		if !module.migrate {
			continue
		}
		moduleDirty, err := worktreeChanged(module.root)
		if err != nil {
			return err
		}
		changed := moduleDirty || module.report.ChangedFiles != nil && *module.report.ChangedFiles > 0
		if (!publishableOnly && !changed && !module.report.ManifestChanged) ||
			(publishableOnly && !module.report.PublishableManifest) {
			continue
		}
		results := verifyGoModule(module.root, c.options.Verify)
		module.report.Verifications = append(module.report.Verifications, results...)
		for _, result := range results {
			if !result.Passed {
				return fmt.Errorf("verification failed for %s: %s", module.path, result.Command)
			}
		}
	}
	return nil
}

func (c *campaign) repositoryLayers() ([][]*campaignRepository, error) {
	componentLayers, err := c.repositoryComponentLayers()
	if err != nil {
		return nil, err
	}
	return flattenRepositoryComponentLayers(componentLayers), nil
}

func (c *campaign) repositoryComponentLayers() ([][][]*campaignRepository, error) {
	repositories := map[string]*campaignRepository{}
	for _, repo := range c.repos {
		repositories[repo.repository] = repo
	}
	dependencies := map[string]map[string]bool{}
	for parent, children := range c.children {
		parentModule := c.modules[parent]
		if parentModule == nil {
			continue
		}
		for _, child := range children {
			childModule := c.modules[child]
			if childModule == nil || parentModule.repository == childModule.repository {
				continue
			}
			if dependencies[parentModule.repository] == nil {
				dependencies[parentModule.repository] = map[string]bool{}
			}
			dependencies[parentModule.repository][childModule.repository] = true
		}
	}
	componentByRepository, componentCount := repositoryComponents(repositories, dependencies)
	componentDependencies := make(map[int]map[int]bool, componentCount)
	for repository, repositoryDependencies := range dependencies {
		component, ok := componentByRepository[repository]
		if !ok {
			continue
		}
		for dependency := range repositoryDependencies {
			dependencyComponent, ok := componentByRepository[dependency]
			if !ok || component == dependencyComponent {
				continue
			}
			if componentDependencies[component] == nil {
				componentDependencies[component] = map[int]bool{}
			}
			componentDependencies[component][dependencyComponent] = true
		}
	}
	componentDepths := map[int]int{}
	var componentDepth func(int) int
	componentDepth = func(component int) int {
		if level, ok := componentDepths[component]; ok {
			return level
		}
		level := 0
		for dependency := range componentDependencies[component] {
			candidate := componentDepth(dependency)
			if candidate+1 > level {
				level = candidate + 1
			}
		}
		componentDepths[component] = level
		return level
	}
	maxDepth := 0
	for component := range componentCount {
		value := componentDepth(component)
		if value > maxDepth {
			maxDepth = value
		}
	}
	components := make([][]*campaignRepository, componentCount)
	for repository, repo := range repositories {
		component := componentByRepository[repository]
		components[component] = append(components[component], repo)
	}
	for _, component := range components {
		sort.Slice(component, func(i, j int) bool { return component[i].repository < component[j].repository })
	}
	layers := make([][][]*campaignRepository, maxDepth+1)
	for component, repositories := range components {
		depth := componentDepths[component]
		layers[depth] = append(layers[depth], repositories)
	}
	for _, layer := range layers {
		sort.Slice(layer, func(i, j int) bool {
			return layer[i][0].repository < layer[j][0].repository
		})
	}
	return layers, nil
}

func flattenRepositoryComponentLayers(componentLayers [][][]*campaignRepository) [][]*campaignRepository {
	layers := make([][]*campaignRepository, 0, len(componentLayers))
	for _, componentLayer := range componentLayers {
		layer := flattenRepositoryComponents(componentLayer)
		sort.Slice(layer, func(i, j int) bool { return layer[i].repository < layer[j].repository })
		layers = append(layers, layer)
	}
	return layers
}

func flattenRepositoryComponents(components [][]*campaignRepository) []*campaignRepository {
	var repositories []*campaignRepository
	for _, component := range components {
		repositories = append(repositories, component...)
	}
	return repositories
}

// repositoryComponents collapses cyclic repository dependencies into strongly
// connected components. Go modules may legitimately form cycles; every
// campaign worktree is prepared before processing starts, so repositories in
// one component can be migrated and verified in the same dependency layer.
func repositoryComponents(
	repositories map[string]*campaignRepository,
	dependencies map[string]map[string]bool,
) (componentByRepository map[string]int, componentCount int) {
	componentByRepository = make(map[string]int, len(repositories))
	indices := make(map[string]int, len(repositories))
	lowLinks := make(map[string]int, len(repositories))
	onStack := make(map[string]bool, len(repositories))
	stack := make([]string, 0, len(repositories))
	nextIndex := 0

	var visit func(string)
	visit = func(repository string) {
		indices[repository] = nextIndex
		lowLinks[repository] = nextIndex
		nextIndex++
		stack = append(stack, repository)
		onStack[repository] = true

		dependencyNames := make([]string, 0, len(dependencies[repository]))
		for dependency := range dependencies[repository] {
			if _, ok := repositories[dependency]; ok {
				dependencyNames = append(dependencyNames, dependency)
			}
		}
		sort.Strings(dependencyNames)
		for _, dependency := range dependencyNames {
			dependencyIndex, visited := indices[dependency]
			if !visited {
				visit(dependency)
				if lowLinks[dependency] < lowLinks[repository] {
					lowLinks[repository] = lowLinks[dependency]
				}
			} else if onStack[dependency] && dependencyIndex < lowLinks[repository] {
				lowLinks[repository] = dependencyIndex
			}
		}

		if lowLinks[repository] != indices[repository] {
			return
		}
		for {
			last := len(stack) - 1
			member := stack[last]
			stack = stack[:last]
			onStack[member] = false
			componentByRepository[member] = componentCount
			if member == repository {
				break
			}
		}
		componentCount++
	}

	repositoryNames := make([]string, 0, len(repositories))
	for repository := range repositories {
		repositoryNames = append(repositoryNames, repository)
	}
	sort.Strings(repositoryNames)
	for _, repository := range repositoryNames {
		if _, visited := indices[repository]; !visited {
			visit(repository)
		}
	}
	return componentByRepository, componentCount
}

func runRepositoriesParallel(repositories []*campaignRepository, parallel int, action func(*campaignRepository) error) error {
	errors := runRepositoriesParallelErrors(repositories, parallel, action)
	if len(errors) > 0 {
		return errors[0]
	}
	return nil
}

// readyRepositoryComponents preflights independent strongly connected
// components atomically. Missing releases inside a cycle become an explicit
// seed bootstrap; every other preflight error blocks the whole component.
func readyRepositoryComponents(
	components [][]*campaignRepository,
	preflight func(*campaignRepository, map[string]bool) (map[string]bool, error),
) (ready []*campaignRepository, bootstraps []cycleBootstrap, blocked []error) {
	for _, component := range components {
		allowedUnreleased := map[string]bool{}
		if len(component) > 1 {
			for _, repo := range component {
				for _, module := range repo.modules {
					if module.migrate {
						allowedUnreleased[module.path] = true
					}
				}
			}
		}
		bootstrapPaths := map[string]bool{}
		var errs []error
		for _, repo := range component {
			paths, err := preflight(repo, allowedUnreleased)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for path := range paths {
				bootstrapPaths[path] = true
			}
		}
		if len(errs) > 0 {
			blocked = append(blocked, errs...)
			continue
		}
		ready = append(ready, component...)
		if len(bootstrapPaths) > 0 {
			bootstraps = append(bootstraps, cycleBootstrap{
				repositories: component,
				modulePaths:  bootstrapPaths,
			})
		}
	}
	return ready, bootstraps, blocked
}

// runRepositoriesParallelErrors runs every repository and preserves input
// order in its result, regardless of goroutine completion order.
func runRepositoriesParallelErrors(repositories []*campaignRepository, parallel int, action func(*campaignRepository) error) []error {
	if len(repositories) == 0 {
		return nil
	}
	workers := parallel
	if workers > len(repositories) {
		workers = len(repositories)
	}
	jobs := make(chan int)
	results := make([]error, len(repositories))
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				results[index] = action(repositories[index])
			}
		}()
	}
	for index := range repositories {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	errors := make([]error, 0, len(results))
	for _, err := range results {
		if err != nil {
			errors = append(errors, err)
		}
	}
	return errors
}

func orderedRepositories(layers [][]*campaignRepository) []*campaignRepository {
	var ordered []*campaignRepository
	for _, layer := range layers {
		ordered = append(ordered, layer...)
	}
	return ordered
}

func (c *campaign) syncReport() {
	for reportIndex := range c.report.Repositories {
		reportRepo := &c.report.Repositories[reportIndex]
		for _, repo := range c.repos {
			if reportRepo.Repository != repo.repository {
				continue
			}
			reportRepo.Actions = append([]string(nil), repo.report.Actions...)
			if repo.report.ChangedFiles == nil {
				reportRepo.ChangedFiles = nil
			} else {
				files := append([]string(nil), (*repo.report.ChangedFiles)...)
				reportRepo.ChangedFiles = &files
			}
			reportRepo.Commit = repo.report.Commit
			reportRepo.Pushed = repo.report.Pushed
			reportRepo.PR = repo.report.PR
			reportRepo.RequiredChecks = append([]RemoteCheck(nil), repo.report.RequiredChecks...)
			reportRepo.Merged = repo.report.Merged
			reportRepo.Modules = reportRepo.Modules[:0]
			for _, module := range repo.modules {
				reportRepo.Modules = append(reportRepo.Modules, *module.report)
			}
		}
	}
}

func prepareCampaignRepository(repo *campaignRepository) error {
	if _, err := os.Stat(repo.canonical); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(repo.canonical), 0o755); err != nil {
			return err
		}
		if _, err := runIn(filepath.Dir(repo.canonical), "git", "clone", "--quiet", repo.cloneURL, repo.canonical); err != nil {
			return err
		}
	}
	if _, err := runIn(repo.canonical, "git", "fetch", "--quiet", "origin"); err != nil {
		return err
	}
	base := "origin/" + repo.ref
	if _, err := runIn(repo.canonical, "git", "rev-parse", "--verify", base+"^{commit}"); err != nil {
		return fmt.Errorf("%s does not contain %s: %w", repo.repository, base, err)
	}
	if _, err := os.Stat(repo.worktree); err == nil {
		if repo.resume {
			return validateResumeWorktree(repo)
		}
		return fmt.Errorf("campaign worktree already exists: %s (leave it intact or choose a different migration id)", repo.worktree)
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := runIn(repo.canonical, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+repo.branch); err == nil {
		return fmt.Errorf("campaign branch already exists in %s: %s", repo.canonical, repo.branch)
	}
	if err := os.MkdirAll(filepath.Dir(repo.worktree), 0o755); err != nil {
		return err
	}
	if _, err := runIn(repo.canonical, "git", "worktree", "add", "--quiet", "-b", repo.branch, repo.worktree, base); err != nil {
		return err
	}
	return nil
}

func validateResumeWorktree(repo *campaignRepository) error {
	branch, err := runIn(repo.worktree, "git", "branch", "--show-current")
	if err != nil {
		return err
	}
	if strings.TrimSpace(branch) != repo.branch {
		return fmt.Errorf("cannot resume %s: worktree branch is %q, want %q", repo.repository, strings.TrimSpace(branch), repo.branch)
	}
	return nil
}

type campaignLock struct {
	path string
}

func acquireCampaignLock(githubDir, migrationID string) (campaignLock, error) {
	dir := filepath.Join(githubDir, ".wb", "worktrees", slug(migrationID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return campaignLock{}, err
	}
	path := filepath.Join(dir, ".lock")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return campaignLock{}, fmt.Errorf("migration campaign %q is already running or was interrupted: remove %s only after confirming no WB process is active", migrationID, path)
		}
		return campaignLock{}, err
	}
	if _, err := fmt.Fprintf(file, "migration=%s\npid=%d\n", migrationID, os.Getpid()); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return campaignLock{}, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return campaignLock{}, err
	}
	return campaignLock{path: path}, nil
}

func (l campaignLock) release() {
	_ = os.Remove(l.path)
}

// CleanupCampaignWorktrees removes clean, dedicated worktrees for one
// migration. It never removes canonical clones, branches, reports, or a
// worktree with uncommitted changes.
func CleanupCampaignWorktrees(githubDir, migrationID string) ([]string, error) {
	root, err := filepath.Abs(filepath.Join(githubDir, ".wb", "worktrees", slug(migrationID)))
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(root, ".lock")); err == nil {
		return nil, fmt.Errorf("campaign %q is locked at %s", migrationID, root)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	worktrees := []string{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() == ".git" {
			worktrees = append(worktrees, filepath.Dir(path))
			if entry.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(worktrees, func(i, j int) bool { return len(worktrees[i]) > len(worktrees[j]) })
	for _, worktree := range worktrees {
		changed, err := worktreeChanged(worktree)
		if err != nil {
			return nil, err
		}
		if changed {
			return nil, fmt.Errorf("refusing to clean dirty campaign worktree: %s", worktree)
		}
		if _, err := runIn(worktree, "git", "worktree", "remove", worktree); err != nil {
			return nil, err
		}
	}
	return worktrees, nil
}

func inspectGoModuleGraph(root string, repairManifest bool) (map[string]listedModule, map[string][]string, error) {
	args := []string{"list"}
	if repairManifest {
		// A prerequisite commit or manual partial fix may update go.mod before
		// go.sum. Resume is an apply-only operation, so let official Go tooling
		// reconcile that metadata before graph discovery.
		args = append(args, "-mod=mod")
	}
	args = append(args, "-m", "-json", "all")
	output, err := runIn(root, "go", args...)
	if err != nil {
		return nil, nil, err
	}
	modules := map[string]listedModule{}
	decoder := json.NewDecoder(strings.NewReader(output))
	for {
		var module goListModule
		if err := decoder.Decode(&module); err != nil {
			if err == io.EOF {
				break
			}
			return nil, nil, fmt.Errorf("decode go module list: %w", err)
		}
		goMod := module.GoMod
		if module.Replace != nil && module.Replace.GoMod != "" {
			goMod = module.Replace.GoMod
		}
		modules[module.Path] = listedModule{Path: module.Path, Version: module.Version, Main: module.Main, GoMod: goMod}
	}
	if err := populateGoModPaths(root, modules); err != nil {
		return nil, nil, err
	}
	graph, err := runIn(root, "go", "mod", "graph")
	if err != nil {
		return nil, nil, err
	}
	childSets := map[string]map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(graph), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		parent, child := moduleToken(fields[0]), moduleToken(fields[1])
		if _, ok := modules[parent]; !ok {
			continue
		}
		if _, ok := modules[child]; !ok {
			continue
		}
		if childSets[parent] == nil {
			childSets[parent] = map[string]bool{}
		}
		childSets[parent][child] = true
	}
	if err := addGoModRequirementEdges(modules, childSets); err != nil {
		return nil, nil, err
	}
	children := make(map[string][]string, len(childSets))
	for module, dependencies := range childSets {
		for dependency := range dependencies {
			children[module] = append(children[module], dependency)
		}
		sort.Strings(children[module])
	}
	return modules, children, nil
}

func populateGoModPaths(root string, modules map[string]listedModule) error {
	moduleCache, err := runIn(root, "go", "env", "GOMODCACHE")
	if err != nil {
		return err
	}
	moduleCache = strings.TrimSpace(moduleCache)
	paths := make([]string, 0, len(modules))
	for modulePath := range modules {
		paths = append(paths, modulePath)
	}
	sort.Strings(paths)
	for _, modulePath := range paths {
		module := modules[modulePath]
		if module.GoMod != "" || module.Version == "" {
			continue
		}
		candidate, candidateErr := cachedGoModPath(moduleCache, module.Path, module.Version)
		if candidateErr == nil {
			if _, statErr := os.Stat(candidate); statErr == nil {
				module.GoMod = candidate
				modules[modulePath] = module
				continue
			}
		}
		output, listErr := runIn(root, "go", "list", "-m", "-json", module.Path+"@"+module.Version)
		if listErr != nil {
			return fmt.Errorf("resolve go.mod for %s@%s: %w", module.Path, module.Version, listErr)
		}
		var resolved goListModule
		if err := json.Unmarshal([]byte(output), &resolved); err != nil {
			return fmt.Errorf("decode module metadata for %s@%s: %w", module.Path, module.Version, err)
		}
		module.GoMod = resolved.GoMod
		if resolved.Replace != nil && resolved.Replace.GoMod != "" {
			module.GoMod = resolved.Replace.GoMod
		}
		modules[modulePath] = module
	}
	return nil
}

func cachedGoModPath(moduleCache, modulePath, version string) (string, error) {
	escapedPath, err := modmodule.EscapePath(modulePath)
	if err != nil {
		return "", err
	}
	escapedVersion, err := modmodule.EscapeVersion(version)
	if err != nil {
		return "", err
	}
	return filepath.Join(moduleCache, "cache", "download", escapedPath, "@v", escapedVersion+".mod"), nil
}

// addGoModRequirementEdges restores direct requirement edges hidden by Go's
// pruned module graph. Reverse migration discovery needs to know that an
// adapter depends on a changed module even when `go mod graph` omits the
// adapter's outgoing edges from the root module's pruned view.
func addGoModRequirementEdges(modules map[string]listedModule, children map[string]map[string]bool) error {
	for modulePath, module := range modules {
		if module.GoMod == "" {
			continue
		}
		contents, err := os.ReadFile(module.GoMod)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read go.mod for %s: %w", modulePath, err)
		}
		parsed, err := modfile.ParseLax(module.GoMod, contents, nil)
		if err != nil {
			return fmt.Errorf("parse go.mod requirements for %s: %w", modulePath, err)
		}
		for _, requirement := range parsed.Require {
			dependency := requirement.Mod.Path
			if _, selected := modules[dependency]; !selected {
				continue
			}
			if children[modulePath] == nil {
				children[modulePath] = map[string]bool{}
			}
			children[modulePath][dependency] = true
		}
	}
	return nil
}

func migrationTargetModules(spec Spec, modules map[string]listedModule) map[string]bool {
	targets := map[string]bool{}
	for _, step := range spec.Steps {
		for _, candidate := range []string{step.From, step.Import} {
			if candidate == "" {
				continue
			}
			if module := longestModulePrefix(candidate, modules); module != "" {
				targets[module] = true
			}
		}
	}
	return targets
}

func longestModulePrefix(candidate string, modules map[string]listedModule) string {
	var match string
	for module := range modules {
		if candidate == module || strings.HasPrefix(candidate, module+"/") {
			if len(module) > len(match) {
				match = module
			}
		}
	}
	return match
}

func reverseClosure(targets map[string]bool, children map[string][]string) map[string]bool {
	parents := map[string][]string{}
	for parent, descendants := range children {
		for _, child := range descendants {
			parents[child] = append(parents[child], parent)
		}
	}
	closure := map[string]bool{}
	queue := make([]string, 0, len(targets))
	for target := range targets {
		queue = append(queue, target)
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if closure[current] {
			continue
		}
		closure[current] = true
		queue = append(queue, parents[current]...)
	}
	return closure
}

func dependencyOrder(included map[string]bool, children map[string][]string) []string {
	var (
		order   []string
		visited = map[string]bool{}
	)
	var visit func(string)
	visit = func(module string) {
		if visited[module] || !included[module] {
			return
		}
		visited[module] = true
		for _, child := range children[module] {
			visit(child)
		}
		order = append(order, module)
	}
	modules := make([]string, 0, len(included))
	for module := range included {
		modules = append(modules, module)
	}
	sort.Strings(modules)
	for _, module := range modules {
		visit(module)
	}
	return order
}

func githubRepository(modulePath string) (owner, name, repository string, err error) {
	parts := strings.Split(modulePath, "/")
	if len(parts) < 3 || parts[0] != "github.com" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("module %q is not a resolvable GitHub module", modulePath)
	}
	return parts[1], parts[2], strings.Join(parts[:3], "/"), nil
}

func moduleToken(value string) string {
	if at := strings.LastIndex(value, "@"); at > 0 {
		return value[:at]
	}
	return value
}

func findModuleRoot(repositoryRoot, modulePath string) (string, error) {
	var found string
	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "go.mod" {
			return nil
		}
		parsed, err := parseGoMod(path)
		if err != nil {
			return err
		}
		if parsed.Module != nil && parsed.Module.Mod.Path == modulePath {
			found = filepath.Dir(path)
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("no go.mod declares module %q below %s", modulePath, repositoryRoot)
	}
	return found, nil
}

func parseGoMod(path string) (*modfile.File, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return modfile.Parse(path, contents, nil)
}

func updateGoModule(moduleRoot string, spec Spec, modulePath string, moduleRoots map[string]string) (bool, error) {
	goMod := filepath.Join(moduleRoot, "go.mod")
	before, err := os.ReadFile(goMod)
	if err != nil {
		return false, err
	}
	parsed, err := modfile.Parse(goMod, before, nil)
	if err != nil {
		return false, err
	}
	direct := map[string]bool{}
	for _, requirement := range parsed.Require {
		direct[requirement.Mod.Path] = true
	}
	for _, requirement := range spec.GoModuleRequires {
		if _, err := runIn(moduleRoot, "go", "mod", "edit", "-require="+requirement.Path+"@"+requirement.Version); err != nil {
			return false, err
		}
		direct[requirement.Path] = true
	}
	for path, root := range moduleRoots {
		if path == modulePath || !direct[path] {
			continue
		}
		if err := replaceGoModule(moduleRoot, goMod, path, root); err != nil {
			return false, err
		}
	}
	if _, err := runIn(moduleRoot, "go", "mod", "tidy"); err != nil {
		return false, err
	}
	// Tidy removes unused requirements but intentionally retains their replace
	// directives. Drop only replacements that point at this campaign's known
	// worktrees and no longer have a requirement, keeping the local diff minimal.
	parsed, err = parseGoMod(goMod)
	if err != nil {
		return false, err
	}
	required := map[string]bool{}
	for _, requirement := range parsed.Require {
		required[requirement.Mod.Path] = true
	}
	for path, root := range moduleRoots {
		if path == modulePath || required[path] {
			continue
		}
		hasReplace, replaceErr := hasCampaignReplace(moduleRoot, parsed, path, root)
		if replaceErr != nil || !hasReplace {
			continue
		}
		if err := dropCampaignReplace(moduleRoot, parsed, path); err != nil {
			return false, err
		}
	}
	after, err := os.ReadFile(goMod)
	if err != nil {
		return false, err
	}
	return string(before) != string(after), nil
}

// finalizeGoModule removes only WB's temporary worktree replacements and
// pins their published counterparts before a campaign branch is pushed for
// review. A pull request with local paths in go.mod cannot be verified by CI,
// so an explicit release is required for every affected dependency.
func finalizeGoModule(
	moduleRoot string,
	spec Spec,
	modulePath string,
	moduleRoots map[string]string,
	releaseOverrides map[string]string,
) (bool, error) {
	goMod := filepath.Join(moduleRoot, "go.mod")
	before, err := os.ReadFile(goMod)
	if err != nil {
		return false, err
	}
	parsed, err := modfile.Parse(goMod, before, nil)
	if err != nil {
		return false, err
	}
	direct := map[string]bool{}
	for _, requirement := range parsed.Require {
		direct[requirement.Mod.Path] = true
	}
	releases := map[string]string{}
	for _, release := range spec.GoModuleReleases {
		releases[release.Path] = release.Version
	}
	for path, version := range releaseOverrides {
		releases[path] = version
	}
	for dependency, worktree := range moduleRoots {
		if dependency == modulePath || !direct[dependency] {
			continue
		}
		hasReplace, err := hasCampaignReplace(moduleRoot, parsed, dependency, worktree)
		if err != nil {
			return false, err
		}
		if !hasReplace {
			continue
		}
		version := releases[dependency]
		if version == "" {
			return false, fmt.Errorf("dependency %s uses a campaign worktree; add go_module_release %q before using --pr", dependency, dependency)
		}
		if err := dropCampaignReplace(moduleRoot, parsed, dependency); err != nil {
			return false, err
		}
		if _, err := runIn(moduleRoot, "go", "mod", "edit", "-require="+dependency+"@"+version); err != nil {
			return false, err
		}
	}
	afterEdit, err := os.ReadFile(goMod)
	if err != nil {
		return false, err
	}
	if string(before) != string(afterEdit) {
		if _, err := runIn(moduleRoot, "go", "mod", "tidy"); err != nil {
			return false, err
		}
	}
	after, err := os.ReadFile(goMod)
	if err != nil {
		return false, err
	}
	return string(before) != string(after), nil
}

// preflightPublishedReleases runs before applying source or manifest changes,
// so an incomplete --pr request never strands a campaign worktree dirty.
func preflightPublishedReleases(
	moduleRoot string,
	spec Spec,
	modulePath string,
	moduleRoots map[string]string,
	allowedUnreleased map[string]bool,
) (map[string]bool, error) {
	goMod := filepath.Join(moduleRoot, "go.mod")
	contents, err := os.ReadFile(goMod)
	if err != nil {
		return nil, err
	}
	parsed, err := modfile.Parse(goMod, contents, nil)
	if err != nil {
		return nil, err
	}
	direct := map[string]bool{}
	for _, requirement := range parsed.Require {
		direct[requirement.Mod.Path] = true
	}
	for _, requirement := range spec.GoModuleRequires {
		direct[requirement.Path] = true
	}
	releases := map[string]bool{}
	for _, release := range spec.GoModuleReleases {
		releases[release.Path] = true
	}
	for _, replacement := range parsed.Replace {
		if replacement.New.Version != "" {
			continue
		}
		campaignRoot, ok := moduleRoots[replacement.Old.Path]
		if !ok {
			return nil, fmt.Errorf(
				"dependency %s has local replacement %q; remove it manually before using --pr",
				replacement.Old.Path,
				replacement.New.Path,
			)
		}
		if _, err := hasCampaignReplace(moduleRoot, parsed, replacement.Old.Path, campaignRoot); err != nil {
			return nil, err
		}
	}
	bootstrap := map[string]bool{}
	for dependency := range moduleRoots {
		if dependency != modulePath && direct[dependency] && !releases[dependency] {
			if allowedUnreleased[dependency] {
				bootstrap[dependency] = true
				continue
			}
			return nil, fmt.Errorf("dependency %s will use a campaign worktree; add go_module_release %q before using --pr", dependency, dependency)
		}
	}
	return bootstrap, nil
}

func hasCampaignReplace(moduleRoot string, parsed *modfile.File, modulePath, campaignRoot string) (bool, error) {
	for _, replacement := range parsed.Replace {
		if replacement.Old.Path != modulePath {
			continue
		}
		replacementPath := replacement.New.Path
		if !filepath.IsAbs(replacementPath) {
			replacementPath = filepath.Join(moduleRoot, replacementPath)
		}
		replacementPath = filepath.Clean(replacementPath)
		if replacementPath != filepath.Clean(campaignRoot) {
			return false, fmt.Errorf("dependency %s has non-campaign replacement %q; remove it manually before using --pr", modulePath, replacement.New.Path)
		}
		return true, nil
	}
	return false, nil
}

func dropCampaignReplace(moduleRoot string, parsed *modfile.File, modulePath string) error {
	for _, replacement := range parsed.Replace {
		if replacement.Old.Path != modulePath {
			continue
		}
		old := replacement.Old.Path
		if replacement.Old.Version != "" {
			old += "@" + replacement.Old.Version
		}
		if _, err := runIn(moduleRoot, "go", "mod", "edit", "-dropreplace="+old); err != nil {
			return err
		}
	}
	return nil
}

func replaceGoModule(moduleRoot, goMod, modulePath, replacementRoot string) error {
	contents, err := os.ReadFile(goMod)
	if err != nil {
		return err
	}
	parsed, err := modfile.Parse(goMod, contents, nil)
	if err != nil {
		return err
	}
	for _, replacement := range parsed.Replace {
		if replacement.Old.Path != modulePath {
			continue
		}
		old := replacement.Old.Path
		if replacement.Old.Version != "" {
			old += "@" + replacement.Old.Version
		}
		if _, err := runIn(moduleRoot, "go", "mod", "edit", "-dropreplace="+old); err != nil {
			return err
		}
	}
	relative, err := filepath.Rel(moduleRoot, replacementRoot)
	if err != nil {
		return err
	}
	if relative != "." && !strings.HasPrefix(relative, ".") {
		relative = "." + string(filepath.Separator) + relative
	}
	_, err = runIn(moduleRoot, "go", "mod", "edit", "-replace="+modulePath+"="+filepath.ToSlash(relative))
	return err
}

func verifyGoModule(moduleRoot string, mode Verification) []VerificationResult {
	var commands [][]string
	switch mode {
	case VerifyCompile:
		commands = [][]string{{"go", "test", "-run=^$", "./..."}}
	case VerifyTest:
		commands = [][]string{{"go", "test", "./..."}}
	case VerifyFull:
		commands = [][]string{{"go", "vet", "./..."}, {"go", "test", "./..."}}
	}
	results := make([]VerificationResult, 0, len(commands))
	for _, command := range commands {
		output, err := runIn(moduleRoot, command[0], command[1:]...)
		result := VerificationResult{Command: strings.Join(command, " "), Passed: err == nil}
		if err != nil {
			result.Detail = shortenedDetail(output)
		}
		results = append(results, result)
	}
	return results
}

func shortenedDetail(output string) string {
	output = strings.TrimSpace(output)
	const max = 1000
	if len(output) > max {
		return output[:max] + "…"
	}
	return output
}

func worktreeChanged(dir string) (bool, error) {
	output, err := runIn(dir, "git", "status", "--porcelain")
	return strings.TrimSpace(output) != "", err
}

func campaignChangeTitle(spec Spec) string {
	if title := strings.TrimSpace(spec.Title); title != "" {
		return "chore: " + strings.TrimSuffix(title, ".")
	}
	return "chore: migrate " + spec.ID
}

func openCampaignPR(repo *campaignRepository, spec Spec) (string, error) {
	// A retry after a transient failure should reuse the branch's existing PR
	// rather than opening a duplicate one.
	if output, err := runIn(repo.worktree, "gh", "pr", "view", repo.branch, "--json", "url", "--jq", ".url"); err == nil {
		if url := strings.TrimSpace(output); url != "" {
			return url, nil
		}
	}
	output, err := runIn(repo.worktree, "gh", "pr", "create",
		"--base", repo.ref,
		"--head", repo.branch,
		"--title", campaignChangeTitle(spec),
		"--body", "Automated WB migration `"+spec.ID+"`. Local verification completed before this pull request was opened.")
	if err != nil {
		return "", err
	}
	url := strings.TrimSpace(output)
	if url == "" {
		return "", fmt.Errorf("gh pr create returned no pull request URL for %s", repo.repository)
	}
	return lastNonEmptyLine(url), nil
}

func requiredChecksGreen(worktree, prURL string) ([]RemoteCheck, bool, error) {
	output, err := runIn(worktree, "gh", "pr", "checks", prURL, "--required", "--json", "name,bucket")
	var checks []RemoteCheck
	if decodeErr := json.Unmarshal([]byte(output), &checks); decodeErr != nil {
		if err != nil {
			return nil, false, err
		}
		return nil, false, fmt.Errorf("decode required checks for %s: %w", prURL, decodeErr)
	}
	// gh exits non-zero while checks are pending or failing, but still returns
	// the requested JSON. Treat that as a blocked merge, not a transport error.
	if err != nil {
		return checks, false, nil
	}
	for _, check := range checks {
		if check.Bucket != "pass" {
			return checks, false, nil
		}
	}
	return checks, true, nil
}

func lastNonEmptyLine(value string) string {
	for lines := strings.Split(strings.TrimSpace(value), "\n"); len(lines) > 0; lines = lines[:len(lines)-1] {
		if line := strings.TrimSpace(lines[len(lines)-1]); line != "" {
			return line
		}
	}
	return ""
}

func runIn(dir, name string, args ...string) (string, error) {
	command := exec.Command(name, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

// Markdown renders the campaign index for human review and AI agents.
func (r CampaignReport) Markdown() string {
	var out strings.Builder
	fmt.Fprintf(&out, "# WB hierarchical migration: %s\n\n", r.Migration.ID)
	if r.Migration.Title != "" {
		fmt.Fprintf(&out, "%s\n\n", r.Migration.Title)
	}
	fmt.Fprintf(&out, "- Migration format: [%s](%s)\n- Status: `%s`\n- Base ref: `%s`\n- Verification: `%s`\n- Source root: `%s`\n\n", r.Migration.Format, r.Migration.Format, r.Status, r.BaseRef, r.Verification, r.SourceRoot)
	out.WriteString("## Repository index\n\n")
	out.WriteString("| Repository | Worktree | Ref | Changed files | Modules | Commit | Pushed | PR | Merged |\n|---|---|---|---:|---:|---|---|---|---|\n")
	for _, repo := range r.Repositories {
		pr := ""
		if repo.PR != "" {
			pr = "[PR](" + repo.PR + ")"
		}
		changedFiles := "unknown"
		if repo.ChangedFiles != nil {
			changedFiles = fmt.Sprintf("%d", len(*repo.ChangedFiles))
		}
		fmt.Fprintf(&out, "| `%s` | [%s](%s) | `%s` | `%s` | `%d` | `%s` | `%t` | %s | `%t` |\n", repo.Repository, repo.WorktreeDir, fileURL(repo.WorktreeDir), repo.Ref, changedFiles, len(repo.Modules), repo.Commit, repo.Pushed, pr, repo.Merged)
	}
	for _, repo := range r.Repositories {
		fmt.Fprintf(&out, "\n## %s\n\n", repo.Repository)
		if repo.ChangedFiles != nil {
			if len(*repo.ChangedFiles) == 0 {
				out.WriteString("No files differ from the campaign base ref.\n\n")
			} else {
				out.WriteString("Changed files relative to the campaign base ref:\n\n")
				for _, path := range *repo.ChangedFiles {
					fmt.Fprintf(&out, "- [%s](%s)\n", path, fileURL(filepath.Join(repo.WorktreeDir, filepath.FromSlash(path))))
				}
				fmt.Fprintf(&out, "\nInspect the repository diff with `%s`.\n\n", fmt.Sprintf("git -C %s diff %s", shellQuote(repo.WorktreeDir), shellQuote("origin/"+repo.Ref)))
			}
		}
		for _, module := range repo.Modules {
			files := "unknown (worktree not created)"
			if module.PlanState == "not_applicable" {
				files = "not applicable"
			} else if module.ChangedFiles != nil {
				files = fmt.Sprintf("%d", *module.ChangedFiles)
			}
			fmt.Fprintf(&out, "- `%s` — `%s`; plan: `%s`; files rewritten this pass: %s; manifest changed this pass: `%t`", module.Path, module.Status, module.PlanState, files, module.ManifestChanged)
			if module.PublishableManifest {
				out.WriteString("; publishable manifest: `true`")
			}
			if module.MigrationReportPath != "" {
				fmt.Fprintf(&out, "; [migration report](%s)", fileURL(filepath.Join(module.MigrationReportPath, "migration.md")))
			}
			out.WriteString("\n")
			for _, verification := range module.Verifications {
				fmt.Fprintf(&out, "  - `%s`: `%t`", verification.Command, verification.Passed)
				if verification.Detail != "" {
					fmt.Fprintf(&out, " — %s", verification.Detail)
				}
				out.WriteString("\n")
			}
		}
		if len(repo.RequiredChecks) > 0 {
			out.WriteString("\nRequired GitHub checks:\n")
			for _, check := range repo.RequiredChecks {
				fmt.Fprintf(&out, "- `%s`: `%s`\n", check.Name, check.Bucket)
			}
		}
	}
	return out.String()
}

// YAML renders the deterministic campaign manifest.
func (r CampaignReport) YAML() ([]byte, error) {
	return yaml.Marshal(r)
}

// WriteCampaignReports writes both report formats to dir.
func WriteCampaignReports(dir string, report CampaignReport) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "campaign.md"), []byte(report.Markdown()), 0o644); err != nil {
		return err
	}
	raw, err := report.YAML()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "campaign.yaml"), raw, 0o644)
}

func slug(value string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			out.WriteRune(r)
		} else {
			out.WriteRune('-')
		}
	}
	return strings.Trim(out.String(), "-")
}
