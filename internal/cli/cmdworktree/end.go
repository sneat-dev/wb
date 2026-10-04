package cmdworktree

import (
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktreeend"
	"github.com/spf13/cobra"
	"io"
)

func NewEnd(runtime shared.Runtime, factory func(string, io.Writer) (*worktreeend.Engine, error)) *cobra.Command {
	var (
		repository, note, format string
		apply, keepCapture       bool
	)
	command := &cobra.Command{
		Use:   "end <task>",
		Short: "Close a task: capture uncommitted work, seal a note, retire the worktrees, release the claim",
		Long: `End is how an agent finishes. It is the closing half of 'wb worktree create'
and the last line of every lane contract.

In order, and the order is the contract:

  1. refuse while any worktree holds a live local link — a checkout that builds
     against an unpublished library working tree must never be retired silently
  2. capture uncommitted work and print where it went, BEFORE anything is
     removed
  3. seal a closing note into the Work Log
  4. retire each worktree through the existing 'wb worktree cleanup' transaction
  5. release the fleet-wide claim, but only once every worktree is gone

A dirty worktree is NOT a refusal. Refusing one would leave exactly the choice
this verb exists to remove: hand-roll the removal, or leave residue. The
uncommitted work is captured as a git stash commit in the repository the
worktree was cut from — it survives the worktree's removal — and the exact ref
is printed. Recover it with 'git stash apply <ref>' or 'git show <ref>'.

Retirement itself is the existing cleanup transaction, so its own guards still
apply: an unmerged branch is refused by cleanup with its reason, not silently
deleted here.

The default is a dry-run plan; --apply performs the retirement.`,
		Example: `# See what ending would do
wb worktree end improve-login

# Close it
wb worktree end improve-login --apply

# Close one repository of a coordinated task
wb worktree end improve-login --repo acme/app --apply --note "landed in #412"`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			engine, err := factory(runtime.Flags().ProjectsRoot, command.ErrOrStderr())
			if err != nil {
				return err
			}
			result, err := engine.End(command.Context(), worktreeend.Options{
				Task: args[0], Repository: repository, Note: note,
				Apply: apply, KeepCapture: keepCapture,
			})
			if err != nil {
				// A guard that fired is exit 2 with the command that
				// satisfies it; a failure is exit 1. errors.As rather than a
				// type assertion, so a refusal still reaches the caller after
				// any later wrapping.
				var refusal *worktreeend.Refusal
				if errors.As(err, &refusal) {
					return runtime.ExitError(shared.ExitUsage, refusal.Error())
				}
				return err
			}
			if err := printWorktreeEnd(command, format, result); err != nil {
				return err
			}
			if result.Failed() {
				return runtime.ExitError(shared.ExitFindings, "wb worktree end reported findings; see the report above")
			}
			return nil
		},
	}
	command.Flags().StringVar(&repository, "repo", "", "narrow a coordinated task to one owner/repository")
	command.Flags().StringVar(&note, "note", "", "closing statement sealed into the Work Log")
	command.Flags().BoolVar(&apply, "apply", false, "perform the retirement; without it nothing is changed")
	command.Flags().BoolVar(&keepCapture, "no-capture", false, "do not capture uncommitted work (only when it is already preserved elsewhere)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}
