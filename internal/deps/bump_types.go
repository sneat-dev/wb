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
	SchemaVersion int             `yaml:"schema_version"`
	Operation     string          `yaml:"operation"`
	Status        string          `yaml:"status"`
	Phase         BumpPhase       `yaml:"phase"`
	Progress      BumpProgress    `yaml:"progress"`
	Ecosystem     Ecosystem       `yaml:"ecosystem"`
	SeedEvents    []ReleaseEvent  `yaml:"seed_events"`
	GitHubDir     string          `yaml:"github_dir"`
	BaseRef       string          `yaml:"base_ref"`
	Verification  []quality.Check `yaml:"verification,omitempty"`
	Parallel      int             `yaml:"parallel"`
	// RegistryLookupsSkipped records that this plan intentionally omitted
	// registry-derived carrier and stale-event evidence.
	RegistryLookupsSkipped bool                 `yaml:"registry_lookups_skipped,omitempty"`
	DiscoverySkips         []GraphDiscoverySkip `yaml:"discovery_skips,omitempty"`
	Waves                  []BumpWaveReport     `yaml:"waves"`
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
