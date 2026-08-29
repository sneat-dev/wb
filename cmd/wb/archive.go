package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/archiveprune"
)

// newArchiveCmd is the top-level `wb archive` family: a sibling of `wb
// layout` and `wb branch`, not a mode of `wb sync`. `wb sync` already removes
// a clean archived clone as a side effect of its own general-purpose dirty
// check, but that check is not the strict predicate a purpose-built,
// irreversible-on-this-machine deletion needs (it does not see a linked
// worktree, a local-only branch, an unpushed tag, or a live WB Work Log
// claim), and `wb sync` mutates by default (-n/--dry-run is opt-in) — the
// wrong default shape to inherit for a more destructive check. See
// spec/features/archived-clone-cleanup/README.md.
func newArchiveCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "archive",
		Short: "Inspect and safely remove local clones of repositories archived on GitHub",
	}
	command.AddCommand(newArchiveCleanCmd())
	return command
}

func newArchiveCleanCmd() *cobra.Command {
	var format string
	var apply bool
	var deleteUntracked bool
	command := &cobra.Command{
		Use:   "clean",
		Short: "Plan or delete local clones of repositories confirmed archived on GitHub",
		Long: `Inventory every local clone below --projects-root, confirm each one's
archived status live against GitHub, and report per clone whether it is safe
to delete and exactly why or why not. The default is a dry-run plan; --apply
is required to delete anything. Untracked files are itemized in every plan and
remain a refusal unless --apply is paired with --delete-untracked. That second
flag authorizes only the exact itemized paths after WB rereads them unchanged;
it is not a general force mode.

A clone is eligible only when every one of these holds:
  - the repository is confirmed archived on GitHub right now (a live check,
    never a name pattern and never a cached local list)
  - no uncommitted changes (untracked files require the separate, explicit
    --apply --delete-untracked authorization described above)
  - no stashes
  - no unpushed commits on any local branch, not only the checked-out one
  - no local-only branches (every local branch exists on origin)
  - no unpushed tags (every local tag exists on origin)
  - no linked worktrees registered against the clone
  - no live WB task worktree or non-terminal Work Log claim recorded against it
  - the clone is not marked wb.skip-sync

Any check that cannot be completed — GitHub unreachable, a ref that cannot be
resolved, a claim that cannot be read — makes that clone not deletable; wb
never guesses. 'wb archive clean' never removes a clone whose repository it
has not itself confirmed archived and clean in this exact run, and every
result — deleted, would-delete, or refused — is reported, never summarized
away as a bare count.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "yaml", "json"); err != nil {
				return err
			}
			outcome, err := archiveprune.Clean(cmd.Context(), archiveprune.Options{
				ProjectsRoot:    projectsRoot,
				Filter:          filterFlag,
				Apply:           apply,
				DeleteUntracked: deleteUntracked,
				Progress:        cmd.ErrOrStderr(),
			})
			if err != nil {
				return err
			}
			switch format {
			case "text":
				printArchiveClean(cmd, outcome)
			case "yaml":
				raw, err := yaml.Marshal(outcome)
				if err != nil {
					return err
				}
				if _, err := cmd.OutOrStdout().Write(raw); err != nil {
					return err
				}
			case "json":
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(outcome); err != nil {
					return err
				}
			}
			if archiveCleanFailed(outcome) {
				return &exitError{
					code:    exitFindings,
					message: "archive clean reported errors; see the report above",
				}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "delete every eligible clone; the default is a dry-run plan")
	command.Flags().BoolVar(&deleteUntracked, "delete-untracked", false, "with --apply, delete only unchanged itemized untracked paths from an otherwise-safe archived clone")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text, yaml, or json")
	return command
}

// archiveCleanFailed reports whether an apply run left a deletion error
// behind. A dry run never fails solely for planned (would-delete) results.
func archiveCleanFailed(outcome archiveprune.Outcome) bool {
	for _, result := range outcome.Results {
		if result.Error != "" {
			return true
		}
	}
	return false
}

func printArchiveClean(cmd *cobra.Command, outcome archiveprune.Outcome) {
	out := cmd.OutOrStdout()
	if len(outcome.Results) == 0 {
		_, _ = fmt.Fprintln(out, "no local clones matched")
		return
	}
	deleted, eligible, refused := 0, 0, 0
	for _, result := range outcome.Results {
		switch {
		case result.Applied:
			deleted++
			_, _ = fmt.Fprintf(out, "  deleted      %s — %s\n", result.Repository, result.Reason)
		case result.Error != "":
			_, _ = fmt.Fprintf(out, "  failed       %s — eligible but deletion failed: %s\n", result.Repository, result.Error)
		case result.Eligible:
			eligible++
			_, _ = fmt.Fprintf(out, "  would delete %s — %s\n", result.Repository, result.Reason)
		default:
			refused++
			_, _ = fmt.Fprintf(out, "  skipped      %s — %s\n", result.Repository, result.Reason)
		}
		for _, entry := range result.Untracked {
			_, _ = fmt.Fprintf(out, "    untracked %s %s (%d bytes)\n", entry.Kind, entry.Path, entry.Size)
		}
		if result.ReceiptPath != "" {
			_, _ = fmt.Fprintf(out, "    receipt %s\n", result.ReceiptPath)
		}
	}
	_, _ = fmt.Fprintln(out)
	if outcome.Apply {
		_, _ = fmt.Fprintf(out, "%d deleted, %d skipped\n", deleted, refused)
		return
	}
	_, _ = fmt.Fprintf(out, "%d eligible, %d skipped; dry-run only, pass --apply to delete\n", eligible, refused)
}
