package githubapp

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

// DefaultMachineStaleAfter matches WB's ordinary remote-machine freshness
// window. Hosted views keep stale rows visible; this threshold only labels
// their last published observation.
const DefaultMachineStaleAfter = 24 * time.Hour

// WorktreeFilter selects rows in the consolidated development-machine table.
// Empty strings match every value. NeedsAttention is a pointer so false can be
// selected explicitly rather than being confused with an omitted filter.
type WorktreeFilter struct {
	Machine        string
	Repository     string
	Status         string
	Stream         string
	Task           string
	NeedsAttention *bool
}

// WorktreeRow is the privacy-safe hosted projection of a published worktree.
// It intentionally has no local path, projects root, commit subject, or prompt.
type WorktreeRow struct {
	Repository      string     `json:"repository"`
	Task            string     `json:"task"`
	Stream          string     `json:"stream,omitempty"`
	Branch          string     `json:"branch"`
	Status          string     `json:"status"`
	Lifecycle       string     `json:"lifecycle"`
	OwnerStatus     string     `json:"owner_status"`
	Owner           string     `json:"owner,omitempty"`
	PullRequest     int        `json:"pull_request,omitempty"`
	PullRequestURL  string     `json:"pull_request_url,omitempty"`
	Machine         string     `json:"machine"`
	MachineSeenAt   time.Time  `json:"machine_seen_at"`
	MachineStale    bool       `json:"machine_stale"`
	LastActivityAt  *time.Time `json:"last_activity_at,omitempty"`
	PublishedAt     time.Time  `json:"published_at"`
	NeedsAttention  bool       `json:"needs_attention"`
	AttentionReason string     `json:"attention_reason,omitempty"`
}

// WorktreeTable is one generated, filterable view across authorized machines.
type WorktreeTable struct {
	GeneratedAt time.Time     `json:"generated_at"`
	Rows        []WorktreeRow `json:"rows"`
}

// WorktreeReadModel supplies the private cross-machine dashboard table.
type WorktreeReadModel interface {
	Worktrees(context.Context, Viewer, WorktreeFilter) (Access[WorktreeTable], error)
}

// RemoteSnapshotSource is the read-only subset of remotestate.Provider needed
// by the table provider.
type RemoteSnapshotSource interface {
	List(context.Context) ([]remotestate.Entry, error)
}

// MachineAccessResolver proves that one viewer may inspect one exact published
// machine. Authentication alone is never treated as machine ownership.
type MachineAccessResolver interface {
	CanViewMachine(context.Context, Viewer, string, string) (bool, error)
}

// RemoteStateWorktreeReadModel projects existing WB machine snapshots through
// a per-machine authorization boundary.
type RemoteStateWorktreeReadModel struct {
	Source     RemoteSnapshotSource
	Access     MachineAccessResolver
	Now        func() time.Time
	StaleAfter time.Duration
}

func (model RemoteStateWorktreeReadModel) Worktrees(ctx context.Context, viewer Viewer, filter WorktreeFilter) (Access[WorktreeTable], error) {
	if model.Source == nil || model.Access == nil {
		return Access[WorktreeTable]{}, ErrNoReadModel
	}
	if !viewer.Authenticated || !viewer.Member || strings.TrimSpace(viewer.UserID) == "" {
		return Access[WorktreeTable]{}, ErrPrivateData
	}
	entries, err := model.Source.List(ctx)
	if err != nil {
		return Access[WorktreeTable]{}, err
	}
	now := time.Now().UTC()
	if model.Now != nil {
		now = model.Now().UTC()
	}
	staleAfter := model.StaleAfter
	if staleAfter <= 0 {
		staleAfter = DefaultMachineStaleAfter
	}
	table := WorktreeTable{Rows: []WorktreeRow{}}
	for _, entry := range entries {
		snapshot := entry.Snapshot
		allowed, err := model.Access.CanViewMachine(ctx, viewer, snapshot.Login, snapshot.Machine)
		if err != nil {
			return Access[WorktreeTable]{}, err
		}
		if !allowed || entry.Error != "" {
			continue
		}
		if snapshot.PublishedAt.After(table.GeneratedAt) {
			table.GeneratedAt = snapshot.PublishedAt
		}
		for _, worktree := range snapshot.Worktrees {
			row := worktreeRow(snapshot, worktree, now, staleAfter)
			if worktreeMatches(row, filter) {
				table.Rows = append(table.Rows, row)
			}
		}
	}
	sort.Slice(table.Rows, func(i, j int) bool {
		left, right := table.Rows[i], table.Rows[j]
		if left.Repository != right.Repository {
			return left.Repository < right.Repository
		}
		if left.Machine != right.Machine {
			return left.Machine < right.Machine
		}
		return left.Task < right.Task
	})
	return Access[WorktreeTable]{Visibility: VisibilityPrivate, Value: table}, nil
}

func worktreeRow(snapshot remotestate.Snapshot, worktree remotestate.WorktreeState, now time.Time, staleAfter time.Duration) WorktreeRow {
	lifecycle := strings.TrimSpace(worktree.Lifecycle)
	if lifecycle == "" {
		lifecycle = "working"
	}
	ownerStatus := strings.TrimSpace(worktree.OwnerState)
	if ownerStatus == "" {
		ownerStatus = "unknown"
	}
	attention := safeAttentionReason(worktree.Attention)
	needsAttention := worktree.NeedsAttention || attention != ""
	if ownerStatus == "orphaned" {
		needsAttention = true
		if attention == "" {
			attention = "owner session is no longer active"
		}
	}
	status := lifecycle
	if needsAttention {
		status = "attention"
	}
	row := WorktreeRow{
		Repository: worktree.Repository, Task: worktree.Task, Stream: worktree.Stream,
		Branch: worktree.Branch, Status: status, Lifecycle: lifecycle,
		OwnerStatus: ownerStatus, Owner: worktree.Owner, Machine: snapshot.Machine,
		MachineSeenAt: snapshot.Heartbeat(), MachineStale: now.Sub(snapshot.Heartbeat()) > staleAfter,
		PublishedAt:    snapshot.PublishedAt,
		NeedsAttention: needsAttention, AttentionReason: attention,
	}
	if !worktree.LastActivityAt.IsZero() {
		lastActivity := worktree.LastActivityAt
		row.LastActivityAt = &lastActivity
	}
	if worktree.PullRequest != nil {
		row.PullRequest = worktree.PullRequest.Number
		row.PullRequestURL = worktree.PullRequest.URL
	}
	return row
}

func safeAttentionReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case "":
		return ""
	case "owner session is no longer active", "supersession evidence requires review", "absorption evidence requires review":
		return strings.TrimSpace(reason)
	default:
		return "worktree requires attention"
	}
}

func worktreeMatches(row WorktreeRow, filter WorktreeFilter) bool {
	return matchesWorktreeValue(row.Machine, filter.Machine) &&
		matchesWorktreeValue(row.Repository, filter.Repository) &&
		matchesWorktreeStatus(row, filter.Status) &&
		matchesWorktreeValue(row.Stream, filter.Stream) &&
		matchesWorktreeValue(row.Task, filter.Task) &&
		(filter.NeedsAttention == nil || row.NeedsAttention == *filter.NeedsAttention)
}

func matchesWorktreeStatus(row WorktreeRow, filter string) bool {
	return matchesWorktreeValue(row.Status, filter) ||
		matchesWorktreeValue(row.Lifecycle, filter) ||
		matchesWorktreeValue(row.OwnerStatus, filter)
}

func matchesWorktreeValue(value, filter string) bool {
	filter = strings.TrimSpace(filter)
	return filter == "" || strings.EqualFold(value, filter)
}

var _ WorktreeReadModel = RemoteStateWorktreeReadModel{}
