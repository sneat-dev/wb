package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

// remoteMachineRow is one machine's summary line, shared by `wb remote
// machines` and the header of each `wb remote status` section.
type remoteMachineRow struct {
	Key         string    `json:"key"`
	PublishedAt time.Time `json:"published_at"`
	Age         string    `json:"age"`
	Stale       bool      `json:"stale"`
	WBVersion   string    `json:"wb_version,omitempty"`
	Attention   int       `json:"attention"`
	Worktrees   int       `json:"worktrees"`
	Error       string    `json:"error,omitempty"`
}

// machineRows summarizes entries for rendering. now and stale together
// decide staleness; an entry with a decode error carries no age/attention.
func machineRows(entries []remotestate.Entry, now time.Time, stale time.Duration) []remoteMachineRow {
	rows := make([]remoteMachineRow, 0, len(entries))
	for _, entry := range entries {
		row := remoteMachineRow{Key: entry.Snapshot.Key(), Error: entry.Error}
		if entry.Error == "" {
			if entry.Snapshot.PublishedAt.IsZero() {
				row.Error = "snapshot has no published_at (truncated or empty file)"
			} else {
				age := now.Sub(entry.Snapshot.PublishedAt)
				row.PublishedAt = entry.Snapshot.PublishedAt
				row.Age = humanAge(age)
				row.Stale = stale > 0 && age > stale
				row.WBVersion = entry.Snapshot.WBVersion
				row.Attention = len(entry.Snapshot.Repositories)
				row.Worktrees = len(entry.Snapshot.Worktrees)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// humanAge renders a duration the way `wb fleet status` ages things: coarse
// enough to be stable in tests, fine enough to be useful.
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
// glance but still comparable exactly across machines.
func writeMachinesTable(out io.Writer, rows []remoteMachineRow) {
	_, _ = fmt.Fprintf(out, "%-32s %-20s %-10s %-6s %-10s %9s %9s\n", "MACHINE", "PUBLISHED_AT", "PUBLISHED", "STALE", "WB", "ATTENTION", "WORKTREES")
	for _, row := range rows {
		if row.Error != "" {
			_, _ = fmt.Fprintf(out, "%-32s error: %s\n", row.Key, row.Error)
			continue
		}
		stale := ""
		if row.Stale {
			stale = "STALE"
		}
		_, _ = fmt.Fprintf(out, "%-32s %-20s %-10s %-6s %-10s %9d %9d\n",
			row.Key, row.PublishedAt.UTC().Format(time.RFC3339), row.Age, stale, row.WBVersion, row.Attention, row.Worktrees)
	}
}

// writeStatusWorklist renders one section per machine for `wb remote
// status`: rows and entries are index-aligned (both come from machineRows'
// same-order entries slice).
func writeStatusWorklist(out io.Writer, entries []remotestate.Entry, rows []remoteMachineRow) {
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
		_, _ = fmt.Fprintf(out, "%s (published %s ago, %d scanned)\n", header, row.Age, entry.Snapshot.RepositoriesScanned)
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
			_, _ = fmt.Fprintf(out, "  worktree %-30s %-40s %s\n", wt.Task, wt.Repository, wt.Branch)
		}
		if len(entry.Snapshot.Repositories) == 0 && len(entry.Snapshot.Worktrees) == 0 {
			_, _ = fmt.Fprintln(out, "  clean")
		}
		_, _ = fmt.Fprintln(out)
	}
}
