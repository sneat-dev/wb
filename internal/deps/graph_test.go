package deps

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGraphProjectionsShareCanonicalEvidence(t *testing.T) {
	t.Parallel()
	graph := graphFixture()
	repositories, err := graph.Project(GraphViewRepositories)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories.Nodes) != 4 || len(repositories.Edges) != 2 {
		t.Fatalf("repository projection = %+v", repositories)
	}
	provider := projectionNode(repositories, "acme/provider")
	if provider.CodeGrapherURL != "https://codegrapher.dev/github.com/acme/provider" || provider.GitHubURL != "https://github.com/acme/provider" {
		t.Fatalf("provider links = %+v", provider)
	}
	dependencies, err := graph.Project(GraphViewDependencies)
	if err != nil {
		t.Fatal(err)
	}
	if len(dependencies.Edges) != 3 || !projectionHasLabel(dependencies, "example.com/external") {
		t.Fatalf("dependency projection = %+v", dependencies)
	}
	selections, err := graph.Project(GraphViewSelections)
	if err != nil {
		t.Fatal(err)
	}
	if !projectionHasStatus(selections, "example.com/provider@v0.9.0", "behind") ||
		!projectionHasStatus(selections, "example.com/provider@v1.0.0", "fleet-highest") {
		t.Fatalf("selection projection = %+v", selections)
	}
	again, err := graph.Project(GraphViewSelections)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selections, again) {
		t.Fatal("projection identities or order are not deterministic")
	}
}

func TestGraphFromGoFleetFiltersExactDependencyWithProviderContext(t *testing.T) {
	t.Parallel()
	discovered := goFleetGraph{
		modules: map[string]goFleetModule{
			"example.com/provider": {Path: "example.com/provider", Repository: "acme/provider", Manifest: "go.mod"},
			"example.com/app":      {Path: "example.com/app", Repository: "acme/app", Manifest: "go.mod"},
		},
		requirements: map[string][]goFleetRequirement{
			"example.com/provider": {{Dependency: "example.com/provider", Version: "v1.0.0", ConsumerModule: "example.com/app", Repository: "acme/app", Manifest: "go.mod"}},
			"example.com/other":    {{Dependency: "example.com/other", Version: "v2.0.0", ConsumerModule: "example.com/app", Repository: "acme/app", Manifest: "go.mod"}},
		},
	}
	graph := graphFromGoFleet(discovered, nil, "main", []string{"example.com/provider"})
	if graph.Summary.Repositories != 2 || graph.Summary.Modules != 2 || graph.Summary.Requirements != 1 {
		t.Fatalf("summary = %+v", graph.Summary)
	}
	if got := graph.Requirements[0]; got.ProviderRepository != "acme/provider" || got.Dependency != "example.com/provider" {
		t.Fatalf("requirement = %+v", got)
	}
}

func TestGraphPreservesAmbiguousProvidersWhileMutationValidationRejectsThem(t *testing.T) {
	t.Parallel()
	declarations := []goFleetModule{
		{Path: "example.com/provider", Repository: "acme/provider", Manifest: "go.mod"},
		{Path: "example.com/provider", Repository: "acme/provider-copy", Manifest: "go.mod"},
	}
	discovered := goFleetGraph{
		modules:            map[string]goFleetModule{},
		moduleDeclarations: map[string][]goFleetModule{"example.com/provider": declarations},
		requirements: map[string][]goFleetRequirement{
			"example.com/provider": {{Dependency: "example.com/provider", Version: "v1.0.0", ConsumerModule: "example.com/app", Repository: "acme/app", Manifest: "go.mod"}},
		},
	}
	graph := graphFromGoFleet(discovered, nil, "main", nil)
	if graph.Summary.AmbiguousProviders != 1 || graph.Summary.ExternalDependencies != 0 || len(graph.Requirements[0].ProviderCandidates) != 2 {
		t.Fatalf("graph = %+v", graph)
	}
	if err := discovered.validateUniqueModuleDeclarations(); err == nil || !strings.Contains(err.Error(), "acme/provider-copy:go.mod") {
		t.Fatalf("mutation validation error = %v", err)
	}
}

func TestGraphUsesRepositoryMatchingDeclarationWithoutHidingAmbiguity(t *testing.T) {
	t.Parallel()
	const module = "github.com/acme/provider"
	declarations := []goFleetModule{
		{Path: module, Repository: "acme/provider", Manifest: "go.mod"},
		{Path: module, Repository: "acme/provider-copy", Manifest: "go.mod"},
	}
	provider, ok := canonicalGoModuleDeclaration(module, declarations)
	if !ok || provider.Repository != "acme/provider" {
		t.Fatalf("canonical provider = %+v, %v", provider, ok)
	}
	discovered := goFleetGraph{
		modules:            map[string]goFleetModule{module: provider},
		moduleDeclarations: map[string][]goFleetModule{module: declarations},
		requirements: map[string][]goFleetRequirement{
			module: {{Dependency: module, Version: "v1.0.0", ConsumerModule: "example.com/app", Repository: "acme/app", Manifest: "go.mod"}},
		},
	}
	graph := graphFromGoFleet(discovered, nil, "main", nil)
	requirement := graph.Requirements[0]
	if requirement.ProviderRepository != "acme/provider" || len(requirement.ProviderCandidates) != 2 || graph.Summary.AmbiguousProviders != 1 {
		t.Fatalf("graph = %+v", graph)
	}
	if err := discovered.validateUniqueModuleDeclarations(); err == nil {
		t.Fatal("mutation validation accepted duplicate declarations")
	}
}

func TestBuildGraphPreservesManifestEvidence(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repositories := []Repository{
		newBumpRepository(t, root, githubDir, "provider", "module example.com/provider\n\ngo 1.24\n"),
		newBumpRepository(t, root, githubDir, "consumer", "module example.com/consumer\n\ngo 1.24\n\nrequire (\n example.com/provider v1.0.0\n example.com/external v0.2.0 // indirect\n)\n"),
	}
	graph, err := BuildGraph(context.Background(), repositories, GraphOptions{
		Ecosystem: EcosystemGo, GitHubDir: githubDir, Ref: "main", Parallel: 2, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if graph.Summary.Repositories != 2 || graph.Summary.Modules != 2 || graph.Summary.Requirements != 2 || graph.Summary.ExternalDependencies != 1 {
		t.Fatalf("graph summary = %+v", graph.Summary)
	}
	if graph.Requirements[0].ConsumerModule != "example.com/consumer" || graph.Requirements[0].Manifest != "go.mod" {
		t.Fatalf("requirements = %+v", graph.Requirements)
	}
	var indirect bool
	for _, requirement := range graph.Requirements {
		if requirement.Dependency == "example.com/external" {
			indirect = requirement.Indirect
		}
	}
	if !indirect {
		t.Fatalf("indirect evidence was lost: %+v", graph.Requirements)
	}
}

func TestGraphReportsAreDeterministicAndSelfContained(t *testing.T) {
	t.Parallel()
	graph := graphFixture()
	directory := t.TempDir()
	paths, err := WriteGraphReports(directory, graph, GraphViewSelections)
	if err != nil {
		t.Fatal(err)
	}
	first := map[string][]byte{}
	for _, path := range []string{paths.Markdown, paths.YAML, paths.JSON, paths.SVG, paths.HTML} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(contents) == 0 {
			t.Fatalf("%s is empty", path)
		}
		first[filepath.Base(path)] = contents
	}
	if _, err := WriteGraphReports(directory, graph, GraphViewSelections); err != nil {
		t.Fatal(err)
	}
	for name, want := range first {
		got, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s changed between identical renders", name)
		}
	}
	htmlContents := string(first["deps-graph.html"])
	for _, expected := range []string{
		`data-select-view="repos"`, `data-select-view="dependencies"`, `data-select-view="selections"`,
		"Canonical requirement evidence", "Search graph nodes", "Explore code in CodeGrapher",
		`href="https://codegrapher.dev/github.com/acme/consumer"`, "Highlight organization", "data-inspector-connected",
	} {
		if !strings.Contains(htmlContents, expected) {
			t.Errorf("HTML does not contain %q", expected)
		}
	}
	if strings.Contains(htmlContents, "<script src=") || strings.Contains(htmlContents, "<link rel=") {
		t.Fatal("HTML report contains an external asset")
	}
}

func TestGraphRepositoryLinksAreDeterministicAndRejectInvalidSlugs(t *testing.T) {
	t.Parallel()
	githubURL, codeGrapherURL := graphRepositoryLinks("acme/repo with space")
	if githubURL != "https://github.com/acme/repo%20with%20space" || codeGrapherURL != "https://codegrapher.dev/github.com/acme/repo%20with%20space" {
		t.Fatalf("links = %q, %q", githubURL, codeGrapherURL)
	}
	for _, value := range []string{"", "acme", "acme/repo/extra", "/repo", "acme/"} {
		if githubURL, codeGrapherURL := graphRepositoryLinks(value); githubURL != "" || codeGrapherURL != "" {
			t.Errorf("graphRepositoryLinks(%q) = %q, %q", value, githubURL, codeGrapherURL)
		}
	}
}

func TestGraphSVGEscapesLabelsAndRendersCycles(t *testing.T) {
	t.Parallel()
	graph := Graph{
		SchemaVersion: 1, Ecosystem: EcosystemGo, BaseRef: "main",
		Repositories: []GraphRepository{{Slug: "acme/<a>", Organization: "acme"}, {Slug: "acme/b", Organization: "acme"}},
		Requirements: []GraphRequirement{
			{Dependency: "example.com/a<script>", Version: "v1.0.0", ConsumerModule: "example.com/b", ConsumerRepository: "acme/b", ProviderRepository: "acme/<a>"},
			{Dependency: "example.com/b", Version: "v1.0.0", ConsumerModule: "example.com/a", ConsumerRepository: "acme/<a>", ProviderRepository: "acme/b"},
		},
	}
	svg, err := graph.SVG(GraphViewRepositories)
	if err != nil {
		t.Fatal(err)
	}
	value := string(svg)
	if strings.Contains(value, "<a>") || strings.Contains(value, "<script>") || !strings.Contains(value, "acme/&lt;a&gt;") {
		t.Fatalf("unsafe or missing escaped SVG label:\n%s", value)
	}
	if strings.Count(value, `class="edge`) < 2 || !strings.Contains(value, `role="img"`) || !strings.Contains(value, `tabindex="0"`) || !strings.Contains(value, "Release wave 00") {
		t.Fatalf("cycle or accessibility metadata missing:\n%s", value)
	}
}

func TestParseGraphViewAndOutputRejectUnknownValues(t *testing.T) {
	t.Parallel()
	if _, err := ParseGraphView("modules"); err == nil {
		t.Fatal("unknown view was accepted")
	}
	if _, err := graphFixture().Output("dot", GraphViewRepositories); err == nil {
		t.Fatal("unknown output format was accepted")
	}
}

func projectionHasLabel(projection GraphProjection, label string) bool {
	for _, node := range projection.Nodes {
		if node.Label == label {
			return true
		}
	}
	return false
}

func projectionHasStatus(projection GraphProjection, label, status string) bool {
	for _, node := range projection.Nodes {
		if node.Label == label && node.Status == status {
			return true
		}
	}
	return false
}

func projectionNode(projection GraphProjection, label string) GraphProjectionNode {
	for _, node := range projection.Nodes {
		if node.Label == label {
			return node
		}
	}
	return GraphProjectionNode{}
}

func graphFixture() Graph {
	return Graph{
		SchemaVersion: 1, Ecosystem: EcosystemGo, BaseRef: "main",
		Summary: GraphSummary{Repositories: 4, Modules: 3, Requirements: 3, InternalRequirements: 2, ExternalDependencies: 1, Selections: 3},
		Repositories: []GraphRepository{
			{Slug: "acme/provider", Organization: "acme", Modules: []string{"example.com/provider"}},
			{Slug: "acme/consumer", Organization: "acme", Modules: []string{"example.com/consumer"}},
			{Slug: "acme/app", Organization: "acme", Modules: []string{"example.com/app"}},
			{Slug: "acme/isolated", Organization: "acme"},
		},
		Modules: []GraphModule{
			{Path: "example.com/app", Repository: "acme/app", Manifest: "go.mod"},
			{Path: "example.com/consumer", Repository: "acme/consumer", Manifest: "go.mod"},
			{Path: "example.com/provider", Repository: "acme/provider", Manifest: "go.mod"},
		},
		Requirements: []GraphRequirement{
			{Dependency: "example.com/external", Version: "v0.3.0", ConsumerModule: "example.com/consumer", ConsumerRepository: "acme/consumer", Manifest: "go.mod", Indirect: true},
			{Dependency: "example.com/provider", Version: "v0.9.0", ConsumerModule: "example.com/app", ConsumerRepository: "acme/app", Manifest: "go.mod", ProviderModule: "example.com/provider", ProviderRepository: "acme/provider"},
			{Dependency: "example.com/provider", Version: "v1.0.0", ConsumerModule: "example.com/consumer", ConsumerRepository: "acme/consumer", Manifest: "go.mod", ProviderModule: "example.com/provider", ProviderRepository: "acme/provider"},
		},
	}
}
