package deps

import (
	"time"

	"github.com/sneat-dev/wb/internal/progress"
)

// DriftClassification is the fleet-level state for one canonical dependency path.
type DriftClassification string

const (
	DriftConverged      DriftClassification = "converged"
	DriftDivergent      DriftClassification = "divergent"
	DriftReplaced       DriftClassification = "replaced"
	DriftMajorPathSplit DriftClassification = "major_path_split"
	DriftUnavailable    DriftClassification = "unavailable"
	DriftError          DriftClassification = "error"
)

// DriftOptions controls read-only drift analysis.
type DriftOptions struct {
	GitHubDir    string
	Ref          string
	Parallel     int
	Timeout      time.Duration
	Retry        int
	GoPrivate    []string
	Dependencies []string
	Online       bool
	FailOnDrift  bool
	Now          func() time.Time
	Progress     progress.Reporter
}

// DriftReport is the deterministic convergence index for one repository or a fleet.
type DriftReport struct {
	SchemaVersion  int                  `json:"schema_version" yaml:"schema_version"`
	Ecosystem      Ecosystem            `json:"ecosystem" yaml:"ecosystem"`
	Mode           string               `json:"mode" yaml:"mode"`
	BaseRef        string               `json:"base_ref" yaml:"base_ref"`
	ObservedAt     time.Time            `json:"observed_at" yaml:"observed_at"`
	Summary        DriftSummary         `json:"summary" yaml:"summary"`
	Groups         []DriftVersionGroup  `json:"groups" yaml:"groups"`
	Repositories   []DriftRepository    `json:"repositories" yaml:"repositories"`
	DiscoverySkips []GraphDiscoverySkip `json:"discovery_skips,omitempty" yaml:"discovery_skips,omitempty"`
}

// DriftSummary counts fleet-level classifications.
type DriftSummary struct {
	Repositories int `json:"repositories" yaml:"repositories"`
	Dependencies int `json:"dependencies" yaml:"dependencies"`
	Converged    int `json:"converged" yaml:"converged"`
	Divergent    int `json:"divergent" yaml:"divergent"`
	Replaced     int `json:"replaced" yaml:"replaced"`
	MajorSplit   int `json:"major_path_split" yaml:"major_path_split"`
	Unavailable  int `json:"unavailable" yaml:"unavailable"`
	Error        int `json:"error" yaml:"error"`
}

// DriftVersionGroup aggregates one canonical dependency across repositories.
type DriftVersionGroup struct {
	Dependency     string              `json:"dependency" yaml:"dependency"`
	Family         string              `json:"family,omitempty" yaml:"family,omitempty"`
	Classification DriftClassification `json:"classification" yaml:"classification"`
	Versions       []DriftVersionUse   `json:"versions" yaml:"versions"`
	MajorPaths     []string            `json:"major_paths,omitempty" yaml:"major_paths,omitempty"`
	Latest         *VersionEvidence    `json:"latest,omitempty" yaml:"latest,omitempty"`
	Reason         string              `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// DriftVersionUse links one observed version to the repositories that use it.
type DriftVersionUse struct {
	Version      string   `json:"version" yaml:"version"`
	Kind         string   `json:"kind" yaml:"kind"` // selected or declared
	Repositories []string `json:"repositories" yaml:"repositories"`
}

// DriftRepository is one inspected checkout.
type DriftRepository struct {
	Repository   string            `json:"repository" yaml:"repository"`
	Path         string            `json:"path" yaml:"path"`
	Status       string            `json:"status" yaml:"status"`
	Reason       string            `json:"reason,omitempty" yaml:"reason,omitempty"`
	Dependencies []DriftDependency `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
}

// DriftDependency is one module-path observation inside a repository.
type DriftDependency struct {
	Dependency  string           `json:"dependency" yaml:"dependency"`
	Manifest    string           `json:"manifest" yaml:"manifest"`
	Declared    VersionEvidence  `json:"declared" yaml:"declared"`
	Selected    VersionEvidence  `json:"selected" yaml:"selected"`
	Replacement *ReplaceEvidence `json:"replacement,omitempty" yaml:"replacement,omitempty"`
	Latest      *VersionEvidence `json:"latest,omitempty" yaml:"latest,omitempty"`
	Edges       []DriftEdge      `json:"edges,omitempty" yaml:"edges,omitempty"`
}

// VersionEvidence records one observed version value and how it was obtained.
type VersionEvidence struct {
	Value      string    `json:"value,omitempty" yaml:"value,omitempty"`
	ObservedAt time.Time `json:"observed_at" yaml:"observed_at"`
	Source     string    `json:"source" yaml:"source"`
	Reason     string    `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// ReplaceEvidence records a go.mod replace directive.
type ReplaceEvidence struct {
	OldPath    string    `json:"old_path" yaml:"old_path"`
	OldVersion string    `json:"old_version,omitempty" yaml:"old_version,omitempty"`
	NewPath    string    `json:"new_path" yaml:"new_path"`
	NewVersion string    `json:"new_version,omitempty" yaml:"new_version,omitempty"`
	Local      bool      `json:"local" yaml:"local"`
	ObservedAt time.Time `json:"observed_at" yaml:"observed_at"`
	Source     string    `json:"source" yaml:"source"`
}

// DriftEdge is a direct manifest requirement edge (not a full MVS forcing path).
type DriftEdge struct {
	ConsumerModule string `json:"consumer_module" yaml:"consumer_module"`
	Dependency     string `json:"dependency" yaml:"dependency"`
	Version        string `json:"version" yaml:"version"`
	Manifest       string `json:"manifest" yaml:"manifest"`
	Indirect       bool   `json:"indirect,omitempty" yaml:"indirect,omitempty"`
}
