package deps

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestRepositoryOrderLayersNineLevelFleet uses a fixture shaped like the real
// DALgo rollout: nine provider-first levels, a wide fan-out onto one shared
// module, an isolated repository, and a skip-level requirement from the last
// level straight onto the first. The skip-level edge is the part a two-node
// fixture cannot catch: a shortest-path layering would put the top repository
// in layer 1 instead of layer 8.
func TestRepositoryOrderLayersNineLevelFleet(t *testing.T) {
	t.Parallel()
	graph := nineLevelGraphFixture()
	order := graph.RepositoryOrder()
	if len(order.Layers) != 9 {
		t.Fatalf("layers = %d, want 9: %+v", len(order.Layers), order.Layers)
	}
	if len(order.Cycles) != 0 {
		t.Fatalf("acyclic fleet reported cycles: %+v", order.Cycles)
	}
	expected := map[string]int{
		"strongo/strongoapp":          0,
		"acme/unrelated":              0,
		"sneat-co/sneat-go-core":      1,
		"sneat-co/sneat-auth-go":      2,
		"sneat-co/sneat-spaceus":      3,
		"sneat-co/sneat-core-modules": 4,
		"sneat-co/sneat-go-backend":   6,
		"sneat-co/sneat-bots":         7,
		"sneat-co/sneat-go":           8,
	}
	for index := range fanOutRepositories {
		expected[fanOutSlug(index)] = 5
	}
	actual := map[string]int{}
	for _, layer := range order.Layers {
		for _, repository := range layer.Repositories {
			actual[repository] = layer.Index
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		for repository, want := range expected {
			if got := actual[repository]; got != want {
				t.Errorf("%s is in layer %d, want %d", repository, got, want)
			}
		}
		t.Fatalf("layering = %+v", order.Layers)
	}
	if got := len(order.Layers[5].Repositories); got != fanOutRepositories {
		t.Fatalf("layer 5 holds %d repositories, want %d", got, fanOutRepositories)
	}
	if got := len(order.Repositories()); got != len(expected) {
		t.Fatalf("ordered repositories = %d, want %d", got, len(expected))
	}
	if again := graph.RepositoryOrder(); !reflect.DeepEqual(again, order) {
		t.Fatal("layering is not deterministic across identical calls")
	}
}

func TestRepositoryOrderGroupsCycleIntoOneLayerAndNamesThePath(t *testing.T) {
	t.Parallel()
	graph := Graph{
		Repositories: []GraphRepository{
			{Slug: "acme/base"}, {Slug: "acme/a"}, {Slug: "acme/b"}, {Slug: "acme/consumer"},
		},
		Requirements: []GraphRequirement{
			internalRequirement("acme/base", "acme/a"),
			internalRequirement("acme/a", "acme/b"),
			internalRequirement("acme/b", "acme/a"),
			internalRequirement("acme/b", "acme/consumer"),
		},
	}
	order := graph.RepositoryOrder()
	if len(order.Cycles) != 1 {
		t.Fatalf("cycles = %+v", order.Cycles)
	}
	cycle := order.Cycles[0]
	if cycle.Path != "acme/a -> acme/b -> acme/a" {
		t.Errorf("cycle path = %q", cycle.Path)
	}
	if !reflect.DeepEqual(cycle.Repositories, []string{"acme/a", "acme/b"}) {
		t.Errorf("cycle repositories = %v", cycle.Repositories)
	}
	layerOf := map[string]int{}
	for _, layer := range order.Layers {
		for _, repository := range layer.Repositories {
			layerOf[repository] = layer.Index
		}
	}
	if layerOf["acme/a"] != layerOf["acme/b"] {
		t.Errorf("cycle members were separated: %+v", order.Layers)
	}
	if cycle.Layer != layerOf["acme/a"] {
		t.Errorf("cycle layer = %d, member layer = %d", cycle.Layer, layerOf["acme/a"])
	}
	if layerOf["acme/base"] >= layerOf["acme/a"] || layerOf["acme/consumer"] <= layerOf["acme/b"] {
		t.Errorf("cycle neighbours are misordered: %+v", order.Layers)
	}
	if len(layerOf) != 4 {
		t.Fatalf("a repository was dropped by cycle handling: %+v", order.Layers)
	}
}

func TestRepositoryOrderKeepsIsolatedRepositoriesInTheFirstLayer(t *testing.T) {
	t.Parallel()
	graph := Graph{
		Repositories: []GraphRepository{{Slug: "acme/solo"}, {Slug: "acme/multi"}},
		Requirements: []GraphRequirement{
			internalRequirement("acme/multi", "acme/multi"),
			{Dependency: "example.com/external", Version: "v1.0.0", ConsumerRepository: "acme/solo"},
		},
	}
	order := graph.RepositoryOrder()
	if len(order.Layers) != 1 || len(order.Layers[0].Repositories) != 2 {
		t.Fatalf("layers = %+v", order.Layers)
	}
	if len(order.Cycles) != 0 {
		t.Fatalf("a same-repository requirement was reported as a cycle: %+v", order.Cycles)
	}
}

func TestGraphFromGoFleetPublishesReleaseOrderInReports(t *testing.T) {
	t.Parallel()
	discovered := goFleetGraph{
		modules: map[string]goFleetModule{
			"example.com/provider": {Path: "example.com/provider", Repository: "acme/provider", Manifest: "go.mod"},
			"example.com/app":      {Path: "example.com/app", Repository: "acme/app", Manifest: "go.mod"},
		},
		requirements: map[string][]goFleetRequirement{
			"example.com/provider": {{Dependency: "example.com/provider", Version: "v1.0.0", ConsumerModule: "example.com/app", Repository: "acme/app", Manifest: "go.mod"}},
		},
	}
	graph := graphFromGoFleet(discovered, nil, "main", nil)
	if len(graph.Order.Layers) != 2 {
		t.Fatalf("stored order = %+v", graph.Order)
	}
	if !reflect.DeepEqual(graph.Order.Layers[0].Repositories, []string{"acme/provider"}) {
		t.Fatalf("layer 0 = %+v", graph.Order.Layers[0])
	}
	markdown := graph.Markdown()
	for _, want := range []string{"## Release order", "| `00` | `acme/provider` | `1` |", "| `01` | `acme/app` | `1` |"} {
		if !strings.Contains(markdown, want) {
			t.Errorf("graph Markdown is missing %q:\n%s", want, markdown)
		}
	}
}

func TestGraphOrderMarkdownNamesCyclesAndSkipsEmptySelections(t *testing.T) {
	t.Parallel()
	if markdown := (GraphOrder{}).Markdown(); markdown != "" {
		t.Fatalf("empty order rendered %q", markdown)
	}
	order := GraphOrder{
		Layers: []GraphOrderLayer{{Index: 0, Repositories: []string{"acme/a", "acme/b"}}},
		Cycles: []GraphOrderCycle{{Layer: 0, Repositories: []string{"acme/a", "acme/b"}, Path: "acme/a -> acme/b -> acme/a"}},
	}
	markdown := order.Markdown()
	for _, want := range []string{"coordinated release", "- Layer `00`: `acme/a -> acme/b -> acme/a`"} {
		if !strings.Contains(markdown, want) {
			t.Errorf("order Markdown is missing %q:\n%s", want, markdown)
		}
	}
}

const fanOutRepositories = 16

func fanOutSlug(index int) string { return fmt.Sprintf("sneat-co/consumer-%02d", index) }

// internalRequirement builds one provider-to-consumer evidence row.
func internalRequirement(provider, consumer string) GraphRequirement {
	module := "example.com/" + strings.ReplaceAll(provider, "/", "-")
	return GraphRequirement{
		Dependency: module, Version: "v1.0.0", Manifest: "go.mod",
		ConsumerModule:     "example.com/" + strings.ReplaceAll(consumer, "/", "-"),
		ConsumerRepository: consumer, ProviderModule: module, ProviderRepository: provider,
	}
}

// nineLevelGraphFixture mirrors the shape of the DALgo consumer rollout.
func nineLevelGraphFixture() Graph {
	chain := []string{
		"strongo/strongoapp",
		"sneat-co/sneat-go-core",
		"sneat-co/sneat-auth-go",
		"sneat-co/sneat-spaceus",
		"sneat-co/sneat-core-modules",
	}
	graph := Graph{Repositories: []GraphRepository{{Slug: "acme/unrelated"}}}
	for _, slug := range chain {
		graph.Repositories = append(graph.Repositories, GraphRepository{Slug: slug})
	}
	for index := 1; index < len(chain); index++ {
		graph.Requirements = append(graph.Requirements, internalRequirement(chain[index-1], chain[index]))
	}
	for index := range fanOutRepositories {
		slug := fanOutSlug(index)
		graph.Repositories = append(graph.Repositories, GraphRepository{Slug: slug})
		graph.Requirements = append(graph.Requirements, internalRequirement("sneat-co/sneat-core-modules", slug))
	}
	top := []string{"sneat-co/sneat-go-backend", "sneat-co/sneat-bots", "sneat-co/sneat-go"}
	for _, slug := range top {
		graph.Repositories = append(graph.Repositories, GraphRepository{Slug: slug})
	}
	graph.Requirements = append(graph.Requirements,
		internalRequirement(fanOutSlug(0), top[0]),
		internalRequirement(fanOutSlug(fanOutRepositories-1), top[0]),
		internalRequirement(top[0], top[1]),
		internalRequirement(top[1], top[2]),
		// A skip-level requirement: the top repository also requires the root
		// module directly. Longest-path layering must keep it in layer 8.
		internalRequirement(chain[0], top[2]),
	)
	return graph
}
