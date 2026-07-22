package deps

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

// BuildGraph scans selected repositories once and returns canonical evidence.
func BuildGraph(ctx context.Context, repositories []Repository, options GraphOptions) (Graph, error) {
	if options.Ecosystem == "" {
		options.Ecosystem = EcosystemGo
	}
	if options.Ecosystem != EcosystemGo {
		return Graph{}, fmt.Errorf("dependency graph currently supports only the go ecosystem")
	}
	lifecycle, err := orchestrate.Normalize(orchestrate.Options{
		GitHubDir: options.GitHubDir, Operation: "deps-graph-go", Ref: options.Ref,
		Parallel: options.Parallel, Timeout: options.Timeout, Retry: options.Retry, DryRun: true,
	})
	if err != nil {
		return Graph{}, err
	}
	filters, err := normalizeGraphDependencies(options.Dependencies)
	if err != nil {
		return Graph{}, err
	}
	discovered, err := discoverGoFleetGraph(ctx, repositories, lifecycle, nil)
	if err != nil {
		return Graph{}, err
	}
	graph := graphFromGoFleet(discovered, repositories, lifecycle.Ref, filters)
	return graph, nil
}

func normalizeGraphDependencies(values []string) ([]string, error) {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("dependency filters must not be empty")
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func graphFromGoFleet(discovered goFleetGraph, selected []Repository, ref string, filters []string) Graph {
	graph := Graph{SchemaVersion: 1, Ecosystem: EcosystemGo, BaseRef: ref, Filters: GraphFilters{Dependencies: append([]string(nil), filters...)}}
	filterSet := map[string]bool{}
	for _, dependency := range filters {
		filterSet[dependency] = true
	}
	keepAll := len(filterSet) == 0
	moduleSet := map[string]bool{}
	repositorySet := map[string]bool{}
	externalDependencies := map[string]bool{}
	ambiguousProviders := map[string]bool{}
	selections := map[string]bool{}
	for dependency, requirements := range discovered.requirements {
		if !keepAll && !filterSet[dependency] {
			continue
		}
		provider, internal := discovered.modules[dependency]
		declarations := graphModuleDeclarations(discovered, dependency)
		ambiguous := len(declarations) > 1
		if len(declarations) == 0 {
			externalDependencies[dependency] = true
		}
		for _, requirement := range requirements {
			item := GraphRequirement{
				Dependency: dependency, Version: requirement.Version,
				ConsumerModule: requirement.ConsumerModule, ConsumerRepository: requirement.Repository,
				Manifest: filepath.ToSlash(requirement.Manifest), Indirect: requirement.Indirect,
			}
			if ambiguous {
				ambiguousProviders[dependency] = true
				moduleSet[dependency] = true
				for _, declaration := range declarations {
					item.ProviderCandidates = append(item.ProviderCandidates, declaration.Repository+":"+filepath.ToSlash(declaration.Manifest))
					repositorySet[declaration.Repository] = true
				}
				sort.Strings(item.ProviderCandidates)
			}
			if internal {
				item.ProviderModule = provider.Path
				item.ProviderRepository = provider.Repository
				moduleSet[provider.Path] = true
				repositorySet[provider.Repository] = true
			}
			moduleSet[requirement.ConsumerModule] = true
			repositorySet[requirement.Repository] = true
			selections[dependency+"\x00"+requirement.Version] = true
			graph.Requirements = append(graph.Requirements, item)
		}
	}
	if keepAll {
		for _, repository := range selected {
			if !repository.Archived {
				repositorySet[repository.Slug] = true
			}
		}
		for module := range discovered.moduleDeclarations {
			moduleSet[module] = true
		}
		for module := range discovered.modules {
			moduleSet[module] = true
		}
	}
	declarationsByModule := discovered.moduleDeclarations
	if len(declarationsByModule) == 0 {
		declarationsByModule = map[string][]goFleetModule{}
		for path, module := range discovered.modules {
			declarationsByModule[path] = []goFleetModule{module}
		}
	}
	for modulePath, declarations := range declarationsByModule {
		if !moduleSet[modulePath] {
			continue
		}
		for _, module := range declarations {
			graph.Modules = append(graph.Modules, GraphModule{Path: module.Path, Repository: module.Repository, Manifest: filepath.ToSlash(module.Manifest)})
		}
	}
	modulesByRepository := map[string][]string{}
	for _, module := range graph.Modules {
		modulesByRepository[module.Repository] = append(modulesByRepository[module.Repository], module.Path)
	}
	for repository := range repositorySet {
		organization, _, _ := strings.Cut(repository, "/")
		modules := append([]string(nil), modulesByRepository[repository]...)
		sort.Strings(modules)
		graph.Repositories = append(graph.Repositories, GraphRepository{Slug: repository, Organization: organization, Modules: modules})
	}
	sort.Slice(graph.Repositories, func(i, j int) bool { return graph.Repositories[i].Slug < graph.Repositories[j].Slug })
	sort.Slice(graph.Modules, func(i, j int) bool {
		if graph.Modules[i].Path == graph.Modules[j].Path {
			return graph.Modules[i].Repository < graph.Modules[j].Repository
		}
		return graph.Modules[i].Path < graph.Modules[j].Path
	})
	sort.Slice(graph.Requirements, func(i, j int) bool {
		left, right := graph.Requirements[i], graph.Requirements[j]
		if left.Dependency != right.Dependency {
			return left.Dependency < right.Dependency
		}
		if left.Version != right.Version {
			return left.Version < right.Version
		}
		if left.ConsumerRepository != right.ConsumerRepository {
			return left.ConsumerRepository < right.ConsumerRepository
		}
		if left.ConsumerModule != right.ConsumerModule {
			return left.ConsumerModule < right.ConsumerModule
		}
		return left.Manifest < right.Manifest
	})
	internal := 0
	for _, requirement := range graph.Requirements {
		if requirement.ProviderRepository != "" {
			internal++
		}
	}
	graph.Summary = GraphSummary{
		Repositories: len(graph.Repositories), Modules: len(graph.Modules), Requirements: len(graph.Requirements),
		InternalRequirements: internal, ExternalDependencies: len(externalDependencies), Selections: len(selections),
		AmbiguousProviders: len(ambiguousProviders),
	}
	return graph
}

func graphModuleDeclarations(graph goFleetGraph, module string) []goFleetModule {
	if declarations := graph.moduleDeclarations[module]; len(declarations) > 0 {
		return declarations
	}
	if declaration, exists := graph.modules[module]; exists {
		return []goFleetModule{declaration}
	}
	return nil
}
