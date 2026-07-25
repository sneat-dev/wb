package deps

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

// LayerSelection restricts an ordered run to one layer or a contiguous range
// of layers. The zero value selects every layer.
type LayerSelection struct {
	spec string
	from int
	to   int
}

// ParseLayerSelection accepts an empty value for every layer, `N` for one
// layer, `N-M` for a closed range, and `N-` for every layer from N onwards.
func ParseLayerSelection(value string) (LayerSelection, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return LayerSelection{}, nil
	}
	invalid := fmt.Errorf("invalid layer selection %q (want N, N-M, or N-)", value)
	first, second, ranged := strings.Cut(value, "-")
	from, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil || from < 0 {
		return LayerSelection{}, invalid
	}
	selection := LayerSelection{spec: value, from: from, to: from}
	if !ranged {
		return selection, nil
	}
	if strings.TrimSpace(second) == "" {
		selection.to = -1
		return selection, nil
	}
	to, err := strconv.Atoi(strings.TrimSpace(second))
	if err != nil || to < from {
		return LayerSelection{}, invalid
	}
	selection.to = to
	return selection, nil
}

// Contains reports whether one layer index belongs to the selection.
func (selection LayerSelection) Contains(index int) bool {
	if selection.spec == "" {
		return true
	}
	if index < selection.from {
		return false
	}
	return selection.to < 0 || index <= selection.to
}

// String renders the requested selection for reports and reasons.
func (selection LayerSelection) String() string {
	if selection.spec == "" {
		return "all"
	}
	return selection.spec
}

// Markdown renders the layer plan and each layer's outcome, or nothing when
// the run was not ordered. It is the per-layer boundary an operator reads to
// decide whether the next layer may start.
func (report *OrderReport) Markdown() string {
	if report == nil || len(report.Layers) == 0 {
		return ""
	}
	var output strings.Builder
	output.WriteString("## Dependency order\n\n")
	fmt.Fprintf(&output, "Provider-first layers; selected layers: `%s`.\n", report.Selection)
	output.WriteString("A later layer is never started after an earlier layer failed.\n\n")
	output.WriteString("| Layer | Status | Repositories |\n")
	output.WriteString("|---:|---|---|\n")
	for _, layer := range report.Layers {
		repositories := "—"
		if len(layer.Repositories) > 0 {
			repositories = "`" + strings.Join(layer.Repositories, "`, `") + "`"
		}
		fmt.Fprintf(&output, "| `%02d` | `%s` | %s |\n", layer.Index, layer.Status, repositories)
	}
	if len(report.Cycles) > 0 {
		output.WriteString("\nMutually dependent repositories share one layer and need a coordinated release:\n\n")
		for _, cycle := range report.Cycles {
			fmt.Fprintf(&output, "- Layer `%02d`: `%s`\n", cycle.Layer, cycle.Path)
		}
	}
	output.WriteByte('\n')
	return output.String()
}

// orderForRepositories scans the selected repositories once and derives their
// provider-first layering. Ordering is evidence-based, so it needs an
// ecosystem whose manifests declare module identities; `github-actions`
// references have no module graph and are rejected before any repository work.
func orderForRepositories(ctx context.Context, repositories []Repository, target Target, lifecycle orchestrate.Options) (GraphOrder, error) {
	if target.Ecosystem != EcosystemGo {
		return GraphOrder{}, fmt.Errorf("dependency ordering is supported only for the go ecosystem, not %q", target.Ecosystem)
	}
	graph, err := BuildGraph(ctx, repositories, GraphOptions{
		Ecosystem: EcosystemGo, GitHubDir: lifecycle.GitHubDir, Ref: lifecycle.Ref,
		Parallel: lifecycle.Parallel, Timeout: lifecycle.Timeout, Retry: lifecycle.Retry,
	})
	if err != nil {
		return GraphOrder{}, err
	}
	return graph.Order, nil
}

// orderedLayer is one planned layer restricted to the selected repositories.
type orderedLayer struct {
	index        int
	repositories []Repository
	selected     bool
}

// planOrderedLayers intersects the canonical layering with the repositories
// the caller selected and with the requested layer range. Selected
// repositories that the layering does not mention — archived clones, or
// repositories whose manifests declare nothing the fleet provides — join layer
// 0, so an ordered run never silently drops a repository an ordinary run would
// have reported.
func planOrderedLayers(order GraphOrder, repositories []Repository, selection LayerSelection) []orderedLayer {
	bySlug := make(map[string]Repository, len(repositories))
	for _, repository := range repositories {
		bySlug[repository.Slug] = repository
	}
	placed := map[string]bool{}
	layers := make([]orderedLayer, 0, len(order.Layers))
	for _, layer := range order.Layers {
		planned := orderedLayer{index: layer.Index, selected: selection.Contains(layer.Index)}
		for _, slug := range layer.Repositories {
			repository, exists := bySlug[slug]
			if !exists || placed[slug] {
				continue
			}
			placed[slug] = true
			planned.repositories = append(planned.repositories, repository)
		}
		layers = append(layers, planned)
	}
	var unplaced []Repository
	for _, repository := range repositories {
		if !placed[repository.Slug] {
			unplaced = append(unplaced, repository)
		}
	}
	if len(unplaced) > 0 {
		if len(layers) == 0 {
			layers = append(layers, orderedLayer{index: 0, selected: selection.Contains(0)})
		}
		layers[0].repositories = append(layers[0].repositories, unplaced...)
	}
	for index := range layers {
		sort.Slice(layers[index].repositories, func(left, right int) bool {
			return layers[index].repositories[left].Slug < layers[index].repositories[right].Slug
		})
	}
	return layers
}

// runOrderedLayers processes provider-first layers one after another and never
// starts a later layer once an earlier one failed. Repositories inside a layer
// are independent of each other, so they keep the shared engine's `--parallel`
// concurrency and its complete-independent-results behaviour. Repositories in
// a later layer are not independent of a failed one, so they are reported as
// blocked instead of receiving a pull request nothing can turn green.
func runOrderedLayers[T any](ctx context.Context, order GraphOrder, repositories []Repository, handler orchestrate.Handler[T], lifecycle orchestrate.Options, selection LayerSelection) ([]orchestrate.Result[T], *OrderReport, error) {
	layers := planOrderedLayers(order, repositories, selection)
	report := &OrderReport{Selection: selection.String(), Cycles: order.Cycles}
	var results []orchestrate.Result[T]
	var runErrors []error
	failed := -1
	for _, layer := range layers {
		entry := OrderLayerReport{Index: layer.index}
		for _, repository := range layer.repositories {
			entry.Repositories = append(entry.Repositories, repository.Slug)
		}
		switch {
		case !layer.selected:
			entry.Status = "not_selected"
		case failed >= 0:
			entry.Status = "blocked"
			reason := fmt.Sprintf("layer %02d was not started because layer %02d failed", layer.index, failed)
			for _, repository := range layer.repositories {
				results = append(results, orchestrate.Result[T]{
					Repository: repository.Slug, Ref: lifecycle.Ref, Status: "blocked", Reason: reason,
				})
			}
		default:
			layerResults, err := orchestrate.Run(ctx, layer.repositories, handler, lifecycle)
			results = append(results, layerResults...)
			entry.Status = "completed"
			if err != nil {
				entry.Status = "failed"
				failed = layer.index
				runErrors = append(runErrors, fmt.Errorf("layer %02d: %w", layer.index, err))
			}
		}
		report.Layers = append(report.Layers, entry)
	}
	return results, report, errors.Join(runErrors...)
}
