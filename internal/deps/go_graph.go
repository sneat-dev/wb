package deps

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"golang.org/x/mod/modfile"
)

type goFleetGraph struct {
	modules            map[string]goFleetModule
	moduleDeclarations map[string][]goFleetModule
	requirements       map[string][]goFleetRequirement
	repositoryModules  map[string][]string
	discoverySkips     []GraphDiscoverySkip
	baseRefFallbacks   []GraphDefaultBranchFallback
	manifestWarnings   []GraphManifestWarning
	ambiguousModules   []GraphAmbiguousModuleWarning
}

type goFleetModule struct {
	Path       string
	Repository string
	Manifest   string
}

type goFleetRequirement struct {
	Dependency     string
	Version        string
	ConsumerModule string
	Repository     string
	Manifest       string
	Indirect       bool
}

type graphDiscoveryProgress struct {
	RepositoriesTotal     int
	RepositoriesCompleted int
	LastRepository        string
}

type goGraphDiscoveryPolicy struct {
	SkipFailedNonGo bool
}

func discoverGoFleetGraph(ctx context.Context, repositories []Repository, options orchestrate.Options, policy goGraphDiscoveryPolicy, onProgress func(graphDiscoveryProgress)) (goFleetGraph, error) {
	graph := goFleetGraph{
		modules: map[string]goFleetModule{}, moduleDeclarations: map[string][]goFleetModule{}, requirements: map[string][]goFleetRequirement{},
		repositoryModules: map[string][]string{},
	}
	type repositoryGraph struct {
		modules      []goFleetModule
		requirements []goFleetRequirement
		warnings     []GraphManifestWarning
	}
	results := make([]repositoryGraph, len(repositories))
	errorsByRepository := make([]error, len(repositories))
	skipsByRepository := make([]*GraphDiscoverySkip, len(repositories))
	fallbacksByRepository := make([]*GraphDefaultBranchFallback, len(repositories))
	workers := options.Parallel
	if workers > len(repositories) {
		workers = len(repositories)
	}
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan int)
	var group sync.WaitGroup
	var progressMu sync.Mutex
	completed := 0
	recordProgress := func(repository string) {
		if onProgress == nil {
			return
		}
		progressMu.Lock()
		completed++
		item := graphDiscoveryProgress{
			RepositoriesTotal:     len(repositories),
			RepositoriesCompleted: completed,
			LastRepository:        repository,
		}
		progressMu.Unlock()
		onProgress(item)
	}
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				repository := repositories[index]
				func() {
					defer recordProgress(repository.Slug)
					if repository.Archived {
						return
					}
					owner, name, ok := strings.Cut(repository.Slug, "/")
					if !ok || owner == "" || name == "" {
						errorsByRepository[index] = fmt.Errorf("invalid repository slug %q", repository.Slug)
						return
					}
					canonical := repository.Path
					if canonical == "" {
						canonical = filepath.Join(options.GitHubDir, owner, name)
					}
					resolvedBase, ensureErr := orchestrate.EnsureCanonical(ctx, repository, canonical, options)
					if ensureErr != nil {
						skipsByRepository[index], errorsByRepository[index] = classifyGoGraphDiscoveryFailure(repository.Slug, canonical, ensureErr, policy)
						return
					}
					if resolvedBase.Fallback {
						fallbacksByRepository[index] = &GraphDefaultBranchFallback{Repository: repository.Slug, Ref: resolvedBase.Ref}
					}
					result, err := inspectRepositoryGoGraph(ctx, repository.Slug, canonical, "origin/"+resolvedBase.Ref, options)
					results[index] = result
					if err != nil {
						skipsByRepository[index], errorsByRepository[index] = classifyGoGraphDiscoveryFailure(repository.Slug, canonical, err, policy)
					}
				}()
			}
		}()
	}
	for index := range repositories {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	for _, result := range results {
		for _, module := range result.modules {
			graph.moduleDeclarations[module.Path] = append(graph.moduleDeclarations[module.Path], module)
			if len(graph.moduleDeclarations[module.Path]) == 1 {
				graph.modules[module.Path] = module
			} else {
				delete(graph.modules, module.Path)
			}
			graph.repositoryModules[module.Repository] = append(graph.repositoryModules[module.Repository], module.Path)
		}
		for _, requirement := range result.requirements {
			graph.requirements[requirement.Dependency] = append(graph.requirements[requirement.Dependency], requirement)
		}
		graph.manifestWarnings = append(graph.manifestWarnings, result.warnings...)
	}
	var discoveryErrors []error
	for index, err := range errorsByRepository {
		if err != nil {
			discoveryErrors = append(discoveryErrors, err)
		}
		if skipsByRepository[index] != nil {
			graph.discoverySkips = append(graph.discoverySkips, *skipsByRepository[index])
		}
		if fallbacksByRepository[index] != nil {
			graph.baseRefFallbacks = append(graph.baseRefFallbacks, *fallbacksByRepository[index])
		}
	}
	sort.Slice(graph.discoverySkips, func(i, j int) bool {
		return graph.discoverySkips[i].Repository < graph.discoverySkips[j].Repository
	})
	sort.Slice(graph.baseRefFallbacks, func(i, j int) bool {
		return graph.baseRefFallbacks[i].Repository < graph.baseRefFallbacks[j].Repository
	})
	sort.Slice(graph.manifestWarnings, func(i, j int) bool {
		left, right := graph.manifestWarnings[i], graph.manifestWarnings[j]
		if left.Repository == right.Repository {
			return left.Manifest < right.Manifest
		}
		return left.Repository < right.Repository
	})
	for repository := range graph.repositoryModules {
		sort.Strings(graph.repositoryModules[repository])
	}
	for dependency := range graph.requirements {
		sort.Slice(graph.requirements[dependency], func(i, j int) bool {
			left, right := graph.requirements[dependency][i], graph.requirements[dependency][j]
			if left.Repository == right.Repository {
				return left.Manifest < right.Manifest
			}
			return left.Repository < right.Repository
		})
	}
	for module := range graph.moduleDeclarations {
		sort.Slice(graph.moduleDeclarations[module], func(i, j int) bool {
			left, right := graph.moduleDeclarations[module][i], graph.moduleDeclarations[module][j]
			if left.Repository == right.Repository {
				return left.Manifest < right.Manifest
			}
			return left.Repository < right.Repository
		})
		declarations := graph.moduleDeclarations[module]
		if canonical, ok := canonicalGoModuleDeclaration(module, declarations); ok {
			graph.modules[module] = canonical
			if len(declarations) > 1 {
				graph.ambiguousModules = append(graph.ambiguousModules, ambiguousModuleWarning(
					module, canonical, declarations, "the module's own declared path names this repository",
				))
			}
			continue
		}
		if canonical, ok := resolveDuplicateCloneModuleDeclaration(ctx, declarations, options); ok {
			graph.modules[module] = canonical
			graph.ambiguousModules = append(graph.ambiguousModules, ambiguousModuleWarning(
				module, canonical, declarations,
				"this repository's own origin remote matches its directory; the other declaration(s) are a stale duplicate clone or an unrelated fork and were not treated as a second real provider",
			))
		}
	}
	sort.Slice(graph.ambiguousModules, func(i, j int) bool {
		left, right := graph.ambiguousModules[i], graph.ambiguousModules[j]
		if left.Module != right.Module {
			return left.Module < right.Module
		}
		return left.Repository < right.Repository
	})
	return graph, errors.Join(discoveryErrors...)
}

// ambiguousModuleWarning records a deterministic pick among a module's
// multiple declarations, naming every other declaration as a duplicate so
// the substitution stays visible in the report rather than silent.
func ambiguousModuleWarning(module string, canonical goFleetModule, declarations []goFleetModule, reason string) GraphAmbiguousModuleWarning {
	duplicates := make([]string, 0, len(declarations)-1)
	for _, declaration := range declarations {
		if declaration.Repository == canonical.Repository && declaration.Manifest == canonical.Manifest {
			continue
		}
		duplicates = append(duplicates, declaration.Repository+":"+declaration.Manifest)
	}
	sort.Strings(duplicates)
	return GraphAmbiguousModuleWarning{
		Module: module, Repository: canonical.Repository, Manifest: canonical.Manifest,
		Duplicates: duplicates, Reason: reason,
	}
}

// resolveDuplicateCloneModuleDeclaration is the fallback tie-breaker for a
// module whose declared path does not name any of its declaring
// repositories (so canonicalGoModuleDeclaration cannot resolve it): two
// different local directories under --projects-root that are actually
// clones of the very same GitHub repository — most commonly a stale
// duplicate left behind by an org move or rename, where the old directory
// was never deleted and still declares the module under its now-wrong
// directory-derived slug. It compares each declaring repository's own
// `origin` remote against the {org}/{repo} its OWN directory name claims.
// Resolution requires EXACTLY ONE declaration to be self-consistent (its
// remote actually points at the repository its own slug names): that single
// case is the signature of a stale duplicate (the other declarations are
// misplaced clones of it) or an unrelated fork already handled by
// canonicalGoModuleDeclaration before this runs. Two (or zero)
// self-consistent declarations mean this is not that pattern — most likely
// a genuine coincidence between two unrelated, correctly named
// repositories — and the tie is deliberately left unresolved rather than
// guessed at.
func resolveDuplicateCloneModuleDeclaration(ctx context.Context, declarations []goFleetModule, options orchestrate.Options) (goFleetModule, bool) {
	var consistent []goFleetModule
	for _, declaration := range declarations {
		owner, name, ok := strings.Cut(declaration.Repository, "/")
		if !ok || owner == "" || name == "" {
			continue
		}
		canonicalPath := filepath.Join(options.GitHubDir, owner, name)
		slug, err := remoteOriginSlug(ctx, canonicalPath, options)
		if err != nil {
			continue
		}
		if slug == declaration.Repository {
			consistent = append(consistent, declaration)
		}
	}
	if len(consistent) == 1 {
		return consistent[0], true
	}
	return goFleetModule{}, false
}

// remoteOriginSlug resolves the actual GitHub {org}/{repo} a canonical
// clone's `origin` remote points at, independent of the local directory
// name under --projects-root. It mirrors worktrees.OriginSlug's URL parsing
// (SSH and HTTPS github.com remotes), kept local to this package so deps
// discovery does not take on a new cross-package dependency for one string
// transform.
func remoteOriginSlug(ctx context.Context, canonical string, options orchestrate.Options) (string, error) {
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	remote := strings.TrimSuffix(strings.TrimSpace(output), ".git")
	remote = strings.TrimSuffix(remote, "/")
	switch {
	case strings.Contains(remote, "github.com:"):
		remote = remote[strings.LastIndex(remote, "github.com:")+len("github.com:"):]
	case strings.Contains(remote, "github.com/"):
		remote = remote[strings.LastIndex(remote, "github.com/")+len("github.com/"):]
	default:
		parts := strings.Split(remote, "/")
		if len(parts) < 2 {
			return "", fmt.Errorf("cannot derive owner/repository from origin %q", remote)
		}
		remote = strings.Join(parts[len(parts)-2:], "/")
	}
	owner, name, ok := strings.Cut(remote, "/")
	if !ok || owner == "" || name == "" {
		return "", fmt.Errorf("origin remote does not identify owner/repository: %q", remote)
	}
	return remote, nil
}

func classifyGoGraphDiscoveryFailure(repository, canonical string, cause error, policy goGraphDiscoveryPolicy) (*GraphDiscoverySkip, error) {
	wrapped := fmt.Errorf("%s: %w", repository, cause)
	if !policy.SkipFailedNonGo {
		return nil, wrapped
	}
	// An unreadable/remote-less local clone (no 'origin' configured, or not a
	// git repository at all) needs manual repair regardless of what it
	// contains, so it is skipped and reported rather than aborting an
	// otherwise healthy fleet campaign. This is unconditional — unlike the
	// manifest check below, it does not matter whether the repository has a
	// go.mod, because WB cannot inspect origin at all to find out safely.
	if looksLikeUnreadableClone(cause) {
		return &GraphDiscoverySkip{
			Repository: repository,
			Reason:     fmt.Sprintf("local clone is unreadable (no usable git remote); skipped rather than aborting the fleet — needs manual repair: %v", cause),
		}, nil
	}
	hasGoManifest, inspectErr := repositoryContainsLocalGoManifest(canonical)
	if inspectErr != nil {
		return nil, errors.Join(wrapped, fmt.Errorf("%s: cannot prove failed repository is irrelevant to Go propagation: %w", repository, inspectErr))
	}
	if hasGoManifest {
		return nil, wrapped
	}
	// A missing remote ref in a website must not abort an otherwise unrelated
	// Go campaign. We only take this Actions-saving path after a local scan
	// proves that the repository contains no usable Go manifest.
	return &GraphDiscoverySkip{
		Repository: repository,
		Reason:     fmt.Sprintf("remote Go graph inspection failed, but the local repository contains no go.mod: %v", cause),
	}, nil
}

func repositoryContainsLocalGoManifest(root string) (bool, error) {
	if _, err := os.Stat(root); err != nil {
		return false, err
	}
	found := errors.New("go.mod found")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && path != root {
			switch entry.Name() {
			case ".git", ".wb", ".worktrees", "node_modules", "vendor":
				return filepath.SkipDir
			}
		}
		if !entry.IsDir() && entry.Name() == "go.mod" && !ignoredManifestPath(filepath.ToSlash(path)) {
			return found
		}
		return nil
	})
	if errors.Is(err, found) {
		return true, nil
	}
	return false, err
}

func canonicalGoModuleDeclaration(module string, declarations []goFleetModule) (goFleetModule, bool) {
	if len(declarations) == 1 {
		return declarations[0], true
	}
	parts := strings.Split(strings.TrimPrefix(module, "github.com/"), "/")
	if !strings.HasPrefix(module, "github.com/") || len(parts) < 2 {
		return goFleetModule{}, false
	}
	slug := parts[0] + "/" + parts[1]
	for _, declaration := range declarations {
		if declaration.Repository == slug {
			return declaration, true
		}
	}
	return goFleetModule{}, false
}

func (graph goFleetGraph) validateUniqueModuleDeclarations() error {
	var conflicts []string
	for module, declarations := range graph.moduleDeclarations {
		if len(declarations) < 2 {
			continue
		}
		// A module deterministically resolved to one canonical declaration
		// during discovery (see the resolution loop at the end of
		// discoverGoFleetGraph) is recorded as a GraphAmbiguousModuleWarning,
		// not a fatal conflict — only a genuinely unresolvable tie still
		// aborts the fleet.
		if _, resolved := graph.modules[module]; resolved {
			continue
		}
		locations := make([]string, 0, len(declarations))
		for _, declaration := range declarations {
			locations = append(locations, declaration.Repository+":"+declaration.Manifest)
		}
		sort.Strings(locations)
		conflicts = append(conflicts, fmt.Sprintf("go module %s is declared by %s", module, strings.Join(locations, ", ")))
	}
	sort.Strings(conflicts)
	if len(conflicts) > 0 {
		return errors.New(strings.Join(conflicts, "; "))
	}
	return nil
}

func inspectRepositoryGoGraph(ctx context.Context, repository, canonical, base string, options orchestrate.Options) (struct {
	modules      []goFleetModule
	requirements []goFleetRequirement
	warnings     []GraphManifestWarning
}, error) {
	result := struct {
		modules      []goFleetModule
		requirements []goFleetRequirement
		warnings     []GraphManifestWarning
	}{}
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "ls-tree", "-r", "--name-only", base)
	if err != nil {
		return result, err
	}
	for _, name := range strings.Split(strings.TrimSpace(output), "\n") {
		if filepath.Base(name) != "go.mod" || ignoredManifestPath(name) {
			continue
		}
		contents, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "show", base+":"+name)
		if err != nil {
			return result, err
		}
		parsed, err := modfile.Parse(name, []byte(contents), nil)
		if err != nil {
			// A go.mod that is not the repository's root manifest is often not a
			// real module declaration at all — most commonly a code-generator
			// template (e.g. an EJS `<%= family %>` interpolation in place of a
			// module path) committed under a tools/ directory. WB cannot safely
			// assume the same about the ROOT go.mod, so that one still fails the
			// whole repository; a non-root one is skipped with a warning instead
			// of aborting an otherwise healthy fleet campaign over a template file
			// that was never meant to be parsed as Go.
			if name != "go.mod" {
				result.warnings = append(result.warnings, GraphManifestWarning{
					Repository: repository, Manifest: name,
					Reason: fmt.Sprintf("skipped unparseable non-root go.mod (likely a code-generator template, not a real module declaration): %v", err),
				})
				continue
			}
			return result, fmt.Errorf("parse %s: %w", name, err)
		}
		if parsed.Module == nil || parsed.Module.Mod.Path == "" {
			return result, fmt.Errorf("%s has no module path", name)
		}
		module := goFleetModule{Path: parsed.Module.Mod.Path, Repository: repository, Manifest: name}
		result.modules = append(result.modules, module)
		for _, requirement := range parsed.Require {
			result.requirements = append(result.requirements, goFleetRequirement{
				Dependency: requirement.Mod.Path, Version: requirement.Mod.Version,
				ConsumerModule: module.Path, Repository: repository, Manifest: name, Indirect: requirement.Indirect,
			})
		}
	}
	return result, nil
}

func (graph goFleetGraph) repositoriesForEvents(events []ReleaseEvent) map[string][]Target {
	targets := map[string]map[string]Target{}
	for _, event := range events {
		for _, requirement := range graph.requirements[event.Dependency] {
			if requirement.Version == event.Version {
				continue
			}
			if targets[requirement.Repository] == nil {
				targets[requirement.Repository] = map[string]Target{}
			}
			targets[requirement.Repository][event.Dependency] = Target{
				Ecosystem: EcosystemGo, Dependency: event.Dependency,
				Version: event.Version, Resolved: event.Version,
			}
		}
	}
	result := make(map[string][]Target, len(targets))
	for repository, byDependency := range targets {
		for _, target := range byDependency {
			result[repository] = append(result[repository], target)
		}
		sort.Slice(result[repository], func(i, j int) bool { return result[repository][i].Dependency < result[repository][j].Dependency })
	}
	return result
}

// coalescedRepositoriesForEvents selects only the earliest pending
// provider-first layer. A repository that directly consumes the seed and also
// consumes an intermediate provider is assigned the longer path, so it waits
// and receives all releases in one PR and one GitHub Actions build.
func (graph goFleetGraph) coalescedRepositoriesForEvents(seedEvents, events []ReleaseEvent) (map[string][]Target, []string) {
	allTargets := graph.repositoriesForEvents(events)
	if len(allTargets) == 0 {
		return nil, nil
	}
	adjacency := graph.repositoryAdjacency()
	roots := map[string]bool{}
	for _, event := range seedEvents {
		for _, requirement := range graph.requirements[event.Dependency] {
			roots[requirement.Repository] = true
		}
	}
	reachable := map[string]bool{}
	var visit func(string)
	visit = func(repository string) {
		if reachable[repository] {
			return
		}
		reachable[repository] = true
		for _, consumer := range adjacency[repository] {
			visit(consumer)
		}
	}
	for repository := range roots {
		visit(repository)
	}
	restricted := map[string][]string{}
	for repository := range reachable {
		for _, consumer := range adjacency[repository] {
			if reachable[consumer] {
				restricted[repository] = append(restricted[repository], consumer)
			}
		}
		if _, exists := restricted[repository]; !exists {
			restricted[repository] = nil
		}
	}
	levels := map[string]int{}
	for _, component := range topologicalLayers(restricted) {
		for _, repository := range component.nodes {
			levels[repository] = component.level
		}
	}
	firstLevel := int(^uint(0) >> 1)
	for repository := range allTargets {
		level, exists := levels[repository]
		if !exists {
			level = 0
		}
		if level < firstLevel {
			firstLevel = level
		}
	}
	selected := map[string][]Target{}
	var deferred []string
	for repository, targets := range allTargets {
		level, exists := levels[repository]
		if !exists {
			level = 0
		}
		if level == firstLevel {
			selected[repository] = targets
		} else {
			deferred = append(deferred, repository)
		}
	}
	sort.Strings(deferred)
	return selected, deferred
}

func (graph goFleetGraph) repositoryAdjacency() map[string][]string {
	sets := map[string]map[string]bool{}
	for dependency, requirements := range graph.requirements {
		provider, exists := graph.modules[dependency]
		if !exists {
			continue
		}
		if sets[provider.Repository] == nil {
			sets[provider.Repository] = map[string]bool{}
		}
		for _, requirement := range requirements {
			if provider.Repository == requirement.Repository {
				continue
			}
			if sets[requirement.Repository] == nil {
				sets[requirement.Repository] = map[string]bool{}
			}
			sets[provider.Repository][requirement.Repository] = true
		}
	}
	adjacency := make(map[string][]string, len(sets))
	for repository, consumers := range sets {
		for consumer := range consumers {
			adjacency[repository] = append(adjacency[repository], consumer)
		}
		sort.Strings(adjacency[repository])
	}
	return adjacency
}

// pendingCarriersBlockTargets reports whether an unpublished, already-current
// provider is upstream of this selected batch. Independent repositories may
// still build while WB waits; only downstream CI is held back for coalescing.
func (graph goFleetGraph) pendingCarriersBlockTargets(carriers []ReleaseObservation, targets map[string][]Target) bool {
	if len(targets) == 0 {
		return true
	}
	adjacency := graph.repositoryAdjacency()
	for _, carrier := range carriers {
		if carrier.Status == "released" && carrier.After != "" {
			continue
		}
		visited := map[string]bool{}
		var reachesTarget func(string) bool
		reachesTarget = func(repository string) bool {
			if visited[repository] {
				return false
			}
			visited[repository] = true
			if repository != carrier.Repository && len(targets[repository]) > 0 {
				return true
			}
			for _, consumer := range adjacency[repository] {
				if reachesTarget(consumer) {
					return true
				}
			}
			return false
		}
		if reachesTarget(carrier.Repository) {
			return true
		}
	}
	return false
}

func (graph goFleetGraph) affectedModules(targetsByRepository map[string][]Target) map[string]map[string]bool {
	result := map[string]map[string]bool{}
	for repository, targets := range targetsByRepository {
		for _, target := range targets {
			for _, requirement := range graph.requirements[target.Dependency] {
				if requirement.Repository != repository || requirement.Version == target.Version {
					continue
				}
				if result[repository] == nil {
					result[repository] = map[string]bool{}
				}
				result[repository][requirement.ConsumerModule] = true
			}
		}
	}
	return result
}

func (graph goFleetGraph) hasExternalConsumers(modulePath, repository string) bool {
	for _, requirement := range graph.requirements[modulePath] {
		if requirement.Repository != repository {
			return true
		}
	}
	return false
}

// validateAcyclicPropagation rejects relevant cross-repository dependency
// cycles before any worktree is created. A release wave cannot safely order a
// cycle without a separate coordinated-version protocol.
func (graph goFleetGraph) validateAcyclicPropagation(events []ReleaseEvent) error {
	adjacency := map[string]map[string]bool{}
	for dependency, requirements := range graph.requirements {
		provider, internal := graph.modules[dependency]
		if !internal {
			continue
		}
		for _, requirement := range requirements {
			if provider.Repository == requirement.Repository {
				continue
			}
			if adjacency[provider.Repository] == nil {
				adjacency[provider.Repository] = map[string]bool{}
			}
			adjacency[provider.Repository][requirement.Repository] = true
		}
	}
	roots := map[string]bool{}
	for _, event := range events {
		if provider, exists := graph.modules[event.Dependency]; exists {
			roots[provider.Repository] = true
		}
		for _, requirement := range graph.requirements[event.Dependency] {
			roots[requirement.Repository] = true
		}
	}
	reachable := map[string]bool{}
	var visitReachable func(string)
	visitReachable = func(repository string) {
		if reachable[repository] {
			return
		}
		reachable[repository] = true
		for consumer := range adjacency[repository] {
			visitReachable(consumer)
		}
	}
	for repository := range roots {
		visitReachable(repository)
	}
	state := map[string]uint8{}
	stack := make([]string, 0, len(reachable))
	stackIndex := map[string]int{}
	var cycle []string
	var visitCycle func(string) bool
	visitCycle = func(repository string) bool {
		state[repository] = 1
		stackIndex[repository] = len(stack)
		stack = append(stack, repository)
		consumers := make([]string, 0, len(adjacency[repository]))
		for consumer := range adjacency[repository] {
			if reachable[consumer] {
				consumers = append(consumers, consumer)
			}
		}
		sort.Strings(consumers)
		for _, consumer := range consumers {
			switch state[consumer] {
			case 0:
				if visitCycle(consumer) {
					return true
				}
			case 1:
				cycle = append(cycle, stack[stackIndex[consumer]:]...)
				cycle = append(cycle, consumer)
				return true
			}
		}
		stack = stack[:len(stack)-1]
		delete(stackIndex, repository)
		state[repository] = 2
		return false
	}
	repositories := make([]string, 0, len(reachable))
	for repository := range reachable {
		repositories = append(repositories, repository)
	}
	sort.Strings(repositories)
	for _, repository := range repositories {
		if state[repository] == 0 && visitCycle(repository) {
			return fmt.Errorf("dependency propagation cycle requires a coordinated release protocol: %s", strings.Join(cycle, " -> "))
		}
	}
	return nil
}

// Skips lists repositories excluded from the walk rather than inspected. It
// exposes the private discoverySkips field so goFleetGraph can satisfy the
// ecosystem-neutral bumpFleetGraph interface (see fleet_graph.go); a method
// cannot share its identifier with a field, hence the capitalized name.
func (graph goFleetGraph) Skips() []GraphDiscoverySkip { return graph.discoverySkips }

// BaseRefFallbacks lists repositories whose canonical clone did not contain
// the operation's configured base ref, discovered by falling back to each
// repository's actual origin/HEAD default branch instead (see
// orchestrate.EnsureCanonical). Unlike Skips, these repositories were fully
// discovered and included in the graph — only the ref used to read them
// differed from the fleet-wide default.
func (graph goFleetGraph) BaseRefFallbacks() []GraphDefaultBranchFallback {
	return graph.baseRefFallbacks
}

// ManifestWarnings lists non-root go.mod files that failed to parse as a Go
// module declaration but did not abort discovery (see
// inspectRepositoryGoGraph).
func (graph goFleetGraph) ManifestWarnings() []GraphManifestWarning { return graph.manifestWarnings }

// AmbiguousModules lists modules declared by more than one repository whose
// conflict was deterministically resolved instead of aborting the fleet
// (see resolveDuplicateCloneModuleDeclaration and canonicalGoModuleDeclaration).
func (graph goFleetGraph) AmbiguousModules() []GraphAmbiguousModuleWarning {
	return graph.ambiguousModules
}

// requirementsForDependency projects every requirement of one module path
// into the ecosystem-neutral fleetRequirement shape the bump wave engine
// uses to traverse an already-published consumer without depending on
// goFleetRequirement directly.
func (graph goFleetGraph) requirementsForDependency(dependency string) []fleetRequirement {
	requirements := graph.requirements[dependency]
	result := make([]fleetRequirement, 0, len(requirements))
	for _, requirement := range requirements {
		result = append(result, fleetRequirement{
			ConsumerModule: requirement.ConsumerModule, Repository: requirement.Repository, Version: requirement.Version,
		})
	}
	return result
}
