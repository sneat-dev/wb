package deps

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// ParseGraphView validates a CLI or report projection name.
func ParseGraphView(value string) (GraphView, error) {
	view := GraphView(strings.TrimSpace(value))
	switch view {
	case GraphViewRepositories, GraphViewDependencies, GraphViewSelections:
		return view, nil
	default:
		return "", fmt.Errorf("unknown dependency graph view %q (want repos, dependencies, or selections)", value)
	}
}

// Project derives one visual view without rescanning canonical evidence.
func (graph Graph) Project(view GraphView) (GraphProjection, error) {
	if _, err := ParseGraphView(string(view)); err != nil {
		return GraphProjection{}, err
	}
	highest := graphHighestVersions(graph.Requirements)
	projection := GraphProjection{View: view}
	nodes := map[string]GraphProjectionNode{}
	edges := map[string]GraphProjectionEdge{}
	addRepository := func(repository string) string {
		id := graphNodeID("repository", repository)
		if _, exists := nodes[id]; exists {
			return id
		}
		moduleCount := 0
		for _, candidate := range graph.Repositories {
			if candidate.Slug == repository {
				moduleCount = len(candidate.Modules)
				break
			}
		}
		subtitle := "repository"
		if moduleCount == 1 {
			subtitle = "1 module"
		} else if moduleCount > 1 {
			subtitle = fmt.Sprintf("%d modules", moduleCount)
		}
		nodes[id] = GraphProjectionNode{ID: id, Kind: "repository", Label: repository, Subtitle: subtitle, Status: "normal", Repository: repository}
		return id
	}
	if view == GraphViewRepositories {
		for _, repository := range graph.Repositories {
			addRepository(repository.Slug)
		}
	}
	versionsByDependency := map[string]map[string]bool{}
	for _, requirement := range graph.Requirements {
		if versionsByDependency[requirement.Dependency] == nil {
			versionsByDependency[requirement.Dependency] = map[string]bool{}
		}
		versionsByDependency[requirement.Dependency][requirement.Version] = true
		status := graphRequirementStatus(requirement, highest)
		var from, to string
		switch view {
		case GraphViewRepositories:
			if requirement.ProviderRepository == "" || requirement.ProviderRepository == requirement.ConsumerRepository {
				continue
			}
			from = addRepository(requirement.ProviderRepository)
			to = addRepository(requirement.ConsumerRepository)
		case GraphViewDependencies:
			from = graphNodeID("dependency", requirement.Dependency)
			if _, exists := nodes[from]; !exists {
				subtitle := "external dependency"
				if requirement.ProviderRepository != "" && len(requirement.ProviderCandidates) > 1 {
					subtitle = fmt.Sprintf("provided by %s · %d declarations", requirement.ProviderRepository, len(requirement.ProviderCandidates))
				} else if requirement.ProviderRepository != "" {
					subtitle = "provided by " + requirement.ProviderRepository
				} else if len(requirement.ProviderCandidates) > 0 {
					subtitle = fmt.Sprintf("ambiguous provider · %d declarations", len(requirement.ProviderCandidates))
				}
				nodes[from] = GraphProjectionNode{ID: from, Kind: "dependency", Label: requirement.Dependency, Subtitle: subtitle, Status: "normal", Dependency: requirement.Dependency}
			}
			to = addRepository(requirement.ConsumerRepository)
		case GraphViewSelections:
			selection := requirement.Dependency + "@" + requirement.Version
			from = graphNodeID("selection", selection)
			if _, exists := nodes[from]; !exists {
				subtitle := "observed selection"
				switch status {
				case "fleet-highest":
					subtitle = "fleet-highest observed version"
				case "behind":
					subtitle = "behind fleet-highest observed version"
				}
				nodes[from] = GraphProjectionNode{ID: from, Kind: "selection", Label: selection, Subtitle: subtitle, Status: status, Dependency: requirement.Dependency, Version: requirement.Version}
			}
			to = addRepository(requirement.ConsumerRepository)
		}
		key := from + "\x00" + to
		edge, exists := edges[key]
		if !exists {
			edge = GraphProjectionEdge{ID: graphEdgeID(from, to), From: from, To: to, Status: "normal"}
		}
		if requirement.Indirect {
			edge.IndirectCount++
		} else {
			edge.DirectCount++
		}
		if status == "behind" {
			edge.Status = "behind"
			consumer := nodes[to]
			consumer.Status = "behind"
			nodes[to] = consumer
		}
		directness := "direct"
		if requirement.Indirect {
			directness = "indirect"
		}
		edge.Evidence = append(edge.Evidence, fmt.Sprintf("%s:%s — %s requires %s@%s (%s)",
			requirement.ConsumerRepository, requirement.Manifest, requirement.ConsumerModule,
			requirement.Dependency, requirement.Version, directness))
		edges[key] = edge
	}
	for id, node := range nodes {
		if node.Kind == "dependency" {
			count := len(versionsByDependency[node.Dependency])
			if count == 1 {
				node.Subtitle += " · 1 observed version"
			} else if count > 1 {
				node.Subtitle += fmt.Sprintf(" · %d observed versions", count)
			}
			nodes[id] = node
		}
	}
	for _, node := range nodes {
		projection.Nodes = append(projection.Nodes, node)
	}
	for _, edge := range edges {
		sort.Strings(edge.Evidence)
		projection.Edges = append(projection.Edges, edge)
	}
	sort.Slice(projection.Nodes, func(i, j int) bool {
		if projection.Nodes[i].Kind != projection.Nodes[j].Kind {
			return projection.Nodes[i].Kind < projection.Nodes[j].Kind
		}
		if projection.Nodes[i].Label != projection.Nodes[j].Label {
			return projection.Nodes[i].Label < projection.Nodes[j].Label
		}
		return projection.Nodes[i].ID < projection.Nodes[j].ID
	})
	sort.Slice(projection.Edges, func(i, j int) bool {
		if projection.Edges[i].From != projection.Edges[j].From {
			return projection.Edges[i].From < projection.Edges[j].From
		}
		return projection.Edges[i].To < projection.Edges[j].To
	})
	return projection, nil
}

func graphHighestVersions(requirements []GraphRequirement) map[string]string {
	highest := map[string]string{}
	for _, requirement := range requirements {
		if !semver.IsValid(requirement.Version) {
			continue
		}
		if current := highest[requirement.Dependency]; current == "" || semver.Compare(requirement.Version, current) > 0 {
			highest[requirement.Dependency] = requirement.Version
		}
	}
	return highest
}

func graphRequirementStatus(requirement GraphRequirement, highest map[string]string) string {
	if !semver.IsValid(requirement.Version) || highest[requirement.Dependency] == "" {
		return "selected"
	}
	if semver.Compare(requirement.Version, highest[requirement.Dependency]) < 0 {
		return "behind"
	}
	return "fleet-highest"
}

func graphNodeID(kind, identity string) string {
	digest := sha256.Sum256([]byte(kind + "\x00" + identity))
	return "node-" + hex.EncodeToString(digest[:8])
}

func graphEdgeID(from, to string) string {
	digest := sha256.Sum256([]byte(from + "\x00" + to))
	return "edge-" + hex.EncodeToString(digest[:8])
}
