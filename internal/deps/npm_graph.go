package deps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

// npmFleetGraph is the npm-ecosystem analogue of goFleetGraph: one fleet-wide
// scan of every package.json and pnpm-workspace.yaml at a base ref, kept as
// canonical evidence for both `deps graph --ecosystem npm` and `deps bump
// npm`. It intentionally mirrors goFleetGraph's shape and algorithms rather
// than sharing code with it, so npm support cannot change Go-adapter
// behavior; see fleet_graph.go for the narrow interface the bump wave engine
// actually depends on.
type npmFleetGraph struct {
	packages            map[string]npmFleetPackage
	packageDeclarations map[string][]npmFleetPackage
	requirements        map[string][]npmFleetRequirement
	repositoryPackages  map[string][]string
	discoverySkips      []GraphDiscoverySkip
}

// npmFleetPackage is one published (non-private, named) package.json
// declaration.
type npmFleetPackage struct {
	Name       string
	Repository string
	Manifest   string
	Version    string
}

// npmFleetRequirement is one manifest-owned dependency reference: a
// package.json dependency field entry, or a pnpm-workspace.yaml
// overrides/catalog entry. ConsumerPackage is empty for a workspace-level
// override or catalog entry that is not also the "name" of a package.json —
// pnpm-workspace.yaml pins a version fleet-wide, not per consuming package.
type npmFleetRequirement struct {
	Dependency      string
	Version         string
	ConsumerPackage string
	Repository      string
	Manifest        string
	Field           string
}

type npmGraphDiscoveryPolicy struct {
	SkipFailedNonNPM bool
}

// discoverNpmFleetGraph scans every selected repository's package.json and
// pnpm-workspace.yaml files at "origin/<ref>" in parallel. It mirrors
// discoverGoFleetGraph's shape exactly (parallelism, progress reporting,
// discovery-failure classification) so both ecosystems share one bump-wave
// engine loop in bump.go.
func discoverNpmFleetGraph(ctx context.Context, repositories []Repository, options orchestrate.Options, policy npmGraphDiscoveryPolicy, onProgress func(graphDiscoveryProgress)) (npmFleetGraph, error) {
	graph := npmFleetGraph{
		packages: map[string]npmFleetPackage{}, packageDeclarations: map[string][]npmFleetPackage{},
		requirements: map[string][]npmFleetRequirement{}, repositoryPackages: map[string][]string{},
	}
	type repositoryGraph struct {
		packages     []npmFleetPackage
		requirements []npmFleetRequirement
	}
	results := make([]repositoryGraph, len(repositories))
	errorsByRepository := make([]error, len(repositories))
	skipsByRepository := make([]*GraphDiscoverySkip, len(repositories))
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
		defer progressMu.Unlock()
		completed++
		onProgress(graphDiscoveryProgress{
			RepositoriesTotal: len(repositories), RepositoriesCompleted: completed, LastRepository: repository,
		})
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
					if err := orchestrate.EnsureCanonical(ctx, repository, canonical, options); err != nil {
						skipsByRepository[index], errorsByRepository[index] = classifyNpmGraphDiscoveryFailure(repository.Slug, canonical, err, policy)
						return
					}
					result, err := inspectRepositoryNpmGraph(ctx, repository.Slug, canonical, "origin/"+options.Ref, options)
					results[index] = result
					if err != nil {
						skipsByRepository[index], errorsByRepository[index] = classifyNpmGraphDiscoveryFailure(repository.Slug, canonical, err, policy)
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
		for _, pkg := range result.packages {
			graph.packageDeclarations[pkg.Name] = append(graph.packageDeclarations[pkg.Name], pkg)
			if len(graph.packageDeclarations[pkg.Name]) == 1 {
				graph.packages[pkg.Name] = pkg
			} else {
				delete(graph.packages, pkg.Name)
			}
			graph.repositoryPackages[pkg.Repository] = append(graph.repositoryPackages[pkg.Repository], pkg.Name)
		}
		for _, requirement := range result.requirements {
			graph.requirements[requirement.Dependency] = append(graph.requirements[requirement.Dependency], requirement)
		}
	}
	var discoveryErrors []error
	for index, err := range errorsByRepository {
		if err != nil {
			discoveryErrors = append(discoveryErrors, err)
		}
		if skipsByRepository[index] != nil {
			graph.discoverySkips = append(graph.discoverySkips, *skipsByRepository[index])
		}
	}
	sort.Slice(graph.discoverySkips, func(i, j int) bool { return graph.discoverySkips[i].Repository < graph.discoverySkips[j].Repository })
	for repository := range graph.repositoryPackages {
		sort.Strings(graph.repositoryPackages[repository])
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
	for name, declarations := range graph.packageDeclarations {
		sort.Slice(declarations, func(i, j int) bool {
			if declarations[i].Repository == declarations[j].Repository {
				return declarations[i].Manifest < declarations[j].Manifest
			}
			return declarations[i].Repository < declarations[j].Repository
		})
		graph.packageDeclarations[name] = declarations
		// Unlike Go modules, npm package names carry no owner/repository
		// convention to fall back on when the same name is declared in more
		// than one repository, so an ambiguous declaration simply has no
		// canonical provider — it is reported as ambiguous by graphFromNpmFleet
		// rather than guessed at here.
		if len(declarations) == 1 {
			graph.packages[name] = declarations[0]
		}
	}
	return graph, errors.Join(discoveryErrors...)
}

func classifyNpmGraphDiscoveryFailure(repository, canonical string, cause error, policy npmGraphDiscoveryPolicy) (*GraphDiscoverySkip, error) {
	wrapped := fmt.Errorf("%s: %w", repository, cause)
	if !policy.SkipFailedNonNPM {
		return nil, wrapped
	}
	// See classifyGoGraphDiscoveryFailure: an unreadable/remote-less local
	// clone is skipped and reported unconditionally, regardless of what it
	// contains, because WB cannot safely inspect origin to find out.
	if looksLikeUnreadableClone(cause) {
		return &GraphDiscoverySkip{
			Repository: repository,
			Reason:     fmt.Sprintf("local clone is unreadable (no usable git remote); skipped rather than aborting the fleet — needs manual repair: %v", cause),
		}, nil
	}
	hasManifest, inspectErr := repositoryContainsLocalNpmManifest(canonical)
	if inspectErr != nil {
		return nil, errors.Join(wrapped, fmt.Errorf("%s: cannot prove failed repository is irrelevant to npm propagation: %w", repository, inspectErr))
	}
	if hasManifest {
		return nil, wrapped
	}
	return &GraphDiscoverySkip{
		Repository: repository,
		Reason:     fmt.Sprintf("remote npm graph inspection failed, but the local repository contains no package.json: %v", cause),
	}, nil
}

func repositoryContainsLocalNpmManifest(root string) (bool, error) {
	if _, err := os.Stat(root); err != nil {
		return false, err
	}
	found := errors.New("package.json found")
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
		if !entry.IsDir() && entry.Name() == "package.json" && !ignoredManifestPath(filepath.ToSlash(path)) {
			return found
		}
		return nil
	})
	if errors.Is(err, found) {
		return true, nil
	}
	return false, err
}

// inspectRepositoryNpmGraph reads every package.json and pnpm-workspace.yaml
// blob at base without checking anything out, exactly like
// inspectRepositoryGoGraph does for go.mod.
func inspectRepositoryNpmGraph(ctx context.Context, repository, canonical, base string, options orchestrate.Options) (struct {
	packages     []npmFleetPackage
	requirements []npmFleetRequirement
}, error) {
	result := struct {
		packages     []npmFleetPackage
		requirements []npmFleetRequirement
	}{}
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "ls-tree", "-r", "--name-only", base)
	if err != nil {
		return result, err
	}
	for _, name := range strings.Split(strings.TrimSpace(output), "\n") {
		if ignoredManifestPath(name) {
			continue
		}
		switch filepath.Base(name) {
		case "package.json":
			contents, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "show", base+":"+name)
			if err != nil {
				return result, err
			}
			pkg, requirements, err := parseNpmPackageJSONManifest(repository, name, []byte(contents))
			if err != nil {
				return result, fmt.Errorf("parse %s: %w", name, err)
			}
			if pkg != nil {
				result.packages = append(result.packages, *pkg)
			}
			result.requirements = append(result.requirements, requirements...)
		case "pnpm-workspace.yaml":
			contents, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "show", base+":"+name)
			if err != nil {
				return result, err
			}
			result.requirements = append(result.requirements, parseNpmWorkspaceRequirements(repository, name, []byte(contents))...)
		}
	}
	return result, nil
}

// npmPackageJSONManifest is the subset of package.json fields the fleet
// graph needs: publish identity and every dependency-field reference.
type npmPackageJSONManifest struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Private              bool              `json:"private"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

// parseNpmPackageJSONManifest decodes one package.json blob into its publish
// identity (nil when private or unnamed — "publishes nothing") and every
// dependency-field requirement it declares.
func parseNpmPackageJSONManifest(repository, manifest string, contents []byte) (*npmFleetPackage, []npmFleetRequirement, error) {
	var shape npmPackageJSONManifest
	if err := json.Unmarshal(contents, &shape); err != nil {
		return nil, nil, err
	}
	var pkg *npmFleetPackage
	if shape.Name != "" && !shape.Private {
		pkg = &npmFleetPackage{Name: shape.Name, Repository: repository, Manifest: manifest, Version: shape.Version}
	}
	fields := []struct {
		name string
		deps map[string]string
	}{
		{"dependencies", shape.Dependencies}, {"devDependencies", shape.DevDependencies},
		{"peerDependencies", shape.PeerDependencies}, {"optionalDependencies", shape.OptionalDependencies},
	}
	var requirements []npmFleetRequirement
	for _, field := range fields {
		names := make([]string, 0, len(field.deps))
		for name := range field.deps {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			requirements = append(requirements, npmFleetRequirement{
				Dependency: name, Version: field.deps[name], ConsumerPackage: shape.Name,
				Repository: repository, Manifest: manifest, Field: field.name,
			})
		}
	}
	return pkg, requirements, nil
}

// parseNpmWorkspaceRequirements decodes every overrides/catalog(s) entry of
// one pnpm-workspace.yaml blob into fleet requirements. These entries have no
// single consuming package — pnpm applies them workspace-wide — so
// ConsumerPackage stays empty; graphFromNpmFleet gives such an edge a
// synthetic, clearly-labeled consumer identity instead of guessing one.
func parseNpmWorkspaceRequirements(repository, manifest string, contents []byte) []npmFleetRequirement {
	refs := scanPnpmWorkspaceRefs(contents)
	requirements := make([]npmFleetRequirement, 0, len(refs))
	for _, ref := range refs {
		field := "pnpm-override"
		switch ref.Section {
		case "catalog":
			field = "pnpm-catalog"
		case "catalogs":
			field = "pnpm-catalog:" + ref.CatalogName
		}
		requirements = append(requirements, npmFleetRequirement{
			Dependency: ref.Key, Version: ref.Value, Repository: repository, Manifest: manifest, Field: field,
		})
	}
	return requirements
}

func (graph npmFleetGraph) Skips() []GraphDiscoverySkip { return graph.discoverySkips }

func (graph npmFleetGraph) requirementsForDependency(dependency string) []fleetRequirement {
	requirements := graph.requirements[dependency]
	result := make([]fleetRequirement, 0, len(requirements))
	for _, requirement := range requirements {
		consumer := requirement.ConsumerPackage
		if consumer == "" {
			consumer = requirement.Repository + " (pnpm workspace)"
		}
		result = append(result, fleetRequirement{ConsumerModule: consumer, Repository: requirement.Repository, Version: requirement.Version})
	}
	return result
}

func (graph npmFleetGraph) validateUniqueModuleDeclarations() error {
	var conflicts []string
	for name, declarations := range graph.packageDeclarations {
		if len(declarations) < 2 {
			continue
		}
		locations := make([]string, 0, len(declarations))
		for _, declaration := range declarations {
			locations = append(locations, declaration.Repository+":"+declaration.Manifest)
		}
		sort.Strings(locations)
		conflicts = append(conflicts, fmt.Sprintf("npm package %s is declared by %s", name, strings.Join(locations, ", ")))
	}
	sort.Strings(conflicts)
	if len(conflicts) > 0 {
		return errors.New(strings.Join(conflicts, "; "))
	}
	return nil
}

func (graph npmFleetGraph) repositoriesForEvents(events []ReleaseEvent) map[string][]Target {
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
				Ecosystem: EcosystemNPM, Dependency: event.Dependency, Version: event.Version, Resolved: event.Version,
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

// coalescedRepositoriesForEvents mirrors goFleetGraph's diamond-sink
// coalescing exactly (see go_graph.go for the detailed rationale): only the
// earliest pending provider-first layer is selected, so a repository that
// consumes both a seed package and an intermediate provider waits and
// receives every release in one PR and one CI build.
func (graph npmFleetGraph) coalescedRepositoriesForEvents(seedEvents, events []ReleaseEvent) (map[string][]Target, []string) {
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

func (graph npmFleetGraph) repositoryAdjacency() map[string][]string {
	sets := map[string]map[string]bool{}
	for dependency, requirements := range graph.requirements {
		provider, exists := graph.packages[dependency]
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

func (graph npmFleetGraph) pendingCarriersBlockTargets(carriers []ReleaseObservation, targets map[string][]Target) bool {
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

func (graph npmFleetGraph) affectedModules(targetsByRepository map[string][]Target) map[string]map[string]bool {
	result := map[string]map[string]bool{}
	for repository, targets := range targetsByRepository {
		for _, target := range targets {
			for _, requirement := range graph.requirements[target.Dependency] {
				if requirement.Repository != repository || requirement.Version == target.Version {
					continue
				}
				consumer := requirement.ConsumerPackage
				if consumer == "" {
					consumer = requirement.Repository + " (pnpm workspace)"
				}
				if result[repository] == nil {
					result[repository] = map[string]bool{}
				}
				result[repository][consumer] = true
			}
		}
	}
	return result
}

func (graph npmFleetGraph) hasExternalConsumers(packageName, repository string) bool {
	for _, requirement := range graph.requirements[packageName] {
		if requirement.Repository != repository {
			return true
		}
	}
	return false
}

// validateAcyclicPropagation mirrors goFleetGraph's cycle rejection exactly:
// a release wave cannot safely order a relevant cross-repository cycle
// without a separate coordinated-version protocol.
func (graph npmFleetGraph) validateAcyclicPropagation(events []ReleaseEvent) error {
	adjacency := map[string]map[string]bool{}
	for dependency, requirements := range graph.requirements {
		provider, internal := graph.packages[dependency]
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
		if provider, exists := graph.packages[event.Dependency]; exists {
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

var (
	_ bumpFleetGraph = goFleetGraph{}
	_ bumpFleetGraph = npmFleetGraph{}
)
