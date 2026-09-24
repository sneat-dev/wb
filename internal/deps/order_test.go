package deps

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

func TestParseLayerSelectionAcceptsSingleClosedAndOpenRanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		spec     string
		rendered string
		inside   []int
		outside  []int
	}{
		{spec: "", rendered: "all", inside: []int{0, 3, 99}},
		{spec: "0", rendered: "0", inside: []int{0}, outside: []int{1, 2}},
		{spec: "2", rendered: "2", inside: []int{2}, outside: []int{0, 1, 3}},
		{spec: "1-3", rendered: "1-3", inside: []int{1, 2, 3}, outside: []int{0, 4}},
		{spec: "4-", rendered: "4-", inside: []int{4, 5, 40}, outside: []int{0, 3}},
	}
	for _, test := range tests {
		selection, err := ParseLayerSelection(test.spec)
		if err != nil {
			t.Fatalf("ParseLayerSelection(%q): %v", test.spec, err)
		}
		if selection.String() != test.rendered {
			t.Errorf("ParseLayerSelection(%q).String() = %q, want %q", test.spec, selection.String(), test.rendered)
		}
		for _, index := range test.inside {
			if !selection.Contains(index) {
				t.Errorf("selection %q excludes layer %d", test.spec, index)
			}
		}
		for _, index := range test.outside {
			if selection.Contains(index) {
				t.Errorf("selection %q includes layer %d", test.spec, index)
			}
		}
	}
	for _, invalid := range []string{"x", "-1", "1-0", "1-x", "1.5", "-", "1-2-3"} {
		if selection, err := ParseLayerSelection(invalid); err == nil {
			t.Errorf("ParseLayerSelection(%q) accepted %+v", invalid, selection)
		}
	}
}

func TestPlanOrderedLayersKeepsUnlayeredSelectionsAndAppliesTheRange(t *testing.T) {
	t.Parallel()
	order := GraphOrder{Layers: []GraphOrderLayer{
		{Index: 0, Repositories: []string{"acme/base"}},
		{Index: 1, Repositories: []string{"acme/middle"}},
		{Index: 2, Repositories: []string{"acme/top"}},
	}}
	repositories := []Repository{
		{Slug: "acme/top"}, {Slug: "acme/base"}, {Slug: "acme/middle"}, {Slug: "acme/archived", Archived: true},
	}
	selection, err := ParseLayerSelection("1-")
	if err != nil {
		t.Fatal(err)
	}
	layers := planOrderedLayers(order, repositories, selection)
	if len(layers) != 3 {
		t.Fatalf("layers = %+v", layers)
	}
	if got := layerSlugs(layers[0]); !reflect.DeepEqual(got, []string{"acme/archived", "acme/base"}) {
		t.Errorf("layer 0 = %v, want the archived repository folded into the first layer", got)
	}
	if layers[0].selected || !layers[1].selected || !layers[2].selected {
		t.Errorf("selection 1- chose %t, %t, %t", layers[0].selected, layers[1].selected, layers[2].selected)
	}
}

func TestPlanOrderedLayersIgnoresLayeredRepositoriesOutsideTheSelection(t *testing.T) {
	t.Parallel()
	order := GraphOrder{Layers: []GraphOrderLayer{
		{Index: 0, Repositories: []string{"acme/base", "acme/unselected"}},
		{Index: 1, Repositories: []string{"acme/top"}},
	}}
	layers := planOrderedLayers(order, []Repository{{Slug: "acme/base"}, {Slug: "acme/top"}}, LayerSelection{})
	if got := layerSlugs(layers[0]); !reflect.DeepEqual(got, []string{"acme/base"}) {
		t.Fatalf("layer 0 = %v", got)
	}
	if got := layerSlugs(layers[1]); !reflect.DeepEqual(got, []string{"acme/top"}) {
		t.Fatalf("layer 1 = %v", got)
	}
}

func TestRunOrderedLayersBlocksLaterLayersAfterAFailedLayer(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	base := newBumpRepository(t, root, githubDir, "base", "module example.com/base\n\ngo 1.24\n")
	middle := newBumpRepository(t, root, githubDir, "middle", "module example.com/middle\n\ngo 1.24\n\nrequire example.com/base v1.0.0\n")
	top := newBumpRepository(t, root, githubDir, "top", "module example.com/top\n\ngo 1.24\n\nrequire example.com/middle v1.0.0\n")
	order := GraphOrder{Layers: []GraphOrderLayer{
		{Index: 0, Repositories: []string{base.Slug}},
		{Index: 1, Repositories: []string{middle.Slug}},
		{Index: 2, Repositories: []string{top.Slug}},
	}}
	handler := &recordingOrderHandler{failing: map[string]bool{base.Slug: true}}
	lifecycle, err := orchestrate.Normalize(orchestrate.Options{
		GitHubDir: githubDir, Operation: "deps-set-test", Ref: "main", Parallel: 1, DryRun: true, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	results, report, runErr := runOrderedLayers(context.Background(), order, []Repository{top, middle, base}, handler, lifecycle, LayerSelection{})
	if runErr == nil {
		t.Fatal("a failed layer did not fail the run")
	}
	if !strings.Contains(runErr.Error(), "layer 00") {
		t.Errorf("run error does not name the failed layer: %v", runErr)
	}
	if got := handler.inspected(); !reflect.DeepEqual(got, []string{base.Slug}) {
		t.Fatalf("inspected repositories = %v, want only the failed layer", got)
	}
	wantStatuses := []string{"failed", "blocked", "blocked"}
	for index, layer := range report.Layers {
		if layer.Status != wantStatuses[index] {
			t.Errorf("layer %d status = %q, want %q", index, layer.Status, wantStatuses[index])
		}
	}
	statuses := map[string]string{}
	for _, result := range results {
		statuses[result.Repository] = result.Status
	}
	if statuses[middle.Slug] != "blocked" || statuses[top.Slug] != "blocked" {
		t.Fatalf("downstream results = %+v", statuses)
	}
	if !strings.Contains(report.Markdown(), "| `01` | `blocked` | `acme/middle` |") {
		t.Fatalf("order Markdown does not show the blocked layer:\n%s", report.Markdown())
	}
}

func TestRunOrderedLayersProcessesOneSelectedLayerInProviderFirstOrder(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	base := newBumpRepository(t, root, githubDir, "base", "module example.com/base\n\ngo 1.24\n")
	middle := newBumpRepository(t, root, githubDir, "middle", "module example.com/middle\n\ngo 1.24\n")
	top := newBumpRepository(t, root, githubDir, "top", "module example.com/top\n\ngo 1.24\n")
	order := GraphOrder{
		Layers: []GraphOrderLayer{
			{Index: 0, Repositories: []string{base.Slug}},
			{Index: 1, Repositories: []string{middle.Slug}},
			{Index: 2, Repositories: []string{top.Slug}},
		},
		Cycles: []GraphOrderCycle{{Layer: 1, Repositories: []string{middle.Slug}, Path: "acme/middle -> acme/middle"}},
	}
	selection, err := ParseLayerSelection("1")
	if err != nil {
		t.Fatal(err)
	}
	handler := &recordingOrderHandler{}
	lifecycle, err := orchestrate.Normalize(orchestrate.Options{
		GitHubDir: githubDir, Operation: "deps-set-test", Ref: "main", Parallel: 1, DryRun: true, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	results, report, runErr := runOrderedLayers(context.Background(), order, []Repository{top, middle, base}, handler, lifecycle, selection)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if got := handler.inspected(); !reflect.DeepEqual(got, []string{middle.Slug}) {
		t.Fatalf("inspected repositories = %v, want only the selected layer", got)
	}
	if len(results) != 1 || results[0].Status != "planned" {
		t.Fatalf("results = %+v", results)
	}
	wantStatuses := []string{"not_selected", "completed", "not_selected"}
	for index, layer := range report.Layers {
		if layer.Status != wantStatuses[index] {
			t.Errorf("layer %d status = %q, want %q", index, layer.Status, wantStatuses[index])
		}
	}
	markdown := report.Markdown()
	for _, want := range []string{"selected layers: `1`", "coordinated release", "acme/middle -> acme/middle"} {
		if !strings.Contains(markdown, want) {
			t.Errorf("order Markdown is missing %q:\n%s", want, markdown)
		}
	}
}

// TestRunDerivesOrderFromDeclaredModulesNotNames proves the end-to-end path:
// Run builds the layering from what the checkouts declare, not from repository
// names or directory layout. The alphabetically first repository declares the
// module the other two require, so name order and dependency order disagree.
func TestRunDerivesOrderFromDeclaredModulesNotNames(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	zebra := newBumpRepository(t, root, githubDir, "zebra", "module example.com/zebra\n\ngo 1.24\n")
	alpha := newBumpRepository(t, root, githubDir, "alpha", "module example.com/alpha\n\ngo 1.24\n\nrequire example.com/zebra v1.0.0\n")
	middle := newBumpRepository(t, root, githubDir, "middle", "module example.com/middle\n\ngo 1.24\n\nrequire example.com/alpha v1.0.0\n")
	target := Target{Ecosystem: EcosystemGo, Dependency: "example.com/absent", Version: "v1.0.0"}
	report, err := Run(context.Background(), target, []Repository{alpha, middle, zebra}, Options{
		GitHubDir: githubDir, Ref: "main", Parallel: 1, DryRun: true, Order: true, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Order == nil {
		t.Fatal("an ordered run produced no order report")
	}
	want := [][]string{{"acme/zebra"}, {"acme/alpha"}, {"acme/middle"}}
	if len(report.Order.Layers) != len(want) {
		t.Fatalf("layers = %+v", report.Order.Layers)
	}
	for index, layer := range report.Order.Layers {
		if !reflect.DeepEqual(layer.Repositories, want[index]) {
			t.Errorf("layer %d = %v, want %v", index, layer.Repositories, want[index])
		}
		if layer.Status != "completed" {
			t.Errorf("layer %d status = %q", index, layer.Status)
		}
	}
	if !strings.Contains(report.Markdown(), "## Dependency order") {
		t.Fatalf("deps-set Markdown has no dependency order section:\n%s", report.Markdown())
	}
}

func TestRunWithoutOrderReportsNoLayerPlan(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	zebra := newBumpRepository(t, root, githubDir, "zebra", "module example.com/zebra\n\ngo 1.24\n")
	target := Target{Ecosystem: EcosystemGo, Dependency: "example.com/absent", Version: "v1.0.0"}
	report, err := Run(context.Background(), target, []Repository{zebra}, Options{
		GitHubDir: githubDir, Ref: "main", Parallel: 1, DryRun: true, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Order != nil {
		t.Fatalf("an unordered run reported a layer plan: %+v", report.Order)
	}
	if strings.Contains(report.Markdown(), "## Dependency order") {
		t.Fatalf("an unordered run rendered a dependency order section:\n%s", report.Markdown())
	}
}

func TestRunRejectsDependencyOrderForEcosystemsWithoutAModuleGraph(t *testing.T) {
	t.Parallel()
	target := Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0"}
	_, err := Run(context.Background(), target, []Repository{{Slug: "acme/app"}}, Options{
		GitHubDir: t.TempDir(), DryRun: true, Order: true,
		ResolveGitHubRef: func(context.Context, string, string) (string, error) { return strings.Repeat("6", 40), nil },
	})
	if err == nil || !strings.Contains(err.Error(), "only for the go ecosystem") {
		t.Fatalf("error = %v", err)
	}
}

func layerSlugs(layer orderedLayer) []string {
	slugs := make([]string, 0, len(layer.repositories))
	for _, repository := range layer.repositories {
		slugs = append(slugs, repository.Slug)
	}
	return slugs
}

// recordingOrderHandler is a hermetic lifecycle handler: it records the order
// in which repositories were inspected and can fail a chosen repository.
type recordingOrderHandler struct {
	failing map[string]bool
	mutex   sync.Mutex
	visited []string
}

func (handler *recordingOrderHandler) Inspect(_ context.Context, _, _ string, repository orchestrate.Repository) (orchestrate.Assessment[string], error) {
	handler.mutex.Lock()
	handler.visited = append(handler.visited, repository.Slug)
	handler.mutex.Unlock()
	if handler.failing[repository.Slug] {
		return orchestrate.Assessment[string]{}, fmt.Errorf("synthetic inspection failure")
	}
	return orchestrate.Assessment[string]{Applicable: true, NeedsChange: true, Reason: "test", Metadata: "inspected"}, nil
}

func (handler *recordingOrderHandler) Apply(context.Context, string, orchestrate.Repository) (string, error) {
	return "applied", nil
}

func (handler *recordingOrderHandler) ValidatePublishable(context.Context, string, orchestrate.Repository) error {
	return nil
}

func (handler *recordingOrderHandler) CommitMessage(orchestrate.Repository) string { return "test" }

func (handler *recordingOrderHandler) PullRequest(orchestrate.Repository) (string, string) {
	return "test", "test"
}

func (handler *recordingOrderHandler) inspected() []string {
	handler.mutex.Lock()
	defer handler.mutex.Unlock()
	return append([]string(nil), handler.visited...)
}
