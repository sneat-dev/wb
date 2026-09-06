package main

import (
	"encoding/json"
	"fmt"

	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func newRepoCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "repo",
		Short: "Inspect or configure a single local repository",
	}
	command.AddCommand(newRepoStatusCmd())
	command.AddCommand(newRepoIgnoreCmd())
	command.AddCommand(newRepoInitRemoteCmd())
	command.AddCommand(newRepoTransferCmd())
	return command
}

func newRepoTransferCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "transfer",
		Short: "Inspect or recover a repository transfer",
	}
	command.AddCommand(newRepoTransferCleanupCmd())
	return command
}

func newRepoTransferCleanupCmd() *cobra.Command {
	var receipt string
	var apply bool
	var jsonOut bool
	command := &cobra.Command{
		Use:   "cleanup",
		Short: "Resume secure retirement of a transferred repository's replacement clone",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if receipt == "" {
				return fmt.Errorf("--receipt is required")
			}
			result, err := worktrees.RecoverRepositoryTransferCleanup(command.Context(), worktrees.RepositoryTransferCleanupOptions{
				ProjectsRoot: projectsRoot, ReceiptPath: receipt, Apply: apply,
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
	addJSONFormatFlags(command, &jsonOut)
	return command
}

func newRepoStatusCmd() *cobra.Command {
	options := qualityOptions{parallel: 4}
	var details bool
	command := &cobra.Command{
		Use:   "status [repository-path]",
		Short: "Report local Git state for one repository",
		Long: `Report local Git state for one repository checkout.

Defaults to the current directory when no path is given. Unlike wb fleet
status, this always answers for the named checkout — clean or not — and does
not scan the projects-root fleet.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			return runRepositoryStatus(repositoryStatusRequest{
				path:      path,
				fleet:     false,
				all:       true,
				details:   details,
				options:   options,
				filter:    "",
				projects:  projectsRoot,
				titleKind: statusTitleRepo,
				progress:  cmd.ErrOrStderr(),
			})
		},
	}
	bindRepositoryStatusFlags(command, &options, &details, nil, false)
	return command
}
