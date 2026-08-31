package deps

import (
	"context"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
)

// ReleaseEvent is version evidence that starts or advances a dependency wave.
type ReleaseEvent struct {
	Dependency string `yaml:"dependency"`
	Version    string `yaml:"version"`
	Source     string `yaml:"source"`
	// CheckedAt is when WB accepted this version from the operator or last
	// confirmed it against the registry. Persisting it lets a resumed or
	// long-running campaign refresh stale events before spending another CI
	// build on a downstream PR.
	CheckedAt time.Time `yaml:"checked_at,omitempty"`
}

// BumpOptions adds wave and release discovery policy to shared lifecycle options.
type BumpOptions struct {
	Options
	// Ecosystem selects which fleet graph and adapter the wave engine uses.
	// The zero value defaults to EcosystemGo for backward compatibility with
	// every caller that predates npm support.
	Ecosystem    Ecosystem
	MaxWaves     int
	PollInterval time.Duration
	RefreshAfter time.Duration
	Previous     *BumpReport
	Persist      func(BumpReport) error
	// NoRegistry forbids every registry lookup while retaining the shared
	// fleet-graph and wave-planning algorithm. Composite publication plans use
	// it before a provider workflow has published the proposed version.
	NoRegistry bool

	// Now is injectable for deterministic event-refresh tests.
	Now func() time.Time
	// LatestGoVersion is injectable for deterministic wave tests.
	LatestGoVersion func(context.Context, string) (string, error)
	// LatestGoRelease is injectable for graph traversal through modules that
	// were updated and published before this campaign started.
	LatestGoRelease func(context.Context, string) (PublishedGoRelease, error)
	// LatestNpmVersion is the npm-ecosystem analogue of LatestGoVersion.
	LatestNpmVersion func(context.Context, string) (string, error)
	// LatestNpmRelease is the npm-ecosystem analogue of LatestGoRelease. It
	// reuses PublishedGoRelease's shape (version, requirements, source) since
	// nothing about that shape is actually Go-specific.
	LatestNpmRelease func(context.Context, string) (PublishedGoRelease, error)
}

// PublishedGoRelease is immutable registry evidence used to carry an event
// through an already-current consumer without manufacturing another release.
type PublishedGoRelease struct {
	Version      string
	Requirements map[string]string
	Source       string
}

// BumpReport is the persistent Markdown/YAML state of a wave campaign.
type BumpReport struct {
	SchemaVersion  int             `yaml:"schema_version"`
	Operation      string          `yaml:"operation"`
	Status         string          `yaml:"status"`
	Phase          BumpPhase       `yaml:"phase"`
	Progress       BumpProgress    `yaml:"progress"`
	Ecosystem      Ecosystem       `yaml:"ecosystem"`
	SeedEvents     []ReleaseEvent  `yaml:"seed_events"`
	GitHubDir      string          `yaml:"github_dir"`
	BaseRef        string          `yaml:"base_ref"`
	ValidationMode ValidationMode  `yaml:"validation_mode,omitempty"`
	Verification   []quality.Check `yaml:"verification,omitempty"`
	Parallel       int             `yaml:"parallel"`
	// RegistryLookupsSkipped records that this plan intentionally omitted
	// registry-derived carrier and stale-event evidence.
	RegistryLookupsSkipped bool                          `yaml:"registry_lookups_skipped,omitempty"`
	DiscoverySkips         []GraphDiscoverySkip          `yaml:"discovery_skips,omitempty"`
	DefaultBranchFallbacks []GraphDefaultBranchFallback  `yaml:"default_branch_fallbacks,omitempty"`
	ManifestWarnings       []GraphManifestWarning        `yaml:"manifest_warnings,omitempty"`
	AmbiguousModules       []GraphAmbiguousModuleWarning `yaml:"ambiguous_modules,omitempty"`
	Waves                  []BumpWaveReport              `yaml:"waves"`
}

// BumpPhase identifies the operation currently represented by a persisted
// report. It makes an interrupted campaign distinguish graph discovery from a
// wave that is waiting on local or remote work.
type BumpPhase string

const (
	BumpPhasePreparing        BumpPhase = "preparing"
	BumpPhaseDiscoveringGraph BumpPhase = "discovering_graph"
	BumpPhasePlanningWave     BumpPhase = "planning_wave"
	BumpPhaseProcessingWave   BumpPhase = "processing_wave"
	BumpPhasePlanned          BumpPhase = "planned"
	BumpPhaseAwaitingMerge    BumpPhase = "awaiting_merge"
	BumpPhaseAwaitingRelease  BumpPhase = "awaiting_release"
	BumpPhaseCompleted        BumpPhase = "completed"
)

// BumpProgress records the bounded unit of work for Phase. During graph
// discovery it advances once per selected repository; during wave processing
// it identifies the selected wave repositories.
type BumpProgress struct {
	Wave                  int    `yaml:"wave,omitempty"`
	RepositoriesTotal     int    `yaml:"repositories_total,omitempty"`
	RepositoriesCompleted int    `yaml:"repositories_completed,omitempty"`
	LastRepository        string `yaml:"last_repository,omitempty"`
}

// BumpWaveReport records one recalculated direct-consumer layer.
type BumpWaveReport struct {
	Index                int                   `yaml:"index"`
	Status               string                `yaml:"status"`
	ValidationMode       ValidationMode        `yaml:"validation_mode,omitempty"`
	Events               []ReleaseEvent        `yaml:"events"`
	Refreshes            []ReleaseEventRefresh `yaml:"refreshes,omitempty"`
	DeferredRepositories []string              `yaml:"deferred_repositories,omitempty"`
	Repositories         []RepositoryReport    `yaml:"repositories"`
	Releases             []ReleaseObservation  `yaml:"releases,omitempty"`
}

// ReleaseEventRefresh records the inexpensive registry check WB performs
// before a stale event is allowed to trigger another downstream build.
type ReleaseEventRefresh struct {
	Dependency string    `yaml:"dependency"`
	Before     string    `yaml:"before"`
	After      string    `yaml:"after"`
	CheckedAt  time.Time `yaml:"checked_at"`
	Reason     string    `yaml:"reason"`
}

// GraphDiscoverySkip records a repository whose discovery failed but was not
// treated as a fatal error: either its configured remote ref was unavailable
// and a local scan proved it irrelevant to the ecosystem being propagated
// (no go.mod / no package.json), or its local clone itself was unreadable
// (no usable git remote) and needs manual repair before WB can act on it —
// in that second case the repository may still be relevant, but continuing
// to hard-fail an otherwise healthy fleet campaign over one broken clone
// helps no one, so it is skipped and reported here instead.
type GraphDiscoverySkip struct {
	Repository string `json:"repository" yaml:"repository"`
	Reason     string `json:"reason" yaml:"reason"`
}

// GraphDefaultBranchFallback records a repository whose canonical clone did
// not contain the operation's configured base ref (`origin/<ref>`, "main" by
// default). Discovery did not fail or skip the repository: it fell back to
// the repository's actual origin/HEAD default branch and used that ref for
// both graph inspection and any downstream wave operation on this
// repository (see orchestrate.EnsureCanonical). Recording it here keeps the
// substitution visible in the report — the whole point of the skip/fallback
// model this package uses is that nothing is silently dropped or silently
// rewritten.
type GraphDefaultBranchFallback struct {
	Repository string `json:"repository" yaml:"repository"`
	Ref        string `json:"ref" yaml:"ref"`
}

// GraphManifestWarning records one manifest file that could not be parsed
// but did not abort discovery because it is not the repository's root
// manifest — most commonly a nested code-generator template rather than a
// real declaration WB should propagate dependencies through. A repository's
// ROOT manifest remains a fatal parse failure: WB cannot safely assume
// relevance there.
type GraphManifestWarning struct {
	Repository string `json:"repository" yaml:"repository"`
	Manifest   string `json:"manifest" yaml:"manifest"`
	Reason     string `json:"reason" yaml:"reason"`
}

// GraphAmbiguousModuleWarning records a Go module declared by more than one
// repository whose conflict was NOT treated as fatal because WB could
// deterministically pick a canonical declaration: either the module's own
// declared path names that repository (a legitimate fork keeps the
// upstream's module path, and the repository matching it is preferred), or
// the repository's own origin remote matches the {org}/{repo} its directory
// name claims while the others do not (a stale duplicate local clone left
// behind by an org move or rename, still declaring the module under its
// now-wrong directory-derived slug). Every other declaration is named as a
// duplicate so the substitution stays visible rather than silent. A module
// where no declaration can be preferred this way remains a fatal conflict
// (see goFleetGraph.validateUniqueModuleDeclarations).
type GraphAmbiguousModuleWarning struct {
	Module     string   `json:"module" yaml:"module"`
	Repository string   `json:"repository" yaml:"repository"`
	Manifest   string   `json:"manifest" yaml:"manifest"`
	Duplicates []string `json:"duplicates" yaml:"duplicates"`
	Reason     string   `json:"reason" yaml:"reason"`
}

// ReleaseObservation prevents the wave engine from inventing provider versions.
type ReleaseObservation struct {
	Module               string            `yaml:"module"`
	Repository           string            `yaml:"repository"`
	Before               string            `yaml:"before,omitempty"`
	After                string            `yaml:"after,omitempty"`
	Source               string            `yaml:"source"`
	Status               string            `yaml:"status"`
	Reason               string            `yaml:"reason"`
	ExpectedRequirements map[string]string `yaml:"expected_requirements,omitempty"`
	RequireNewer         bool              `yaml:"require_newer,omitempty"`
	CheckedAt            time.Time         `yaml:"checked_at,omitempty"`
}
