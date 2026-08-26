package deps

import (
	"time"

	"github.com/sneat-dev/wb/internal/progress"
)

// GraphView selects one visual projection of canonical dependency evidence.
type GraphView string

const (
	GraphViewRepositories GraphView = "repos"
	GraphViewDependencies GraphView = "dependencies"
	GraphViewSelections   GraphView = "selections"
)

// GraphOptions controls read-only dependency discovery and evidence filtering.
type GraphOptions struct {
	Ecosystem    Ecosystem
	GitHubDir    string
	Ref          string
	Parallel     int
	Timeout      time.Duration
	Retry        int
	Dependencies []string
	Progress     progress.Reporter
}

// Graph is the canonical, deterministic evidence model shared by every view.
type Graph struct {
	SchemaVersion int                `json:"schema_version" yaml:"schema_version"`
	Ecosystem     Ecosystem          `json:"ecosystem" yaml:"ecosystem"`
	BaseRef       string             `json:"base_ref" yaml:"base_ref"`
	Filters       GraphFilters       `json:"filters" yaml:"filters"`
	Summary       GraphSummary       `json:"summary" yaml:"summary"`
	Order         GraphOrder         `json:"order" yaml:"order"`
	Repositories  []GraphRepository  `json:"repositories" yaml:"repositories"`
	Modules       []GraphModule      `json:"modules" yaml:"modules"`
	Requirements  []GraphRequirement `json:"requirements" yaml:"requirements"`
	// DiscoverySkips lists repositories excluded from the walk rather than
	// inspected. It belongs to the graph rather than to a log line so that no
	// output format can present a partial fleet as a complete one.
	DiscoverySkips []GraphDiscoverySkip `json:"discovery_skips,omitempty" yaml:"discovery_skips,omitempty"`
}

// GraphFilters records evidence filters applied after repository discovery.
type GraphFilters struct {
	Dependencies []string `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
}

// GraphSummary provides view-independent canonical counts.
type GraphSummary struct {
	Repositories         int `json:"repositories" yaml:"repositories"`
	Modules              int `json:"modules" yaml:"modules"`
	Requirements         int `json:"requirements" yaml:"requirements"`
	InternalRequirements int `json:"internal_requirements" yaml:"internal_requirements"`
	ExternalDependencies int `json:"external_dependencies" yaml:"external_dependencies"`
	Selections           int `json:"selections" yaml:"selections"`
	AmbiguousProviders   int `json:"ambiguous_providers" yaml:"ambiguous_providers"`
}

// GraphOrder is the provider-first layering of the selected repositories. It
// answers "which repositories must be released first" from the same canonical
// evidence as every projection, without a second manifest scan.
type GraphOrder struct {
	Layers []GraphOrderLayer `json:"layers,omitempty" yaml:"layers,omitempty"`
	Cycles []GraphOrderCycle `json:"cycles,omitempty" yaml:"cycles,omitempty"`
}

// GraphOrderLayer lists repositories whose selected internal providers all sit
// in earlier layers, so the whole layer may be processed as one batch.
type GraphOrderLayer struct {
	Index        int      `json:"index" yaml:"index"`
	Repositories []string `json:"repositories" yaml:"repositories"`
}

// GraphOrderCycle records repositories that require each other and therefore
// share one layer, because no release order can separate them.
type GraphOrderCycle struct {
	Layer        int      `json:"layer" yaml:"layer"`
	Repositories []string `json:"repositories" yaml:"repositories"`
	Path         string   `json:"path" yaml:"path"`
}

// GraphRepository is a selected repository retained by the filtered graph.
type GraphRepository struct {
	Slug         string   `json:"slug" yaml:"slug"`
	Organization string   `json:"organization" yaml:"organization"`
	Modules      []string `json:"modules,omitempty" yaml:"modules,omitempty"`
}

// GraphModule is a module declaration and its source manifest.
type GraphModule struct {
	Path       string `json:"path" yaml:"path"`
	Repository string `json:"repository" yaml:"repository"`
	Manifest   string `json:"manifest" yaml:"manifest"`
}

// GraphRequirement is one manifest-owned dependency selection.
type GraphRequirement struct {
	Dependency         string   `json:"dependency" yaml:"dependency"`
	Version            string   `json:"version" yaml:"version"`
	ConsumerModule     string   `json:"consumer_module" yaml:"consumer_module"`
	ConsumerRepository string   `json:"consumer_repository" yaml:"consumer_repository"`
	Manifest           string   `json:"manifest" yaml:"manifest"`
	Indirect           bool     `json:"indirect,omitempty" yaml:"indirect,omitempty"`
	ProviderModule     string   `json:"provider_module,omitempty" yaml:"provider_module,omitempty"`
	ProviderRepository string   `json:"provider_repository,omitempty" yaml:"provider_repository,omitempty"`
	ProviderCandidates []string `json:"provider_candidates,omitempty" yaml:"provider_candidates,omitempty"`
}

// GraphProjection is a deterministic view derived only from Graph.
type GraphProjection struct {
	View  GraphView
	Nodes []GraphProjectionNode
	Edges []GraphProjectionEdge
}

// GraphProjectionNode is a visual entity with stable identity.
type GraphProjectionNode struct {
	ID             string
	Kind           string
	Label          string
	Subtitle       string
	Status         string
	Repository     string
	Dependency     string
	Version        string
	GitHubURL      string
	CodeGrapherURL string
	Organization   string
}

// GraphProjectionEdge aggregates canonical evidence for a visual relation.
type GraphProjectionEdge struct {
	ID            string
	From          string
	To            string
	DirectCount   int
	IndirectCount int
	Status        string
	Evidence      []string
}

// GraphReportPaths identifies every artifact written for one graph scan.
type GraphReportPaths struct {
	Markdown string
	YAML     string
	JSON     string
	SVG      string
	HTML     string
}
