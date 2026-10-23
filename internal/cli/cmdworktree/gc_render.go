package cmdworktree

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/diskusage"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func printWorktreeGC(command *cobra.Command, outcome worktrees.GCOutcome) error {
	out := command.OutOrStdout()
	if len(outcome.Entries) == 0 {
		if _, err := fmt.Fprintln(out, "no WB worktrees"); err != nil {
			return err
		}
	}
	for _, entry := range outcome.Entries {
		if _, err := fmt.Fprintln(out, entry.String()); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "    %s\n", entry.Reason); err != nil {
			return err
		}
		if len(entry.Evidence) > 0 {
			if _, err := fmt.Fprintf(out, "    evidence: %v\n", entry.Evidence); err != nil {
				return err
			}
		}
		for _, warning := range entry.Warnings {
			if _, err := fmt.Fprintf(out, "    warning: %s\n", warning); err != nil {
				return err
			}
		}
		if entry.SanctionedCommand != "" {
			if _, err := fmt.Fprintf(out, "    resolve with: %s\n", entry.SanctionedCommand); err != nil {
				return err
			}
		}
		if entry.Management != "" && entry.Management != "managed" {
			if _, err := fmt.Fprintf(out, "    WB management: %s\n", entry.Management); err != nil {
				return err
			}
		}
		if entry.Error != "" {
			if _, err := fmt.Fprintf(out, "    error: %s\n", entry.Error); err != nil {
				return err
			}
		}
	}
	for _, partial := range outcome.PartialTasks {
		if _, err := fmt.Fprintf(out, "partial: task %s retired %v and left %v behind\n",
			partial.Task, partial.Retired, partial.LeftAlone); err != nil {
			return err
		}
	}
	usage := outcome.Reclaimable
	label := "reclaimable"
	if outcome.Apply {
		usage, label = outcome.Reclaimed, "reclaimed"
	}
	for _, artifact := range outcome.Artifacts {
		if _, err := fmt.Fprintf(out, "artifact %s %s: %s\n", artifact.Kind, artifact.Path, artifact.Reason); err != nil {
			return err
		}
	}
	shells := outcome.Totals["retired_shells"]
	shellLabel := "empty shells retired"
	rootStages := outcome.Totals["retired_root_stages"]
	rootStageLabel := "repository-root stages purged"
	if !outcome.Apply {
		shells, shellLabel = outcome.Totals["eligible_shells"], "empty shells to retire"
		// Future tense for the same reason the shells use it: these stages are
		// removed by --apply, so a dry run that reported them in the past tense
		// would claim work it has not done, and one that omitted them entirely
		// would print a row per stage with no figure to act on.
		rootStages, rootStageLabel = outcome.Totals["eligible_root_stages"], "repository-root stages to purge"
	}
	for _, shell := range outcome.Shells {
		if shell.Error == "" {
			continue
		}
		if _, err := fmt.Fprintf(out, "shell %s %s: %s\n", shell.Task, shell.Path, shell.Error); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(out,
		"\n%d retired, %d eligible, %d kept, %d terminal artefacts purged, %d %s, %d %s; %s %s apparent / %s unshared\n",
		outcome.Totals["retired"], outcome.Totals["eligible"], outcome.Totals["refused"],
		outcome.Totals["purged_artefacts"], rootStages, rootStageLabel, shells, shellLabel, label,
		diskusage.Human(usage.ApparentBytes), diskusage.Human(usage.UnsharedBytes))
	return err
}
