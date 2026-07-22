package deps

import (
	"context"
	"errors"
	"fmt"
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

func discoverGoFleetGraph(ctx context.Context, repositories []Repository, options orchestrate.Options) (goFleetGraph, error) {
	graph := goFleetGraph{
		modules: map[string]goFleetModule{}, moduleDeclarations: map[string][]goFleetModule{}, requirements: map[string][]goFleetRequirement{},
		repositoryModules: map[string][]string{},
	}
	type repositoryGraph struct {
		modules      []goFleetModule
		requirements []goFleetRequirement
	}
	results := make([]repositoryGraph, len(repositories))
	errorsByRepository := make([]error, len(repositories))
	workers := options.Parallel
	if workers > len(repositories) {
		workers = len(repositories)
	}
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan int)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				repository := repositories[index]
				if repository.Archived {
					continue
				}
				owner, name, ok := strings.Cut(repository.Slug, "/")
				if !ok || owner == "" || name == "" {
					errorsByRepository[index] = fmt.Errorf("invalid repository slug %q", repository.Slug)
					continue
				}
				canonical := repository.Path
				if canonical == "" {
					canonical = filepath.Join(options.GitHubDir, owner, name)
				}
				if err := orchestrate.EnsureCanonical(ctx, repository, canonical, options); err != nil {
					errorsByRepository[index] = fmt.Errorf("%s: %w", repository.Slug, err)
					continue
				}
				result, err := inspectRepositoryGoGraph(ctx, repository.Slug, canonical, "origin/"+options.Ref, options)
				results[index] = result
				if err != nil {
					errorsByRepository[index] = fmt.Errorf("%s: %w", repository.Slug, err)
				}
			}
		}()
	}
	for index := range repositories {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	var discoveryErrors []error
	for _, err := range errorsByRepository {
		if err != nil {
			discoveryErrors = append(discoveryErrors, err)
		}
	}
	if len(discoveryErrors) > 0 {
		return graph, errors.Join(discoveryErrors...)
	}
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
	}
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
		if canonical, ok := canonicalGoModuleDeclaration(module, graph.moduleDeclarations[module]); ok {
			graph.modules[module] = canonical
		}
	}
	return graph, nil
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
}, error) {
	result := struct {
		modules      []goFleetModule
		requirements []goFleetRequirement
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

func (graph goFleetGraph) affectedModules(events []ReleaseEvent) map[string]map[string]bool {
	result := map[string]map[string]bool{}
	for _, event := range events {
		for _, requirement := range graph.requirements[event.Dependency] {
			if requirement.Version == event.Version {
				continue
			}
			if result[requirement.Repository] == nil {
				result[requirement.Repository] = map[string]bool{}
			}
			result[requirement.Repository][requirement.ConsumerModule] = true
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
