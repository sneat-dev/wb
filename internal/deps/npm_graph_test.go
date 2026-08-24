package deps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGraphFromNpmFleetFiltersExactDependencyWithProviderContext(t *testing.T) {
	t.Parallel()
	discovered := npmFleetGraph{
		packages: map[string]npmFleetPackage{
			"@sneat/core": {Name: "@sneat/core", Repository: "sneat-co/sneat-libs", Manifest: "package.json"},
			"@sneat/app":  {Name: "@sneat/app", Repository: "sneat-co/sneat-apps", Manifest: "package.json"},
		},
		requirements: map[string][]npmFleetRequirement{
			"@sneat/core":  {{Dependency: "@sneat/core", Version: "1.0.0", ConsumerPackage: "@sneat/app", Repository: "sneat-co/sneat-apps", Manifest: "package.json", Field: "dependencies"}},
			"@sneat/other": {{Dependency: "@sneat/other", Version: "2.0.0", ConsumerPackage: "@sneat/app", Repository: "sneat-co/sneat-apps", Manifest: "package.json", Field: "dependencies"}},
		},
	}
	graph := graphFromNpmFleet(discovered, nil, "main", []string{"@sneat/core"})
	if graph.Ecosystem != EcosystemNPM {
		t.Fatalf("ecosystem = %q, want npm", graph.Ecosystem)
	}
	if graph.Summary.Repositories != 2 || graph.Summary.Modules != 2 || graph.Summary.Requirements != 1 {
		t.Fatalf("summary = %+v", graph.Summary)
	}
	if got := graph.Requirements[0]; got.ProviderRepository != "sneat-co/sneat-libs" || got.Dependency != "@sneat/core" {
		t.Fatalf("requirement = %+v", got)
	}
}

// TestGraphFromNpmFleetReportsAmbiguousProviderWithoutGuessing pins the
// design decision this feature required: unlike a Go module path
// (github.com/owner/repo/...), an npm package name carries no
// owner/repository convention, so when the same package name is declared by
// more than one repository, npm support must not guess which one is
// canonical — it reports the ambiguity exactly like the Go graph's own
// fallback path already does when a Go module's owner/repo convention does
// not resolve it either.
func TestGraphFromNpmFleetReportsAmbiguousProviderWithoutGuessing(t *testing.T) {
	t.Parallel()
	declarations := []npmFleetPackage{
		{Name: "@sneat/core", Repository: "sneat-co/sneat-libs", Manifest: "package.json"},
		{Name: "@sneat/core", Repository: "sneat-co/sneat-libs-copy", Manifest: "package.json"},
	}
	discovered := npmFleetGraph{
		packages:            map[string]npmFleetPackage{},
		packageDeclarations: map[string][]npmFleetPackage{"@sneat/core": declarations},
		requirements: map[string][]npmFleetRequirement{
			"@sneat/core": {{Dependency: "@sneat/core", Version: "1.0.0", ConsumerPackage: "@sneat/app", Repository: "sneat-co/sneat-apps", Manifest: "package.json"}},
		},
	}
	graph := graphFromNpmFleet(discovered, nil, "main", nil)
	if graph.Summary.AmbiguousProviders != 1 || len(graph.Requirements[0].ProviderCandidates) != 2 {
		t.Fatalf("graph = %+v", graph)
	}
	if graph.Requirements[0].ProviderRepository != "" {
		t.Fatalf("ambiguous npm provider must not be resolved by guessing: %+v", graph.Requirements[0])
	}
	if err := discovered.validateUniqueModuleDeclarations(); err == nil || !strings.Contains(err.Error(), "sneat-co/sneat-libs-copy:package.json") {
		t.Fatalf("mutation validation error = %v", err)
	}
}

// TestGraphFromNpmFleetLabelsWorkspaceOnlyOverrideConsumer covers a
// pnpm-workspace.yaml override with no single consuming package.json: pnpm
// applies it workspace-wide, so ConsumerPackage is empty and the graph must
// still surface the requirement under a clearly-labeled synthetic consumer
// rather than dropping it or crashing on an empty node identity.
func TestGraphFromNpmFleetLabelsWorkspaceOnlyOverrideConsumer(t *testing.T) {
	t.Parallel()
	discovered := npmFleetGraph{
		packages: map[string]npmFleetPackage{
			"@sneat/core": {Name: "@sneat/core", Repository: "sneat-co/sneat-libs", Manifest: "package.json"},
		},
		requirements: map[string][]npmFleetRequirement{
			"@sneat/core": {{Dependency: "@sneat/core", Version: "1.0.0", ConsumerPackage: "", Repository: "sneat-co/sneat-apps", Manifest: "pnpm-workspace.yaml", Field: "pnpm-override"}},
		},
	}
	graph := graphFromNpmFleet(discovered, nil, "main", nil)
	if len(graph.Requirements) != 1 {
		t.Fatalf("requirements = %+v", graph.Requirements)
	}
	if graph.Requirements[0].ConsumerModule != "sneat-co/sneat-apps (pnpm workspace)" {
		t.Fatalf("consumer module = %q", graph.Requirements[0].ConsumerModule)
	}
	if graph.Requirements[0].ConsumerRepository != "sneat-co/sneat-apps" {
		t.Fatalf("consumer repository = %q", graph.Requirements[0].ConsumerRepository)
	}
}

func TestBuildGraphPreservesNpmManifestEvidenceIncludingPnpmOverrides(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	provider := seedNpmGraphRepository(t, root, githubDir, "sneat-libs", map[string]string{
		"package.json": npmPackageJSONWithDependency("@sneat/core", "lodash", "^4.17.21"),
	})
	consumer := seedNpmGraphRepository(t, root, githubDir, "sneat-apps", map[string]string{
		"package.json":        "{\n  \"name\": \"@sneat/apps\",\n  \"dependencies\": {\n    \"@sneat/core\": \"1.0.0\"\n  }\n}\n",
		"pnpm-workspace.yaml": "packages:\n  - \"packages/*\"\n\noverrides:\n  \"@sneat/core\": \"1.0.0\"\n",
	})

	graph, err := BuildGraph(context.Background(), []Repository{
		{Slug: "sneat-co/sneat-libs", Path: provider},
		{Slug: "sneat-co/sneat-apps", Path: consumer},
	}, GraphOptions{
		Ecosystem: EcosystemNPM, GitHubDir: githubDir, Ref: "main", Parallel: 2, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if graph.Summary.Repositories != 2 {
		t.Fatalf("graph summary = %+v", graph.Summary)
	}
	var packageJSONRequirement, workspaceRequirement *GraphRequirement
	for index := range graph.Requirements {
		requirement := &graph.Requirements[index]
		if requirement.Dependency != "@sneat/core" {
			continue
		}
		switch requirement.Manifest {
		case "package.json":
			packageJSONRequirement = requirement
		case "pnpm-workspace.yaml":
			workspaceRequirement = requirement
		}
	}
	if packageJSONRequirement == nil || packageJSONRequirement.ProviderRepository != "sneat-co/sneat-libs" {
		t.Fatalf("package.json requirement = %+v", packageJSONRequirement)
	}
	// This is the exact scenario the pnpm-workspace.yaml overrides support
	// exists for: pnpm 11 no longer reads package.json's legacy
	// pnpm.overrides field, so the fleet graph must see the override edge
	// too, with the same provider resolved from the same package identity.
	if workspaceRequirement == nil || workspaceRequirement.ProviderRepository != "sneat-co/sneat-libs" {
		t.Fatalf("pnpm-workspace.yaml override requirement = %+v", workspaceRequirement)
	}

	// Release order must place the provider before the consumer, exactly
	// like the Go graph's provider-first layering — this is what lets `deps
	// bump npm` (and `deps graph --ecosystem npm`'s own release-order report)
	// sequence a coordinated npm release the same way Go's already does.
	order := graph.Order
	if len(order.Layers) != 2 {
		t.Fatalf("order = %+v, want provider and consumer in separate layers", order)
	}
	if order.Layers[0].Repositories[0] != "sneat-co/sneat-libs" || order.Layers[1].Repositories[0] != "sneat-co/sneat-apps" {
		t.Fatalf("order = %+v, want sneat-libs before sneat-apps", order)
	}
}

func TestBuildGraphIgnoresTestdataPackageJSONFixtures(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repository := seedNpmGraphRepository(t, root, githubDir, "fixture-safe", map[string]string{
		"package.json": "{\n  \"name\": \"@sneat/fixture-safe\"\n}\n",
		"internal/example/testdata/broken/package.json": "{ this is deliberately not JSON }\n",
	})

	graph, err := BuildGraph(context.Background(), []Repository{{Slug: "sneat-co/fixture-safe", Path: repository}}, GraphOptions{
		Ecosystem: EcosystemNPM, GitHubDir: githubDir, Ref: "main", Parallel: 1, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("BuildGraph() error = %v", err)
	}
	if len(graph.Modules) != 1 {
		t.Fatalf("modules = %+v, want only the production package", graph.Modules)
	}
	if got := graph.Modules[0]; got.Path != "@sneat/fixture-safe" || got.Manifest != "package.json" {
		t.Fatalf("production package = %+v", got)
	}
}

func TestBuildGraphIgnoresDistPackageJSONButKeepsDistinguishedSource(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repository := seedNpmGraphRepository(t, root, githubDir, "generated-safe", map[string]string{
		"package.json":               "{\n  \"name\": \"@sneat/generated-safe\"\n}\n",
		"dist/broken/package.json":   "{ this is generated output, not JSON }\n",
		"distinguished/package.json": "{\n  \"name\": \"@sneat/distinguished-source\"\n}\n",
	})

	graph, err := BuildGraph(context.Background(), []Repository{{Slug: "sneat-co/generated-safe", Path: repository}}, GraphOptions{
		Ecosystem: EcosystemNPM, GitHubDir: githubDir, Ref: "main", Parallel: 1, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("BuildGraph() error = %v", err)
	}
	if len(graph.Modules) != 2 {
		t.Fatalf("modules = %+v, want production and distinguished source packages", graph.Modules)
	}
	got := map[string]string{}
	for _, module := range graph.Modules {
		got[module.Path] = module.Manifest
	}
	if got["@sneat/generated-safe"] != "package.json" || got["@sneat/distinguished-source"] != "distinguished/package.json" {
		t.Fatalf("modules = %+v, want distinguished source package retained", graph.Modules)
	}
}

func seedNpmGraphRepository(t *testing.T, root, githubDir, name string, files map[string]string) string {
	t.Helper()
	seed := filepath.Join(root, name+"-seed")
	remote := filepath.Join(root, name+".git")
	canonical := filepath.Join(githubDir, "sneat-co", name)
	for path, body := range files {
		writeTestFile(t, filepath.Join(seed, path), body)
	}
	runTestGit(t, seed, "init", "-b", "main")
	runTestGit(t, seed, "config", "user.name", "WB Test")
	runTestGit(t, seed, "config", "user.email", "wb@example.test")
	runTestGit(t, seed, "add", "-A")
	runTestGit(t, seed, "commit", "-m", "initial")
	runTestGit(t, root, "clone", "--bare", seed, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "clone", remote, canonical)
	return canonical
}
