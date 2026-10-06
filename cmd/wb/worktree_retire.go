package main

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"io"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func newWorktreeRetireCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewRetire(newCLIRuntime(inv), cmdworktree.RetireDependencies{Run: worktrees.Retire, Admit: requireMutationAdmission, CheckOwnership: func(ctx context.Context, root, task string) error {
		return retireCheckRemoteOwnership(ctx, defaultRemoteDeps(), root, task)
	}, ReleaseWhenComplete: func(command *cobra.Command, root, task string) bool {
		result := retireReleaseClaim(command.Context(), root, task, remoteClaimWriter(command), worktrees.ListWithDiagnostics, func(root, task string, out io.Writer) autoReleaseResult { return releaseRemoteClaim(root, task, out) })
		return result.Leaked()
	}})
	addMutationAdmissionFlags(command)
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
