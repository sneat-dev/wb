package cmdremote

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/age"
	"github.com/sneat-dev/wb/internal/remoterun"
	"github.com/sneat-dev/wb/internal/remotestate"
)

func writeMachinesTable(out io.Writer, rows []remoterun.MachineRow) {
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
func writeStatusWorklist(out io.Writer, entries []remotestate.Entry, rows []remoterun.MachineRow, claims []remoterun.ClaimRow) {
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
		_, _ = fmt.Fprintf(out, "%s (published %s, %d scanned)\n", header, age.PublishedAgo(row.Age), entry.Snapshot.RepositoriesScanned)
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
func writeClaimsTable(out io.Writer, rows []remoterun.ClaimRow) {
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
func writeRemoteStatusDiagnostics(out io.Writer, diagnostics remoterun.StatusDiagnostics) {
	_, _ = fmt.Fprintf(out, "remote provider: %s (%s); refreshed %s; stale=%d; provenance_unknown=%d\n",
		diagnostics.Provider, diagnostics.Store, diagnostics.RefreshedAt.UTC().Format(time.RFC3339), diagnostics.StaleMachines, diagnostics.UnknownProvenance)
	for _, mismatch := range diagnostics.Mismatches {
		_, _ = fmt.Fprintf(out, "warning: remote provider mismatch: %s; configured store is %s\n", mismatch, diagnostics.Store)
	}
	_, _ = fmt.Fprintln(out)
}
func writeClaimOutcome(out io.Writer, jsonOut bool, outcome remotestate.ClaimOutcome, text string) error {
	if jsonOut {
		return json.NewEncoder(out).Encode(outcome)
	}
	if text == "" {
		return nil
	}
	_, err := fmt.Fprint(out, text)
	return err
}
func writeReleaseOutcome(out io.Writer, jsonOut bool, outcome remotestate.ReleaseOutcome, text string) error {
	if jsonOut {
		return json.NewEncoder(out).Encode(outcome)
	}
	_, err := fmt.Fprint(out, text)
	return err
}
func writeEnroll(out io.Writer, result remoterun.EnrollResult, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(out).Encode(result)
	}
	_, err := fmt.Fprintf(out, "Enrolled %s\n  hub         %s\n  config      %s\n  credential  %s\n  verified    yes\n  daemon      %s\n", result.Machine, result.HubURL, result.ConfigPath, result.TokenFile, map[bool]string{true: "restarted if running", false: "restart skipped"}[result.DaemonRestart])
	return err
}
