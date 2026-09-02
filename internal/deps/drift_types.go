package deps

import (
	"context"
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
	DriftBehindLatest   DriftClassification = "behind_latest"
	DriftUnavailable    DriftClassification = "unavailable"
	DriftError          DriftClassification = "error"
)

// DriftOptions controls read-only drift analysis.
type DriftOptions struct {
	// Ecosystem selects which manifests are inspected: go.mod files, or
	// package.json/pnpm-workspace.yaml plus their governing lockfiles.
	Ecosystem    Ecosystem
	GitHubDir    string
	Ref          string
	Parallel     int
	Timeout      time.Duration
	Retry        int
	GoPrivate    []string
	Dependencies []string
	// Scopes are glob patterns matched against a dependency's module path or
	// package name, using path.Match semantics ("*" never crosses "/").
	// "@sneat/*" and "github.com/sneat-co/*" are the fleet's own-library
	// scopes; retaining only those is what keeps an --online run's registry
	// traffic proportional to the question being asked.
	Scopes []string
	// ExcludeRepositories are glob patterns matched against "owner/name".
	// A matching repository is never inspected and is reported as excluded.
	ExcludeRepositories []string
	Online              bool
	FailOnDrift         bool
	FailOnBehind        bool
	Now                 func() time.Time
	Progress            progress.Reporter
	// LatestNpmVersion overrides the registry lookup in tests. Production
	// runs leave it nil and consult the registry through pnpm.
	LatestNpmVersion func(ctx context.Context, module string) (string, error)
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
	// Excluded lists the repositories --exclude removed from the run, so an
	// operator can always tell "nothing to report" from "never inspected".
	Excluded []string `json:"excluded,omitempty" yaml:"excluded,omitempty"`
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
	// Behind counts groups where at least one repository provably installs
	// or admits something older than the registry's latest release. It is
	// orthogonal to the classification ladder: a group can be both divergent
	// and behind, and both facts matter.
	Behind int `json:"behind" yaml:"behind"`
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
	// Behind is true when at least one repository provably lags the observed
	// latest release. BehindRepositories names them, and BehindReason says
	// what the evidence was.
	Behind             bool     `json:"behind,omitempty" yaml:"behind,omitempty"`
	BehindRepositories []string `json:"behind_repositories,omitempty" yaml:"behind_repositories,omitempty"`
	BehindReason       string   `json:"behind_reason,omitempty" yaml:"behind_reason,omitempty"`
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
	Dependency string `json:"dependency" yaml:"dependency"`
	Manifest   string `json:"manifest" yaml:"manifest"`
	// Field names the manifest section the reference lives in. It is empty
	// for Go modules, whose go.mod require block has no sections, and set for
	// npm ("dependencies", "peerDependencies", "pnpm-override", …) where the
	// section changes what the reference means.
	Field       string           `json:"field,omitempty" yaml:"field,omitempty"`
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
