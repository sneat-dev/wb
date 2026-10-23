// Package cmdrepo constructs the complete single-repository command family.
package cmdrepo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/cli/statusview"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

type Dependencies struct {
	Status                     statusview.Dependencies
	SetSkipSync, UnsetSkipSync func(string) error
	InitRemote                 func(string, func(gitops.InitRemoteEvent)) error
	RecoverTransfer            func(context.Context, worktrees.RepositoryTransferCleanupOptions) (worktrees.RepositoryTransferCleanupResult, error)
}

func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "repo",
		Short: "Inspect or configure a single local repository",
	}
	command.AddCommand(statusview.NewRepository(runtime, deps.Status))
	command.AddCommand(newIgnore(deps))
	command.AddCommand(newInitRemote(deps))
	command.AddCommand(newTransfer(runtime, deps))
	return command
}

func newTransfer(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "transfer",
		Short: "Inspect or recover a repository transfer",
	}
	command.AddCommand(newCleanup(runtime, deps))
	return command
}

func newCleanup(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var receipt string
	var apply bool
	var jsonOut bool
	command := &cobra.Command{
		Use:   "cleanup",
		Short: "Resume secure retirement of a transferred repository's replacement clone",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if receipt == "" {
				return runtime.ExitError(shared.ExitUsage, "--receipt is required")
			}
			result, err := deps.RecoverTransfer(command.Context(), worktrees.RepositoryTransferCleanupOptions{
				ProjectsRoot: runtime.Flags().ProjectsRoot, ReceiptPath: receipt, Apply: apply,
			})
			if err != nil {
				return err
			}
			if jsonOut {
				if err := json.NewEncoder(command.OutOrStdout()).Encode(result); err != nil {
					return err
				}
			} else {
				state := "planned"
				if result.Applied {
					state = "completed"
				} else if !result.Eligible {
					state = "refused"
				}
				_, _ = fmt.Fprintf(command.OutOrStdout(), "Replacement cleanup  %s\nQuarantine          %s\nReceipt             %s\n", state, result.QuarantineDir, result.ReceiptPath)
				if result.Outcome != "" {
					_, _ = fmt.Fprintf(command.OutOrStdout(), "Outcome             %s\n", result.Outcome)
				}
				if result.Reason != "" {
					_, _ = fmt.Fprintf(command.OutOrStdout(), "Reason              %s\n", result.Reason)
				}
			}
			if !result.Eligible {
				return fmt.Errorf("replacement cleanup refused: %s", result.Reason)
			}
			return nil
		},
	}
	command.Flags().StringVar(&receipt, "receipt", "", "immutable pending cleanup receipt")
	command.Flags().BoolVar(&apply, "apply", false, "securely retire the exact quarantined clone")
	shared.AddJSONFormatFlags(command, &jsonOut)
	return command
}

func newIgnore(deps Dependencies) *cobra.Command {
	var unset bool
	command := &cobra.Command{
		Use:   "ignore [repository-path]",
		Short: "Mark a repository so wb sync leaves it alone",
		Long: `Mark a repository so wb sync leaves it alone.

Sets wb.skip-sync in the repository's own git config. wb sync then skips it
entirely — no clone, pull, or push — whatever its working-tree state, and
declines to remove its clone if the repository is later archived on GitHub.

Use this for a repository that has nothing to sync yet, such as one that is
still empty on GitHub, where git pull would otherwise fail on every run.

Defaults to the current directory when no path is given.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			if unset {
				if err := deps.UnsetSkipSync(path); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: wb sync re-enabled\n", path)
				return nil
			}
			if err := deps.SetSkipSync(path); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: ignored by wb sync\n", path)
			return nil
		},
	}
	command.Flags().BoolVar(&unset, "unset", false, "clear the marker and let wb sync manage this repository again")
	return command
}
func newInitRemote(deps Dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "init-remote [repository-path]",
		Short: "Publish a branch that has never been pushed",
		Long: `Publish a branch that has never been pushed.

Gives the branch an empty initial commit if it has no commits yet, then
pushes it to origin and sets it as the upstream. After this, wb sync can
pull the repository normally.

This is a one-shot fix for a repository that was never published, not a
general publish-and-merge tool: if origin already holds unrelated history
the push fails and the git error is reported as-is.

Defaults to the current directory when no path is given.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			return deps.InitRemote(path, func(event gitops.InitRemoteEvent) {
				switch event.Kind {
				case gitops.CreatedInitialCommit:
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: created an empty initial commit on %s\n", event.Path, event.Branch)
				case gitops.Published:
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: pushed %s to origin and set it as upstream\n", event.Path, event.Branch)
				}
			})
		},
	}
	return command
}
