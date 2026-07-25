package deps

import (
	"fmt"
	"sort"
	"strings"
)

// layeredComponent is one strongly connected component and the longest
// provider distance between it and a component that has no provider.
type layeredComponent struct {
	nodes []string
	level int
}

// RepositoryOrder derives the provider-first layering of the selected
// repositories from canonical requirement evidence alone. Layer 0 requires no
// other selected repository; every later layer requires only earlier layers,
// so one layer can be released before the next one starts.
//
// Repositories that require each other cannot be separated by any order. They
// share one strongly connected component, are placed in one layer, and are
// reported as a cycle instead of being dropped or making layering fail.
func (graph Graph) RepositoryOrder() GraphOrder {
	adjacency := map[string][]string{}
	for _, repository := range graph.Repositories {
		if _, exists := adjacency[repository.Slug]; !exists {
			adjacency[repository.Slug] = nil
		}
	}
	edges := map[string]bool{}
	for _, requirement := range graph.Requirements {
		provider, consumer := requirement.ProviderRepository, requirement.ConsumerRepository
		if provider == "" || consumer == "" || provider == consumer {
			continue
		}
		if _, exists := adjacency[provider]; !exists {
			adjacency[provider] = nil
		}
		if _, exists := adjacency[consumer]; !exists {
			adjacency[consumer] = nil
		}
		key := provider + "\x00" + consumer
		if edges[key] {
			continue
		}
		edges[key] = true
		adjacency[provider] = append(adjacency[provider], consumer)
	}
	for repository := range adjacency {
		sort.Strings(adjacency[repository])
	}
	order := GraphOrder{}
	repositoriesByLevel := map[int][]string{}
	highest := -1
	for _, component := range topologicalLayers(adjacency) {
		repositoriesByLevel[component.level] = append(repositoriesByLevel[component.level], component.nodes...)
		if component.level > highest {
			highest = component.level
		}
		if len(component.nodes) > 1 {
			order.Cycles = append(order.Cycles, GraphOrderCycle{
				Layer:        component.level,
				Repositories: append([]string(nil), component.nodes...),
				Path:         repositoryCyclePath(component.nodes, adjacency),
			})
		}
	}
	for level := 0; level <= highest; level++ {
		repositories := repositoriesByLevel[level]
		sort.Strings(repositories)
		order.Layers = append(order.Layers, GraphOrderLayer{Index: level, Repositories: repositories})
	}
	return order
}

// Markdown renders the provider-first layering, or nothing when the selection
// contains no repository. Cycles are named explicitly so an operator never has
// to infer why two repositories share a layer.
func (order GraphOrder) Markdown() string {
	if len(order.Layers) == 0 {
		return ""
	}
	var output strings.Builder
	output.WriteString("\n## Release order\n\n")
	output.WriteString("Provider-first layers derived from internal provider to consumer edges.\n")
	output.WriteString("Every layer may be processed as one batch once all earlier layers have landed.\n\n")
	output.WriteString("| Layer | Repositories | Count |\n")
	output.WriteString("|---:|---|---:|\n")
	for _, layer := range order.Layers {
		fmt.Fprintf(&output, "| `%02d` | `%s` | `%d` |\n",
			layer.Index, strings.Join(layer.Repositories, "`, `"), len(layer.Repositories))
	}
	if len(order.Cycles) > 0 {
		output.WriteString("\nMutually dependent repositories share one layer and need a coordinated release:\n\n")
		for _, cycle := range order.Cycles {
			fmt.Fprintf(&output, "- Layer `%02d`: `%s`\n", cycle.Layer, cycle.Path)
		}
	}
	return output.String()
}

// Repositories lists every repository of the layering in provider-first order.
func (order GraphOrder) Repositories() []string {
	var repositories []string
	for _, layer := range order.Layers {
		repositories = append(repositories, layer.Repositories...)
	}
	return repositories
}

// topologicalLayers condenses a directed graph into strongly connected
// components and gives every component the longest provider distance from a
// component without providers. Mutually dependent nodes share one component,
// so a cycle is reported by the caller rather than hanging or losing a node.
// Components are returned in deterministic layer-then-name order.
func topologicalLayers(adjacency map[string][]string) []layeredComponent {
	successors := make(map[string][]string, len(adjacency))
	for node, targets := range adjacency {
		sorted := append([]string(nil), targets...)
		sort.Strings(sorted)
		successors[node] = sorted
	}
	index := 0
	indexes := map[string]int{}
	lowLink := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var components [][]string
	var connect func(string)
	connect = func(node string) {
		indexes[node] = index
		lowLink[node] = index
		index++
		stack = append(stack, node)
		onStack[node] = true
		for _, next := range successors[node] {
			if _, visited := indexes[next]; !visited {
				connect(next)
				if lowLink[next] < lowLink[node] {
					lowLink[node] = lowLink[next]
				}
			} else if onStack[next] && indexes[next] < lowLink[node] {
				lowLink[node] = indexes[next]
			}
		}
		if lowLink[node] != indexes[node] {
			return
		}
		var component []string
		for {
			last := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[last] = false
			component = append(component, last)
			if last == node {
				break
			}
		}
		sort.Strings(component)
		components = append(components, component)
	}
	nodes := make([]string, 0, len(successors))
	for node := range successors {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	for _, node := range nodes {
		if _, visited := indexes[node]; !visited {
			connect(node)
		}
	}
	componentByNode := map[string]int{}
	for component, members := range components {
		for _, node := range members {
			componentByNode[node] = component
		}
	}
	componentEdges := map[int]map[int]bool{}
	indegree := make([]int, len(components))
	for from, nextNodes := range successors {
		fromComponent := componentByNode[from]
		for _, to := range nextNodes {
			toComponent := componentByNode[to]
			if fromComponent == toComponent {
				continue
			}
			if componentEdges[fromComponent] == nil {
				componentEdges[fromComponent] = map[int]bool{}
			}
			if !componentEdges[fromComponent][toComponent] {
				componentEdges[fromComponent][toComponent] = true
				indegree[toComponent]++
			}
		}
	}
	levels := make([]int, len(components))
	var ready []int
	for component := range components {
		if indegree[component] == 0 {
			ready = append(ready, component)
		}
	}
	sort.Ints(ready)
	for len(ready) > 0 {
		component := ready[0]
		ready = ready[1:]
		var targets []int
		for target := range componentEdges[component] {
			targets = append(targets, target)
		}
		sort.Ints(targets)
		for _, target := range targets {
			if levels[target] < levels[component]+1 {
				levels[target] = levels[component] + 1
			}
			indegree[target]--
			if indegree[target] == 0 {
				ready = append(ready, target)
				sort.Ints(ready)
			}
		}
	}
	layered := make([]layeredComponent, len(components))
	for component, members := range components {
		layered[component] = layeredComponent{nodes: members, level: levels[component]}
	}
	sort.Slice(layered, func(i, j int) bool {
		if layered[i].level != layered[j].level {
			return layered[i].level < layered[j].level
		}
		return layered[i].nodes[0] < layered[j].nodes[0]
	})
	return layered
}

// repositoryCyclePath renders one deterministic shortest cycle through a
// strongly connected component, starting and ending at its lowest-sorted
// member so an operator can see exactly which requirements are mutual.
func repositoryCyclePath(component []string, adjacency map[string][]string) string {
	members := map[string]bool{}
	for _, node := range component {
		members[node] = true
	}
	start := component[0]
	parent := map[string]string{}
	visited := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, next := range adjacency[node] {
			if !members[next] {
				continue
			}
			if next == start {
				return strings.Join(append(cycleBack(parent, node, start), start), " -> ")
			}
			if visited[next] {
				continue
			}
			visited[next] = true
			parent[next] = node
			queue = append(queue, next)
		}
	}
	return strings.Join(component, " -> ")
}

// cycleBack rebuilds the provider-first path from start to node.
func cycleBack(parent map[string]string, node, start string) []string {
	var reversed []string
	for at := node; at != start; at = parent[at] {
		reversed = append(reversed, at)
	}
	path := []string{start}
	for index := len(reversed) - 1; index >= 0; index-- {
		path = append(path, reversed[index])
	}
	return path
}
