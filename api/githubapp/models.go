// Package githubapp defines the Workbench GitHub App control-plane API.
package githubapp

import (
	"encoding/json"
	"time"
)

const (
	// ControlPlaneOrigin is the dedicated machine API origin.
	ControlPlaneOrigin = "https://wb-github-app.sneat.dev"
	// ControlPlaneHost is the host-only form used by the Cloud Run adapter.
	ControlPlaneHost = "wb-github-app.sneat.dev"
	// UIOrigin is the browser origin permitted to call the control-plane API.
	UIOrigin = "https://sneat.work"
	// APIPrefix is mounted by the host application.
	APIPrefix = "/v0/workbench"
)

// Scope identifies the GitHub subject represented by a statistic.
type Scope string

const (
	ScopeRepository   Scope = "repository"
	ScopeOrganization Scope = "organization"
	ScopeUser         Scope = "user"
)

// Visibility describes whether a response contains an explicitly opted-in
// public subject or a member-only private subject.
type Visibility string

const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
)

// Viewer is resolved by the host before a private record is rendered.
type Viewer struct {
	Authenticated bool
	Member        bool
	UserID        string
}

// PublicEligibility is the auditable opt-in record required before a
// repository can appear in unauthenticated results. READMEURL may point to the
// repository's free-eligibility declaration; it is not inferred from a public
// GitHub repository alone.
type PublicEligibility struct {
	Repository string    `json:"repository"`
	READMEURL  string    `json:"readme_url"`
	GrantedAt  time.Time `json:"granted_at"`
}

// Link is a canonical GitHub, release, or Workbench receipt reference.
type Link struct {
	Kind string `json:"kind"`
	Href string `json:"href"`
}

// Summary is the compact dashboard card set.
type Summary struct {
	Repositories int `json:"repositories"`
	OpenPulls    int `json:"open_pulls"`
	MergedPulls  int `json:"merged_pulls"`
	OpenIssues   int `json:"open_issues"`
	Releases     int `json:"releases"`
}

// Dashboard is the top-level dashboard response.
type Dashboard struct {
	GeneratedAt time.Time `json:"generated_at"`
	Summary     Summary   `json:"summary"`
	Links       []Link    `json:"links,omitempty"`
}

// Stat is one scoped repository, organization, or user result.
type Stat struct {
	Scope       Scope     `json:"scope"`
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	Summary     Summary   `json:"summary"`
	UpdatedAt   time.Time `json:"updated_at"`
	Links       []Link    `json:"links,omitempty"`
}

// SeriesPoint can render either a graph point or a table row.
type SeriesPoint struct {
	At    time.Time `json:"at"`
	Value int64     `json:"value"`
}

// Series is a named time-series for graph and table consumers.
type Series struct {
	Scope  Scope         `json:"scope"`
	ID     string        `json:"id"`
	Metric string        `json:"metric"`
	Points []SeriesPoint `json:"points"`
}

// LeaderboardEntry is intentionally small so public leaderboards do not leak
// private repository names or private activity counts.
type LeaderboardEntry struct {
	Rank        int    `json:"rank"`
	SubjectID   string `json:"subject_id"`
	DisplayName string `json:"display_name"`
	Value       int64  `json:"value"`
}

// Leaderboard groups ranked values for a requested metric.
type Leaderboard struct {
	Metric  string             `json:"metric"`
	Entries []LeaderboardEntry `json:"entries"`
}

// LatestMerge gives the dashboard every navigable artifact around a merge.
type LatestMerge struct {
	Repository     string    `json:"repository"`
	PullRequest    int       `json:"pull_request"`
	MergedAt       time.Time `json:"merged_at"`
	PullRequestURL string    `json:"pull_request_url,omitempty"`
	IssueURL       string    `json:"issue_url,omitempty"`
	MergeCommitSHA string    `json:"merge_commit_sha,omitempty"`
	MergeCommitURL string    `json:"merge_commit_url,omitempty"`
	ReleaseURL     string    `json:"release_url,omitempty"`
	ReceiptURL     string    `json:"receipt_url,omitempty"`
}

// Access wraps a read-model result with its disclosure class.
type Access[T any] struct {
	Visibility Visibility
	Value      T
}

// EventType classifies a live Workbench daemon update. WebSocket is reserved
// for later bidirectional controls such as cancel and reprioritize.
type EventType string

const (
	EventQueue            EventType = "queue"
	EventJobPhase         EventType = "job.phase"
	EventJobProgress      EventType = "job.progress"
	EventCI               EventType = "ci"
	EventCleanup          EventType = "cleanup"
	EventSync             EventType = "sync"
	EventDaemonGeneration EventType = "daemon.generation"
)

// Event is an SSE record. ID is a durable monotonic cursor, not a timestamp.
// Payload is only serialized after the viewer passes its visibility check.
type Event struct {
	ID         uint64          `json:"id"`
	Type       EventType       `json:"type"`
	Visibility Visibility      `json:"visibility"`
	At         time.Time       `json:"at"`
	Repository string          `json:"repository,omitempty"`
	Task       string          `json:"task,omitempty"`
	Operation  string          `json:"operation,omitempty"`
	Session    string          `json:"session,omitempty"`
	Severity   string          `json:"severity,omitempty"`
	Payload    json.RawMessage `json:"payload"`
}

// EventFilter selects one resumable daemon/direct-WB event sequence. Work Logs
// remain immutable per-task evidence; this filter is transient monitoring only.
type EventFilter struct {
	After      uint64
	Since      time.Time
	Repository string
	Task       string
	Operation  string
	Session    string
	Severity   string
}
