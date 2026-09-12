package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

// activeWorktreeRow is deliberately smaller than ListResult and WorktreeState.
// It is a preflight aid, not an inspection dump: private prompt bodies, paths,
// commits, and source status do not help decide whether work overlaps.
type activeWorktreeRow struct {
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

type activeRemoteStatus struct {
	Status        string                  `json:"status"` // available | stale | unavailable | local_only
	Error         string                  `json:"error,omitempty"`
	StaleAfter    string                  `json:"stale_after,omitempty"`
	Machines      int                     `json:"machines,omitempty"`
	StaleMachines int                     `json:"stale_machines,omitempty"`
	Snapshots     []activeMachineSnapshot `json:"snapshots,omitempty"`
}

type activeLocalStatus struct {
	Status                  string `json:"status"` // available | incomplete
	OmittedUnresolvedClaims int    `json:"omitted_unresolved_claims,omitempty"`
}

type activeMachineSnapshot struct {
	Machine            string `json:"machine"`
	WorktreesPublished string `json:"worktrees_published_at,omitempty"`
	MachineHeartbeat   string `json:"machine_heartbeat_at,omitempty"`
	Stale              bool   `json:"stale,omitempty"`
}

type activeWorktreeReport struct {
	SchemaVersion int                 `json:"schema_version"`
	Local         activeLocalStatus   `json:"local"`
	Remote        activeRemoteStatus  `json:"remote"`
	Worktrees     []activeWorktreeRow `json:"worktrees"`
}

type activeWorktreeDeps struct {
	claims   func(string, string) ([]worktrees.ActiveClaimSummary, error)
	sessions func(string) ([]session.View, error)
	remote   remoteDeps
}

func defaultActiveWorktreeDeps() activeWorktreeDeps {
	return activeWorktreeDeps{claims: worktrees.ListActiveClaimSummaries, sessions: listActiveSessions, remote: defaultRemoteDeps()}
}

func listActiveSessions(projectsRoot string) ([]session.View, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return nil, err
	}
	return session.List(home + "/" + session.DirName)
}

func newWorktreeActiveCmd() *cobra.Command {
	return newWorktreeActiveCmdWithDeps(defaultActiveWorktreeDeps())
}

func newWorktreeActiveCmdWithDeps(deps activeWorktreeDeps) *cobra.Command {
	var format string
	var localOnly bool
	var stale time.Duration
	command := &cobra.Command{
		Use:   "active",
		Short: "Compact local and cross-machine worktree overlap preflight",
		Long: `Read matching nonterminal Work Log claims, then read configured remote
machine snapshots for the same repository filter as one compact cross-machine
preflight. The compact result is for
deciding whether a task already has overlapping work; its rows never add private
prompt bodies or worktree checkout paths. A local claim remains visible until it is
sealed while its registered session is live; a claim from the last 24 hours remains
visible as recent even if process liveness cannot be proved. Older unresolved claims
belong in the full recovery inventory from wb worktree list.

Remote snapshots are not an atomic lock. A different task name may still
overlap, so inspect a plausible row and coordinate or make an audited handoff
before creating another worktree. The local machine is re-scanned and its
older published snapshot is excluded. Remote failures and stale snapshots are
reported explicitly rather than treated as an empty result.

Use --local-only when no remote is configured or a local inventory is enough.
Use --format json for agents and automation.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			report, err := runWorktreeActive(command.Context(), deps, projectsRoot, filterFlag, localOnly, stale)
			if err != nil {
				return err
			}
			if format == "json" {
				if err := json.NewEncoder(command.OutOrStdout()).Encode(report); err != nil {
					return err
				}
			} else if err := writeActiveWorktreeText(command.OutOrStdout(), report); err != nil {
				return err
			}
			if report.Local.Status == "incomplete" || report.Remote.Status == "unavailable" || report.Remote.Status == "stale" {
				return &exitError{code: exitFindings, message: "worktree preflight is incomplete; inspect local/remote status and use wb worktree list for unresolved local claims"}
			}
			return nil
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&localOnly, "local-only", false, "skip remote snapshot lookup")
	command.Flags().DurationVar(&stale, "stale", 24*time.Hour, "mark remote snapshots older than this as stale; 0 disables age marking")
	return command
}

func runWorktreeActive(ctx context.Context, deps activeWorktreeDeps, projectsRoot, filter string, localOnly bool, stale time.Duration) (activeWorktreeReport, error) {
	local, err := deps.claims(projectsRoot, filter)
	if err != nil {
		return activeWorktreeReport{}, errors.New("local worktree inventory unavailable; run wb worktree list for details")
	}
	sessionViews, err := deps.sessions(projectsRoot)
	if err != nil {
		return activeWorktreeReport{}, errors.New("local session inventory unavailable; run wb session list for details")
	}
	liveSessions := activeSessionIDs(sessionViews)
	localNow := deps.remote.now()
	report := activeWorktreeReport{
		SchemaVersion: 1,
		Local:         activeLocalStatus{Status: "available"},
		Remote:        activeRemoteStatus{Status: "local_only", StaleAfter: formatActiveStaleAfter(stale)},
		Worktrees:     make([]activeWorktreeRow, 0, len(local)),
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

	cfg, provider, remoteErr := loadRemote(deps.remote, projectsRoot)
	if remoteErr != nil {
		report.Remote = activeRemoteStatus{Status: "unavailable", Error: "remote configuration unavailable; run wb remote status for details", StaleAfter: formatActiveStaleAfter(stale)}
		sortActiveWorktrees(report.Worktrees)
		return report, nil
	}
	login, loginErr := deps.remote.login()
	if loginErr != nil || strings.TrimSpace(login) == "" {
		report.Remote = activeRemoteStatus{Status: "unavailable", Error: "local machine identity unavailable; run wb remote status for details", StaleAfter: formatActiveStaleAfter(stale)}
		sortActiveWorktrees(report.Worktrees)
		return report, nil
	}
	entries, listErr := provider.List(ctx)
	if listErr != nil {
		report.Remote = activeRemoteStatus{Status: "unavailable", Error: "remote snapshots unavailable; run wb remote status for details", StaleAfter: formatActiveStaleAfter(stale)}
		sortActiveWorktrees(report.Worktrees)
		return report, nil
	}
	report.Remote = activeRemoteStatus{Status: "available", StaleAfter: formatActiveStaleAfter(stale)}
	now := deps.remote.now()
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
		report.Remote.Snapshots = append(report.Remote.Snapshots, activeMachineSnapshot{
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
			report.Worktrees = append(report.Worktrees, activeWorktreeRow{
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

func activeRowFromClaim(claim worktrees.ActiveClaimSummary, ownerState string) activeWorktreeRow {
	return activeWorktreeRow{
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

func sortActiveWorktrees(rows []activeWorktreeRow) {
	sort.Slice(rows, func(i, j int) bool {
		for _, pair := range [][2]string{{rows[i].Repository, rows[j].Repository}, {rows[i].Task, rows[j].Task}, {rows[i].Machine, rows[j].Machine}} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return rows[i].Branch < rows[j].Branch
	})
}

func writeActiveWorktreeText(out io.Writer, report activeWorktreeReport) error {
	if _, err := fmt.Fprintf(out, "local: %s", report.Local.Status); err != nil {
		return err
	}
	if report.Local.OmittedUnresolvedClaims > 0 {
		if _, err := fmt.Fprintf(out, " (%d unresolved claims omitted; inspect wb worktree list)", report.Local.OmittedUnresolvedClaims); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "remote: %s", report.Remote.Status); err != nil {
		return err
	}
	if report.Remote.Error != "" {
		if _, err := fmt.Fprintf(out, " (%s)", report.Remote.Error); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	for _, row := range report.Worktrees {
		machine := row.Machine
		if machine == "" {
			machine = "local"
		}
		if _, err := fmt.Fprintf(out, "%s %s %s %s %s [%s/%s]", row.Locality, machine, row.Repository, row.Task, row.Branch, row.OwnerState, row.Lifecycle); err != nil {
			return err
		}
		if row.Summary != "" {
			if _, err := fmt.Fprintf(out, " — %s", row.Summary); err != nil {
				return err
			}
		}
		if row.SnapshotStale {
			if _, err := fmt.Fprint(out, " (STALE SNAPSHOT)"); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}
	return nil
}
