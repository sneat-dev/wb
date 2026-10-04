package cmdworktree

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"github.com/spf13/cobra"
	"io"
	"time"
)

// NewActive constructs compact local and cross-machine overlap preflight.
func NewActive(runtime shared.Runtime, deps worktreerun.ActiveDependencies) *cobra.Command {
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
			flags := runtime.Flags()
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			report, err := worktreerun.CollectActive(command.Context(), deps, flags.ProjectsRoot, flags.Filter, localOnly, stale)
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
				return runtime.ExitError(shared.ExitFindings, "worktree preflight is incomplete; inspect local/remote status and use wb worktree list for unresolved local claims")
			}
			return nil
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&localOnly, "local-only", false, "skip remote snapshot lookup")
	command.Flags().DurationVar(&stale, "stale", 24*time.Hour, "mark remote snapshots older than this as stale; 0 disables age marking")
	return command
}

func writeActiveWorktreeText(out io.Writer, report worktreerun.ActiveReport) error {
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
