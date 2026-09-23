package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func newWorktreeRetireCmd() *cobra.Command {
	var apply, jsonShortcut bool
	var format, message string
	command := &cobra.Command{
		Use:   "retire <task>",
		Short: "Preserve a task in retired Git refs and a private Work Log repository",
		Long: `Plan or apply retirement of one WB-managed worktree. The default is a dry run.
Apply commits tracked and nonignored untracked source changes on the original
branch with normal Git hooks, then publishes the exact commit as a retired/*
ref in the source repository. It pushes the actual Work Log and checkout
metadata as plain files to the configured private organization retirement
repository. Only after both remote receipts verify does it delete the old
remote ref with an exact lease and remove the local checkout and branch.
An open pull request, changed remote ref, competing claim, or unavailable or
public retirement repository refuses retirement. Retry the same command to
resume an interrupted apply. A coordinated task with multiple repositories
must be retired one repository at a time with --filter.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if jsonShortcut {
				format = "json"
			}
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, release, err := requireMutationAdmission(command, apply)
			if err != nil {
				return err
			}
			defer release()
			result, err := worktrees.Retire(command.Context(), worktrees.RetireOptions{
				ProjectsRoot: projectsRoot, Task: args[0], Repository: filterFlag,
				Message: message, Apply: apply,
			})
			if err != nil {
				return err
			}
			if apply && result.Phase == "complete" {
				retireReleaseClaim(command.Context(), projectsRoot, args[0], remoteClaimWriter(command), worktrees.ListWithDiagnostics,
					func(root, task string, out io.Writer) autoReleaseResult {
						return tryAutoRelease(defaultRemoteDeps(), root, task, out)
					})
			}
			if format == "json" {
				return json.NewEncoder(command.OutOrStdout()).Encode(result)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "%s %s %s -> %s (archive %s, phase %s)\n", result.Task, result.Repository, result.Branch, result.RetiredRef, result.ArchiveRef, result.Phase)
			return err
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "apply the verified retirement plan")
	command.Flags().StringVarP(&message, "message", "m", "", "source commit message when changes remain")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonShortcut, "json", false, "shorthand for --format=json")
	return command
}

// A filtered retirement ends only one checkout. Retain the shared task claim
// until every repository in that task has been retired locally.
func retireReleaseClaim(ctx context.Context, root, task string, out io.Writer,
	inventory func(context.Context, worktrees.ListOptions) (worktrees.ListOutcome, error),
	release func(string, string, io.Writer) autoReleaseResult,
) autoReleaseResult {
	remaining, err := inventory(ctx, worktrees.ListOptions{ProjectsRoot: root, Task: task, Workers: 1})
	if err != nil {
		return skippedAutoRelease(out, "cannot inventory remaining task worktrees: "+err.Error())
	}
	if len(remaining.Diagnostics) != 0 {
		return skippedAutoRelease(out, fmt.Sprintf("%d malformed task worktree records remain", len(remaining.Diagnostics)))
	}
	if len(remaining.Results) != 0 {
		return skippedAutoRelease(out, fmt.Sprintf("%d task worktrees remain", len(remaining.Results)))
	}
	return release(root, task, out)
}
