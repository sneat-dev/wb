package migrate

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/mod/modfile"
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
	Verify     Verification
	Commit     bool
	Push       bool
	PR         bool
	Merge      bool
	Parallel   int
	ReportDir  string
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
	ChangedFiles        int                  `yaml:"changed_files"`
	ManifestChanged     bool                 `yaml:"manifest_changed"`
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
	modules    []*campaignModule
	report     *CampaignRepositoryReport
}

type listedModule struct {
	Path    string
	Version string
	Main    bool
}

type goListModule struct {
	Path    string
	Version string
	Main    bool
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
	listed, children, err := inspectGoModuleGraph(sourceRoot)
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
		entry.report = &CampaignModuleReport{Path: modulePath, MigrationEnabled: entry.migrate, Status: "planned"}
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
	layers, err := c.repositoryLayers()
	if err != nil {
		return err
	}
	for _, layer := range layers {
		if err := runRepositoriesParallel(layer, c.options.Parallel, func(repo *campaignRepository) error {
			return c.processRepository(repo, moduleRoots)
		}); err != nil {
			return err
		}
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

// processRepository runs the local mutation, verification, and optional
// publish phases for a dependency-ready repository. GitHub CI therefore runs
// asynchronously while WB proceeds to other ready repositories.
func (c *campaign) processRepository(repo *campaignRepository, moduleRoots map[string]string) error {
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
		manifestChanged, err := updateGoModule(module.root, c.spec, module.path, moduleRoots)
		if err != nil {
			return fmt.Errorf("update go.mod for %s: %w", module.path, err)
		}
		module.report.ChangedFiles = len(plan.Changes)
		module.report.ManifestChanged = manifestChanged
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
	if c.options.Verify != VerifyNone {
		for _, module := range repo.modules {
			if !module.migrate || (module.report.ChangedFiles == 0 && !module.report.ManifestChanged) {
				continue
			}
			results := verifyGoModule(module.root, c.options.Verify)
			module.report.Verifications = results
			for _, result := range results {
				if !result.Passed {
					return fmt.Errorf("verification failed for %s: %s", module.path, result.Command)
				}
			}
		}
	}
	if !c.options.Commit {
		return nil
	}
	changed, err := worktreeChanged(repo.worktree)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if _, err := runIn(repo.worktree, "git", "add", "-A"); err != nil {
		return err
	}
	if _, err := runIn(repo.worktree, "git", "commit", "-m", "chore: migrate "+c.spec.ID); err != nil {
		return err
	}
	head, err := runIn(repo.worktree, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	repo.report.Commit = strings.TrimSpace(head)
	if c.options.Push {
		if _, err := runIn(repo.worktree, "git", "push", "-u", "origin", repo.branch); err != nil {
			return err
		}
		repo.report.Pushed = true
	}
	if c.options.PR {
		prURL, err := openCampaignPR(repo, c.spec.ID)
		if err != nil {
			return err
		}
		repo.report.PR = prURL
	}
	return nil
}

func (c *campaign) repositoryLayers() ([][]*campaignRepository, error) {
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
	state := map[string]int{}
	depths := map[string]int{}
	var depth func(string) (int, error)
	depth = func(repository string) (int, error) {
		switch state[repository] {
		case 1:
			return 0, fmt.Errorf("cycle in campaign repository dependencies at %s", repository)
		case 2:
			return depths[repository], nil
		}
		state[repository] = 1
		level := 0
		for dependency := range dependencies[repository] {
			candidate, err := depth(dependency)
			if err != nil {
				return 0, err
			}
			if candidate+1 > level {
				level = candidate + 1
			}
		}
		state[repository] = 2
		depths[repository] = level
		return level, nil
	}
	maxDepth := 0
	for repository := range repositories {
		value, err := depth(repository)
		if err != nil {
			return nil, err
		}
		if value > maxDepth {
			maxDepth = value
		}
	}
	layers := make([][]*campaignRepository, maxDepth+1)
	for repository, repo := range repositories {
		layers[depths[repository]] = append(layers[depths[repository]], repo)
	}
	for _, layer := range layers {
		sort.Slice(layer, func(i, j int) bool { return layer[i].repository < layer[j].repository })
	}
	return layers, nil
}

func runRepositoriesParallel(repositories []*campaignRepository, parallel int, action func(*campaignRepository) error) error {
	if len(repositories) == 0 {
		return nil
	}
	workers := parallel
	if workers > len(repositories) {
		workers = len(repositories)
	}
	jobs := make(chan *campaignRepository)
	errors := make(chan error, len(repositories))
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for repo := range jobs {
				if err := action(repo); err != nil {
					errors <- err
				}
			}
		}()
	}
	for _, repo := range repositories {
		jobs <- repo
	}
	close(jobs)
	group.Wait()
	close(errors)
	for err := range errors {
		return err
	}
	return nil
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
		cloneURL := "https://" + repo.repository + ".git"
		if _, err := runIn(filepath.Dir(repo.canonical), "git", "clone", "--quiet", cloneURL, repo.canonical); err != nil {
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

func inspectGoModuleGraph(root string) (map[string]listedModule, map[string][]string, error) {
	output, err := runIn(root, "go", "list", "-m", "-json", "all")
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
		modules[module.Path] = listedModule{Path: module.Path, Version: module.Version, Main: module.Main}
	}
	graph, err := runIn(root, "go", "mod", "graph")
	if err != nil {
		return nil, nil, err
	}
	children := map[string][]string{}
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
		children[parent] = append(children[parent], child)
	}
	for module := range children {
		sort.Strings(children[module])
	}
	return modules, children, nil
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
	after, err := os.ReadFile(goMod)
	if err != nil {
		return false, err
	}
	return string(before) != string(after), nil
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

func openCampaignPR(repo *campaignRepository, migrationID string) (string, error) {
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
		"--title", "chore: migrate "+migrationID,
		"--body", "Automated WB migration `"+migrationID+"`. Local verification completed before this pull request was opened.")
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
	out.WriteString("| Repository | Worktree | Ref | Modules | Commit | Pushed | PR | Merged |\n|---|---|---|---:|---|---|---|---|\n")
	for _, repo := range r.Repositories {
		pr := ""
		if repo.PR != "" {
			pr = "[PR](" + repo.PR + ")"
		}
		fmt.Fprintf(&out, "| `%s` | [%s](%s) | `%s` | `%d` | `%s` | `%t` | %s | `%t` |\n", repo.Repository, repo.WorktreeDir, fileURL(repo.WorktreeDir), repo.Ref, len(repo.Modules), repo.Commit, repo.Pushed, pr, repo.Merged)
	}
	for _, repo := range r.Repositories {
		fmt.Fprintf(&out, "\n## %s\n\n", repo.Repository)
		for _, module := range repo.Modules {
			fmt.Fprintf(&out, "- `%s` — `%s`; files: `%d`; manifest changed: `%t`", module.Path, module.Status, module.ChangedFiles, module.ManifestChanged)
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
