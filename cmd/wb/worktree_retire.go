package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/sneat-dev/wb/internal/remotestate"
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
				RemoteOwnership: func(ctx context.Context, task string) error {
					return retireCheckRemoteOwnership(ctx, defaultRemoteDeps(), projectsRoot, task)
				},
			})
			if err != nil {
				return err
			}
			var releaseLeaked bool
			if apply && result.Phase == "complete" {
				releaseResult := retireReleaseClaim(command.Context(), projectsRoot, args[0], remoteClaimWriter(command), worktrees.ListWithDiagnostics,
					func(root, task string, out io.Writer) autoReleaseResult {
						return tryAutoRelease(defaultRemoteDeps(), root, task, out)
					})
				releaseLeaked = releaseResult.Leaked()
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
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonShortcut, "json", false, "shorthand for --format=json")
	return command
}

// Retirement requires a fresh remote store read. A missing configuration or
// unreadable snapshot cannot prove that another machine has released the task.
func retireCheckRemoteOwnership(ctx context.Context, deps remoteDeps, root, task string) error {
	cfg, provider, err := loadRemote(deps, root)
	if err != nil {
		return fmt.Errorf("load remote task ownership: %w", err)
	}
	login, err := deps.login()
	if err != nil {
		return fmt.Errorf("determine remote task owner: %w", err)
	}
	if login == "" {
		return fmt.Errorf("determine remote task owner: empty login")
	}
	status, err := remotestate.ReadStatus(ctx, provider)
	if err != nil {
		return fmt.Errorf("read remote task ownership: %w", err)
	}
	for _, claim := range status.Claims {
		if claim.Error != "" {
			return fmt.Errorf("remote claim %s is unreadable: %s", claim.Claim.Task, claim.Error)
		}
		if claim.Claim.Task != task {
			continue
		}
		if claim.Claim.Login != login || claim.Claim.Machine != cfg.Machine {
			return fmt.Errorf("task %s has competing remote claim held by %s", task, claim.Claim.Holder())
		}
	}
	for _, machine := range status.Machines {
		if machine.Error != "" {
			return fmt.Errorf("unreadable remote machine snapshot for %s: %s", machine.Snapshot.Key(), machine.Error)
		}
		for _, checkout := range machine.Snapshot.Worktrees {
			if checkout.Task == task && (machine.Snapshot.Login != login || machine.Snapshot.Machine != cfg.Machine) {
				return fmt.Errorf("task %s has competing remote checkout on %s", task, machine.Snapshot.Key())
			}
		}
	}
	return nil
}

// A filtered retirement ends only one checkout. Retain the shared task claim
// until every repository in that task has been retired locally.
func retireReleaseClaim(ctx context.Context, root, task string, out io.Writer,
	inventory func(context.Context, worktrees.ListOptions) (worktrees.ListOutcome, error),
	release func(string, string, io.Writer) autoReleaseResult,
) autoReleaseResult {
	remaining, err := inventory(ctx, worktrees.ListOptions{ProjectsRoot: root, Task: task, Workers: 1})
	if err != nil {
		return failedAutoRelease(out, task, "cannot inventory remaining task worktrees: "+err.Error())
	}
	if len(remaining.Diagnostics) != 0 {
		return failedAutoRelease(out, task, fmt.Sprintf("%d malformed task worktree records remain", len(remaining.Diagnostics)))
	}
	if len(remaining.Results) != 0 {
		return skippedAutoRelease(out, fmt.Sprintf("%d task worktrees remain", len(remaining.Results)))
	}
	result := release(root, task, out)
	if result.Outcome != "released" && result.Outcome != "noop" && !result.Leaked() {
		return failedAutoRelease(out, task, "remote claim release outcome: "+result.Outcome+" "+result.Detail)
	}
	return result
}
