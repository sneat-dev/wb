package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

// remoteMachineRow is one machine's summary line, shared by `wb remote
// machines` and the header of each `wb remote status` section. PublishedAt/
// Age report the raw publish-data age (when the fleet was last scanned);
// SeenAt/Seen report the effective heartbeat age (Snapshot.Heartbeat(): the
// later of publish and claim activity) — the two diverge exactly when a
// machine has claimed or refreshed a task more recently than it last
// published. Stale is judged from the effective heartbeat, never from
// PublishedAt alone.
type remoteMachineRow struct {
	Key         string    `json:"key"`
	PublishedAt time.Time `json:"published_at"`
	Age         string    `json:"age"`
	SeenAt      time.Time `json:"seen_at"`
	Seen        string    `json:"seen"`
	Stale       bool      `json:"stale"`
	WBVersion   string    `json:"wb_version,omitempty"`
	Attention   int       `json:"attention"`
	Worktrees   int       `json:"worktrees"`
	Error       string    `json:"error,omitempty"`
}

// machineRows summarizes entries for rendering. now and stale together
// decide staleness (from the effective heartbeat); an entry with a decode
// error carries no age/attention. A snapshot is only an error row when BOTH
// PublishedAt and LastSeenAt are zero — a snapshot with claim activity but
// no recorded publish still has a usable heartbeat.
func machineRows(entries []remotestate.Entry, now time.Time, stale time.Duration) []remoteMachineRow {
	rows := make([]remoteMachineRow, 0, len(entries))
	for _, entry := range entries {
		row := remoteMachineRow{Key: entry.Snapshot.Key(), Error: entry.Error}
		if entry.Error == "" {
			snap := entry.Snapshot
			if snap.PublishedAt.IsZero() && snap.LastSeenAt.IsZero() {
				row.Error = "snapshot has no published_at (truncated or empty file)"
			} else {
				heartbeat := snap.Heartbeat()
				row.PublishedAt = snap.PublishedAt
				if !snap.PublishedAt.IsZero() {
					row.Age = humanAge(now.Sub(snap.PublishedAt))
				}
				row.SeenAt = heartbeat
				row.Seen = humanAge(now.Sub(heartbeat))
				row.Stale = stale > 0 && now.Sub(heartbeat) > stale
				row.WBVersion = snap.WBVersion
				row.Attention = len(snap.Repositories)
				row.Worktrees = len(snap.Worktrees)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// humanAge renders a duration the way `wb fleet status` ages things: coarse
// enough to be stable in tests, fine enough to be useful.
// publishedAgo phrases an age for prose: "just now" stays as is, "2h"
// becomes "2h ago".
func publishedAgo(age string) string {
	if age == "just now" {
		return age
	}
	return age + " ago"
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// writeMachinesTable renders one fixed-width row per machine for `wb remote
// machines`. PUBLISHED_AT carries the exact RFC3339 UTC instant; PUBLISHED
// stays the coarse relative age next to it, so a row is readable at a
// glance but still comparable exactly across machines. SEEN is the
// effective-heartbeat age (publish or claim activity, whichever is newer)
// that STALE actually keys off; it equals PUBLISHED when there has been no
// claim activity since the last publish.
func writeMachinesTable(out io.Writer, rows []remoteMachineRow) {
	_, _ = fmt.Fprintf(out, "%-32s %-20s %-10s %-10s %-6s %-10s %9s %9s\n", "MACHINE", "PUBLISHED_AT", "PUBLISHED", "SEEN", "STALE", "WB", "ATTENTION", "WORKTREES")
	for _, row := range rows {
		if row.Error != "" {
			_, _ = fmt.Fprintf(out, "%-32s error: %s\n", row.Key, row.Error)
			continue
		}
		stale := ""
		if row.Stale {
			stale = "STALE"
		}
		_, _ = fmt.Fprintf(out, "%-32s %-20s %-10s %-10s %-6s %-10s %9d %9d\n",
			row.Key, row.PublishedAt.UTC().Format(time.RFC3339), row.Age, row.Seen, stale, row.WBVersion, row.Attention, row.Worktrees)
	}
}

// writeStatusWorklist renders one section per machine for `wb remote
// status`: rows and entries are index-aligned (both come from machineRows'
// same-order entries slice). claims lists every claim in the store; a
// machine's section gets a "remote claims:" line naming the tasks whose
// Holder matches that machine's key.
func writeStatusWorklist(out io.Writer, entries []remotestate.Entry, rows []remoteMachineRow, claims []claimRow) {
	for i, entry := range entries {
		row := rows[i]
		header := fmt.Sprintf("## %s", row.Key)
		if row.Stale {
			header += " STALE"
		}
		if row.Error != "" {
			_, _ = fmt.Fprintf(out, "%s\n  error: %s\n\n", header, row.Error)
			continue
		}
		_, _ = fmt.Fprintf(out, "%s (published %s, %d scanned)\n", header, publishedAgo(row.Age), entry.Snapshot.RepositoriesScanned)
		for _, repo := range entry.Snapshot.Repositories {
			tracking := repo.Branch
			if repo.Ahead > 0 || repo.Behind > 0 {
				tracking += fmt.Sprintf(" +%d/-%d", repo.Ahead, repo.Behind)
			}
			if repo.Branch != "" && repo.Upstream == "" {
				tracking += " (no upstream)"
			}
			detail := repo.Summary
			if repo.Error != "" {
				detail = "error: " + repo.Error
			}
			_, _ = fmt.Fprintf(out, "  %-40s %-28s %s\n", repo.Repository, strings.TrimSpace(tracking), detail)
		}
		for _, wt := range entry.Snapshot.Worktrees {
			line := fmt.Sprintf("  worktree %-30s %-40s %s", wt.Task, wt.Repository, wt.Branch)
			if wt.OwnerState != "" {
				line += fmt.Sprintf(" (%s)", wt.OwnerState)
			}
			_, _ = fmt.Fprintln(out, line)
		}
		if len(entry.Snapshot.Repositories) == 0 && len(entry.Snapshot.Worktrees) == 0 {
			_, _ = fmt.Fprintln(out, "  clean")
		}
		var tasks []string
		for _, c := range claims {
			if c.Error == "" && c.Holder == row.Key {
				tasks = append(tasks, c.Task)
			}
		}
		if len(tasks) > 0 {
			_, _ = fmt.Fprintf(out, "  remote claims: %s\n", strings.Join(tasks, ", "))
		}
		_, _ = fmt.Fprintln(out)
	}
}

// claimRow is one claim's summary line, shared by `wb remote claims` and the
// "remote claims:" line under a machine's `wb remote status` section.
type claimRow struct {
	Task         string    `json:"task"`
	Holder       string    `json:"holder"`
	ClaimedAt    time.Time `json:"claimed_at"`
	HeartbeatAge string    `json:"heartbeat_age"`
	Stale        bool      `json:"stale"`
	Note         string    `json:"note,omitempty"`
	Error        string    `json:"error,omitempty"`
}

// findSnapshot returns the decodable snapshot published by login/machine, if
// any. An entry with a decode error is skipped: a corrupt snapshot cannot
// say whether its holder is stale, so it must not be mistaken for silence.
func findSnapshot(machines []remotestate.Entry, login, machine string) (remotestate.Snapshot, bool) {
	for _, m := range machines {
		if m.Error != "" {
			continue
		}
		if m.Snapshot.Login == login && m.Snapshot.Machine == machine {
			return m.Snapshot, true
		}
	}
	return remotestate.Snapshot{}, false
}

// holderStale judges staleness in the command layer, never the provider: a
// holder with no snapshot at all is stale, and one whose effective
// heartbeat (Snapshot.Heartbeat(): the later of publish and claim activity)
// is older than the --stale window is stale. Fresh claim/refresh activity
// through the store counts as liveness even when the holder's last publish
// is old.
func holderStale(machines []remotestate.Entry, login, machine string, now time.Time, stale time.Duration) bool {
	snap, ok := findSnapshot(machines, login, machine)
	if !ok {
		return true
	}
	return stale > 0 && now.Sub(snap.Heartbeat()) > stale
}

// heartbeatPhrase renders a holder's effective-heartbeat age for prose
// messages, falling back to none (e.g. "never published" or "never") when
// the holder has no snapshot in the store at all.
func heartbeatPhrase(machines []remotestate.Entry, login, machine string, now time.Time, none string) string {
	snap, ok := findSnapshot(machines, login, machine)
	if !ok {
		return none
	}
	return publishedAgo(humanAge(now.Sub(snap.Heartbeat())))
}

// holderDesc softens the holder key when it names the caller's own login on
// a different machine: "you on <machine>" rather than the bare "<login>/
// <machine>" key, since that machine is not this one.
func holderDesc(mine, theirs remotestate.Claim) string {
	if theirs.Login == mine.Login && theirs.Machine != mine.Machine {
		return "you on " + theirs.Machine
	}
	return theirs.Holder()
}

// claimRows summarizes claim entries for rendering, matching machineRows'
// shape: an entry with a decode error becomes an error row carrying only
// its task name.
func claimRows(claims []remotestate.ClaimEntry, machines []remotestate.Entry, now time.Time, stale time.Duration) []claimRow {
	rows := make([]claimRow, 0, len(claims))
	for _, entry := range claims {
		if entry.Error != "" {
			rows = append(rows, claimRow{Task: entry.Claim.Task, Error: entry.Error})
			continue
		}
		c := entry.Claim
		rows = append(rows, claimRow{
			Task:         c.Task,
			Holder:       c.Holder(),
			ClaimedAt:    c.ClaimedAt,
			HeartbeatAge: heartbeatPhrase(machines, c.Login, c.Machine, now, "never published"),
			Stale:        holderStale(machines, c.Login, c.Machine, now, stale),
			Note:         c.Note,
		})
	}
	return rows
}

// writeClaimsTable renders one fixed-width row per claim for `wb remote
// claims`, matching writeMachinesTable's error-row convention.
func writeClaimsTable(out io.Writer, rows []claimRow) {
	_, _ = fmt.Fprintf(out, "%-20s %-24s %-20s %-16s %-6s %s\n", "TASK", "HOLDER", "CLAIMED_AT", "HEARTBEAT", "STALE", "NOTE")
	for _, row := range rows {
		if row.Error != "" {
			_, _ = fmt.Fprintf(out, "%-20s error: %s\n", row.Task, row.Error)
			continue
		}
		stale := ""
		if row.Stale {
			stale = "STALE"
		}
		_, _ = fmt.Fprintf(out, "%-20s %-24s %-20s %-16s %-6s %s\n",
			row.Task, row.Holder, row.ClaimedAt.UTC().Format(time.RFC3339), row.HeartbeatAge, stale, row.Note)
	}
}
