package cmdworktree

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func printRetireTaskShells(command *cobra.Command, outcome worktrees.RetireShellsOutcome) error {
	out := command.OutOrStdout()
	if len(outcome.Results) == 0 {
		_, err := fmt.Fprintln(out, "no WB task directories found")
		return err
	}
	for _, result := range outcome.Results {
		switch {
		case result.Applied:
			if _, err := fmt.Fprintf(out, "retired      %s %s\n", result.Task, result.Path); err != nil {
				return err
			}
		case result.Eligible:
			if _, err := fmt.Fprintf(out, "would retire %s %s\n", result.Task, result.Path); err != nil {
				return err
			}
		case result.Error != "":
			if _, err := fmt.Fprintf(out, "failed       %s %s: %s\n", result.Task, result.Path, result.Error); err != nil {
				return err
			}
		default:
			if _, err := fmt.Fprintf(out, "skip         %s %s: %s\n", result.Task, result.Path, result.Reason); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	if outcome.Apply {
		_, err := fmt.Fprintf(out, "%d retired\n", outcome.Totals["retired"])
		return err
	}
	_, err := fmt.Fprintf(out, "%d would retire\n", outcome.Totals["would_retire"])
	return err
}

func printWorktreeCleanup(command *cobra.Command, results []worktrees.CleanupResult, apply bool) error {
	if len(results) == 0 {
		_, err := fmt.Fprintln(command.OutOrStdout(), "no WB worktrees matched")
		return err
	}
	eligible, removed := 0, 0
	for _, result := range results {
		switch {
		case result.Applied:
			removed++
			remote := ""
			if result.RemoteDeleted {
				remote = " and remote branch"
			}
			// Say when WB, not Git, deleted the checkout. The task finished
			// either way, and an operator reading this line is the person who
			// would otherwise have found the directory still on disk.
			residue := ""
			if result.WorktreeResidueRemoved {
				residue = " (WB removed the checkout Git unregistered but could not delete)"
			}
			if _, err := fmt.Fprintf(command.OutOrStdout(), "removed %s %s%s%s%s\n", result.Task, result.Repository, remote, residue, cleanupProofNote(result)); err != nil {
				return err
			}
		case result.Eligible:
			eligible++
			if _, err := fmt.Fprintf(command.OutOrStdout(), "would remove %s %s%s\n", result.Task, result.Repository, cleanupProofNote(result)); err != nil {
				return err
			}
		default:
			if _, err := fmt.Fprintf(command.OutOrStdout(), "skip %s %s: %s\n", result.Task, result.Repository, result.Reason); err != nil {
				return err
			}
		}
	}
	if apply {
		_, err := fmt.Fprintf(command.OutOrStdout(), "%d removed\n", removed)
		return err
	}
	_, err := fmt.Fprintf(command.OutOrStdout(), "%d eligible; dry-run only, pass --apply to remove\n", eligible)
	return err
}

func cleanupProofNote(result worktrees.CleanupResult) string {
	if result.IntegrationProof == "" {
		return ""
	}
	return " (" + result.IntegrationProof + ")"
}
