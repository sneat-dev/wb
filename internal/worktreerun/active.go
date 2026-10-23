// Package worktreerun implements reusable worktree inspection and lifecycle operations.
package worktreerun

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"sort"
	"strings"
	"time"
)

// ActiveRow is deliberately smaller than ListResult and WorktreeState.
// It is a preflight aid, not an inspection dump: private prompt bodies, paths,
// commits, and source status do not help decide whether work overlaps.
type ActiveRow struct {
	Locality          string                        `json:"locality"` // local | remote
	Machine           string                        `json:"machine,omitempty"`
	Task              string                        `json:"task"`
	Summary           string                        `json:"summary,omitempty"`
	Repository        string                        `json:"repository"`
	Branch            string                        `json:"branch"`
	Owner             string                        `json:"owner,omitempty"`
	OwnerState        string                        `json:"owner_state"`
	LastActivityAt    string                        `json:"last_activity_at,omitempty"`
	Lifecycle         string                        `json:"lifecycle"`
	PullRequest       *remotestate.PullRequestState `json:"pull_request,omitempty"`
	SnapshotPublished string                        `json:"snapshot_published_at,omitempty"`
	SnapshotHeartbeat string                        `json:"snapshot_heartbeat_at,omitempty"`
	SnapshotStale     bool                          `json:"snapshot_stale,omitempty"`
}

type ActiveRemoteStatus struct {
	Status        string                  `json:"status"` // available | stale | unavailable | local_only
	Error         string                  `json:"error,omitempty"`
	StaleAfter    string                  `json:"stale_after,omitempty"`
	Machines      int                     `json:"machines,omitempty"`
	StaleMachines int                     `json:"stale_machines,omitempty"`
	Snapshots     []ActiveMachineSnapshot `json:"snapshots,omitempty"`
}

type ActiveLocalStatus struct {
	Status                  string `json:"status"` // available | incomplete
	OmittedUnresolvedClaims int    `json:"omitted_unresolved_claims,omitempty"`
}

type ActiveMachineSnapshot struct {
	Machine            string `json:"machine"`
	WorktreesPublished string `json:"worktrees_published_at,omitempty"`
	MachineHeartbeat   string `json:"machine_heartbeat_at,omitempty"`
	Stale              bool   `json:"stale,omitempty"`
}

type ActiveReport struct {
	SchemaVersion int                `json:"schema_version"`
	Local         ActiveLocalStatus  `json:"local"`
	Remote        ActiveRemoteStatus `json:"remote"`
	Worktrees     []ActiveRow        `json:"worktrees"`
}

// ActiveRemoteDependencies binds inventory to the authoritative configured provider and machine identity.
type ActiveRemoteDependencies struct {
	Load  func(string) (remotestate.Config, remotestate.Provider, error)
	Login func() (string, error)
	Now   func() time.Time
}

// ActiveDependencies supplies local claims/sessions and remote snapshots to preflight.
type ActiveDependencies struct {
	Claims   func(string, string) ([]worktrees.ActiveClaimSummary, error)
	Sessions func(string) ([]session.View, error)
	Remote   ActiveRemoteDependencies
}

// DefaultActiveDependencies uses the native local custody and session inventory.
func DefaultActiveDependencies(remote ActiveRemoteDependencies) ActiveDependencies {
	return ActiveDependencies{Claims: worktrees.ListActiveClaimSummaries, Sessions: listActiveSessions, Remote: remote}
}
func listActiveSessions(projectsRoot string) ([]session.View, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return nil, err
	}
	return session.List(home + "/" + session.DirName)
}

func CollectActive(ctx context.Context, deps ActiveDependencies, projectsRoot, filter string, localOnly bool, stale time.Duration) (ActiveReport, error) {
	local, err := deps.Claims(projectsRoot, filter)
	if err != nil {
		return ActiveReport{}, errors.New("local worktree inventory unavailable; run wb worktree list for details")
	}
	sessionViews, err := deps.Sessions(projectsRoot)
	if err != nil {
		return ActiveReport{}, errors.New("local session inventory unavailable; run wb session list for details")
	}
	liveSessions := activeSessionIDs(sessionViews)
	localNow := deps.Remote.Now()
	report := ActiveReport{
		SchemaVersion: 1,
		Local:         ActiveLocalStatus{Status: "available"},
		Remote:        ActiveRemoteStatus{Status: "local_only", StaleAfter: formatActiveStaleAfter(stale)},
		Worktrees:     make([]ActiveRow, 0, len(local)),
	}
	for _, claim := range local {
		ownerState, include := localClaimOwnerState(claim, liveSessions, localNow)
		if include {
			report.Worktrees = append(report.Worktrees, activeRowFromClaim(claim, ownerState))
		} else {
			report.Local.Status = "incomplete"
			report.Local.OmittedUnresolvedClaims++
		}
	}
	if localOnly {
		sortActiveWorktrees(report.Worktrees)
		return report, nil
	}

	cfg, provider, remoteErr := deps.Remote.Load(projectsRoot)
	if remoteErr != nil {
		report.Remote = ActiveRemoteStatus{Status: "unavailable", Error: "remote configuration unavailable; run wb remote status for details", StaleAfter: formatActiveStaleAfter(stale)}
		sortActiveWorktrees(report.Worktrees)
		return report, nil
	}
	login, loginErr := deps.Remote.Login()
	if loginErr != nil || strings.TrimSpace(login) == "" {
		report.Remote = ActiveRemoteStatus{Status: "unavailable", Error: "local machine identity unavailable; run wb remote status for details", StaleAfter: formatActiveStaleAfter(stale)}
		sortActiveWorktrees(report.Worktrees)
		return report, nil
	}
	entries, listErr := provider.List(ctx)
	if listErr != nil {
		report.Remote = ActiveRemoteStatus{Status: "unavailable", Error: "remote snapshots unavailable; run wb remote status for details", StaleAfter: formatActiveStaleAfter(stale)}
		sortActiveWorktrees(report.Worktrees)
		return report, nil
	}
	report.Remote = ActiveRemoteStatus{Status: "available", StaleAfter: formatActiveStaleAfter(stale)}
	now := deps.Remote.Now()
	for _, entry := range entries {
		snapshot := entry.Snapshot
		if snapshot.Login == login && snapshot.Machine == cfg.Machine {
			continue // local is a live scan, never an old self-publication.
		}
		if entry.Error != "" {
			report.Remote.Status = "unavailable"
			report.Remote.Error = "one or more remote snapshots could not be decoded; run wb remote status for details"
			continue
		}
		report.Remote.Machines++
		isStale := stale > 0 && now.Sub(snapshot.PublishedAt) > stale
		report.Remote.Snapshots = append(report.Remote.Snapshots, ActiveMachineSnapshot{
			Machine: snapshot.Key(), WorktreesPublished: formatActiveTime(snapshot.PublishedAt),
			MachineHeartbeat: formatActiveTime(snapshot.Heartbeat()), Stale: isStale,
		})
		if isStale {
			if report.Remote.Status == "available" {
				report.Remote.Status = "stale"
			}
			report.Remote.StaleMachines++
		}
		for _, worktree := range snapshot.Worktrees {
			if !matchesWorktreeFilter(worktree.Repository, filter) || !includeRemoteActive(worktree) {
				continue
			}
			summary, summaryErr := worktrees.NormalizeTaskSummary(worktree.TaskSummary)
			if summaryErr != nil {
				report.Remote.Status = "unavailable"
				report.Remote.Error = "one or more remote snapshots contained invalid task summaries; run wb remote status for details"
				summary = ""
			}
			report.Worktrees = append(report.Worktrees, ActiveRow{
				Locality: "remote", Machine: snapshot.Key(), Task: worktree.Task,
				Summary: summary, Repository: worktree.Repository, Branch: worktree.Branch,
				Owner: worktree.Owner, OwnerState: normalizedOwnerState(worktree.OwnerState),
				LastActivityAt: formatActiveTime(worktree.LastActivityAt), Lifecycle: normalizedLifecycle(worktree.Lifecycle),
				PullRequest: worktree.PullRequest, SnapshotPublished: formatActiveTime(snapshot.PublishedAt),
				SnapshotHeartbeat: formatActiveTime(snapshot.Heartbeat()), SnapshotStale: isStale,
			})
		}
	}
	sort.Slice(report.Remote.Snapshots, func(i, j int) bool {
		return report.Remote.Snapshots[i].Machine < report.Remote.Snapshots[j].Machine
	})
	sortActiveWorktrees(report.Worktrees)
	return report, nil
}

func includeRemoteActive(result remotestate.WorktreeState) bool {
	return !terminalRemoteLifecycle(result.Lifecycle) && result.OwnerState == "active"
}

func activeSessionIDs(views []session.View) map[string]bool {
	result := make(map[string]bool)
	for _, view := range views {
		if view.State == session.StateLive && view.WBSessionID != "" {
			result[view.WBSessionID] = true
		}
	}
	return result
}

func localClaimOwnerState(claim worktrees.ActiveClaimSummary, liveSessions map[string]bool, now time.Time) (string, bool) {
	if claim.WBSessionID != "" && liveSessions[claim.WBSessionID] {
		return "active", true
	}
	if !claim.RecordedAt.IsZero() && now.Sub(claim.RecordedAt) <= 24*time.Hour {
		return "recent_claim", true
	}
	return "", false
}

func terminalRemoteLifecycle(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "merged", "superseded", "terminal", "removed", "discarded":
		return true
	default:
		return false
	}
}

func activeRowFromClaim(claim worktrees.ActiveClaimSummary, ownerState string) ActiveRow {
	return ActiveRow{
		Locality: "local", Task: claim.Task, Summary: claim.TaskSummary,
		Repository: claim.Repository, Branch: claim.Branch, Owner: claim.Owner,
		OwnerState: ownerState, LastActivityAt: formatActiveTime(claim.RecordedAt), Lifecycle: "working",
	}
}

func formatActiveTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func formatActiveStaleAfter(value time.Duration) string {
	if value <= 0 {
		return ""
	}
	return value.String()
}

func normalizedOwnerState(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func normalizedLifecycle(value string) string {
	if strings.TrimSpace(value) == "" {
		return "working"
	}
	return value
}

func matchesWorktreeFilter(repository, filter string) bool {
	return filter == "" || strings.Contains(strings.ToLower(repository), strings.ToLower(filter))
}

func sortActiveWorktrees(rows []ActiveRow) {
	sort.Slice(rows, func(i, j int) bool {
		for _, pair := range [][2]string{{rows[i].Repository, rows[j].Repository}, {rows[i].Task, rows[j].Task}, {rows[i].Machine, rows[j].Machine}} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return rows[i].Branch < rows[j].Branch
	})
}
