package cmdarchive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/archiveprune"
	"github.com/sneat-dev/wb/internal/cli/shared"
)

// Dependencies contains the domain operation used by archive clean.
type Dependencies struct {
	Clean func(context.Context, archiveprune.Options) (archiveprune.Outcome, error)
}

// New constructs the archive family without performing discovery or deletion.
func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "archive",
		Short: "Inspect and safely remove local clones of repositories archived on GitHub",
	}
	command.AddCommand(newClean(runtime, deps))
	return command
}

func newClean(runtime shared.Runtime, deps Dependencies) *cobra.Command {
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
			if err := shared.RequireOutputFormat(format, "text", "yaml", "json"); err != nil {
				return err
			}
			flags := runtime.Flags()
			outcome, err := deps.Clean(cmd.Context(), archiveprune.Options{
				ProjectsRoot:    flags.ProjectsRoot,
				Filter:          flags.Filter,
				Apply:           apply,
				DeleteUntracked: deleteUntracked,
				Progress:        cmd.ErrOrStderr(),
			})
			if err != nil {
				return err
			}
			switch format {
			case "text":
				if err := writeText(cmd.OutOrStdout(), outcome); err != nil {
					return err
				}
			case "yaml":
				// Outcome and its nested records contain only bools, strings, integers
				// and slices, with no custom MarshalYAML methods; marshaling cannot fail.
				raw, _ := yaml.Marshal(outcome)
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
				return runtime.ExitError(shared.ExitFindings, "archive clean reported errors; see the report above")
			}
			return nil
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "delete every eligible clone; the default is a dry-run plan")
	command.Flags().BoolVar(&deleteUntracked, "delete-untracked", false, "with --apply, delete only unchanged itemized untracked paths from an otherwise-safe archived clone")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text, yaml, or json")
	return command
}

// archiveCleanFailed reports domain errors. Planned results and refusals alone
// do not fail a run.
func archiveCleanFailed(outcome archiveprune.Outcome) bool {
	for _, result := range outcome.Results {
		if result.Error != "" {
			return true
		}
	}
	return false
}

func writeText(out io.Writer, outcome archiveprune.Outcome) error {
	if len(outcome.Results) == 0 {
		_, err := fmt.Fprintln(out, "no local clones matched")
		return err
	}
	var err error
	write := func(format string, args ...any) {
		if err == nil {
			_, err = fmt.Fprintf(out, format, args...)
		}
	}
	deleted, eligible, refused := 0, 0, 0
	for _, result := range outcome.Results {
		switch {
		case result.Applied:
			deleted++
			write("  deleted      %s — %s\n", result.Repository, result.Reason)
		case result.Error != "":
			write("  failed       %s — eligible but deletion failed: %s\n", result.Repository, result.Error)
		case result.Eligible:
			eligible++
			write("  would delete %s — %s\n", result.Repository, result.Reason)
		default:
			refused++
			write("  skipped      %s — %s\n", result.Repository, result.Reason)
		}
		for _, entry := range result.Untracked {
			write("    untracked %s %s (%d bytes)\n", entry.Kind, entry.Path, entry.Size)
		}
		if result.ReceiptPath != "" {
			write("    receipt %s\n", result.ReceiptPath)
		}
	}
	write("\n")
	if outcome.Apply {
		write("%d deleted, %d skipped\n", deleted, refused)
		return err
	}
	write("%d eligible, %d skipped; dry-run only, pass --apply to delete\n", eligible, refused)
	return err
}
