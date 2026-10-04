package cmdworktree

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

type RetireDependencies struct {
	Run                 func(context.Context, worktrees.RetireOptions) (worktrees.RetireResult, error)
	Admit               func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error)
	CheckOwnership      func(context.Context, string, string) error
	ReleaseWhenComplete func(*cobra.Command, string, string) bool
}

func NewRetire(runtime shared.Runtime, deps RetireDependencies) *cobra.Command {
	var apply, jsonShortcut bool
	var format, message, preserve string
	command := &cobra.Command{
		Use:   "retire <task>",
		Short: "Preserve a task in retired Git refs and a private Work Log repository",
		Long: `Plan or apply retirement of one WB-managed worktree. The default is a dry run.
Apply commits tracked and nonignored untracked source changes on the original
branch with normal Git hooks, then publishes the exact commit as a retired/*
branch by default. Pass --preserve=tag to publish refs/tags/retired/* instead.
It pushes the actual Work Log and checkout
metadata as plain files to the configured private organization retirement
repository. Only after both remote receipts verify does it atomically delete
the old remote ref with an exact lease and create a deletion-proof tag at the
source commit, then remove the local checkout and branch.
An open pull request, changed remote ref, competing claim, or unavailable or
public retirement repository refuses retirement. Retry the same command to
resume an interrupted apply. A coordinated task with multiple repositories
must be retired one repository at a time with --filter. The configured remote
task store must be readable; WB checks claims and machine snapshots before
planning, again under the task lock, and before deleting the original ref.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if jsonShortcut {
				format = "json"
			}
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, release, err := deps.Admit(command, apply)
			if err != nil {
				return err
			}
			defer release()
			flags := runtime.Flags()
			result, err := deps.Run(command.Context(), worktrees.RetireOptions{
				ProjectsRoot: flags.ProjectsRoot, Task: args[0], Repository: flags.Filter,
				Message: message, Preserve: preserve, Apply: apply,
				RemoteOwnership: func(ctx context.Context, task string) error {
					return deps.CheckOwnership(ctx, flags.ProjectsRoot, task)
				},
			})
			if err != nil {
				return err
			}
			var releaseLeaked bool
			if apply && result.Phase == "complete" {
				releaseLeaked = deps.ReleaseWhenComplete(command, flags.ProjectsRoot, args[0])
			}
			if format == "json" {
				if err := json.NewEncoder(command.OutOrStdout()).Encode(result); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintf(command.OutOrStdout(), "%s %s %s -> %s (archive %s, phase %s)\n", result.Task, result.Repository, result.Branch, result.RetiredRef, result.ArchiveRef, result.Phase); err != nil {
					return err
				}
			}
			if releaseLeaked {
				return fmt.Errorf("task %q retirement completed but remote claim release failed", args[0])
			}
			return nil
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "apply the verified retirement plan")
	command.Flags().StringVarP(&message, "message", "m", "", "source commit message when changes remain")
	command.Flags().StringVar(&preserve, "preserve", "branch", "source preservation ref: branch or tag")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonShortcut, "json", false, "shorthand for --format=json")
	return command
}
