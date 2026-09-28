package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/gitops"
)

func newRepoInitRemoteCmd() *cobra.Command {
	return newRepoInitRemoteCmdWithOps(defaultRepoInitRemoteOps())
}

type repoInitRemoteOps struct {
	skipSync        func(string) (bool, error)
	originURL       func(string) (string, error)
	currentBranch   func(string) (string, error)
	hasCommits      func(string) (bool, error)
	commitEmpty     func(string, string) error
	pushSetUpstream func(string, string) error
}

func defaultRepoInitRemoteOps() repoInitRemoteOps {
	return repoInitRemoteOps{
		skipSync: gitops.SkipSync, originURL: gitops.OriginURL,
		currentBranch: gitops.CurrentBranch, hasCommits: gitops.HasCommits,
		commitEmpty: gitops.CommitEmpty, pushSetUpstream: gitops.PushSetUpstream,
	}
}

func newRepoInitRemoteCmdWithOps(ops repoInitRemoteOps) *cobra.Command {
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
			return runRepoInitRemoteWithOps(path, ops)
		},
	}
	return command
}

// runRepoInitRemoteWithOps validates before it mutates: a repo that fails any of the
// first three checks is left exactly as it was found, rather than carrying an
// empty commit created for a push that was never going to run.
func runRepoInitRemoteWithOps(path string, ops repoInitRemoteOps) error {
	skip, err := ops.skipSync(path)
	if err != nil {
		return err
	}
	if skip {
		return fmt.Errorf("%s is marked %s, so wb sync would skip it anyway; run `wb repo ignore --unset %s` first",
			path, gitops.SkipSyncKey, path)
	}

	if _, err := ops.originURL(path); err != nil {
		return fmt.Errorf("%s has no origin remote to publish to: %w", path, err)
	}

	branch, err := ops.currentBranch(path)
	if err != nil {
		return err
	}
	if branch == "" {
		return fmt.Errorf("%s has a detached HEAD; check out a branch first", path)
	}

	hasCommits, err := ops.hasCommits(path)
	if err != nil {
		return err
	}
	if !hasCommits {
		if err := ops.commitEmpty(path, "Initial commit"); err != nil {
			return err
		}
		fmt.Printf("%s: created an empty initial commit on %s\n", path, branch)
	}

	if err := ops.pushSetUpstream(path, branch); err != nil {
		return err
	}
	fmt.Printf("%s: pushed %s to origin and set it as upstream\n", path, branch)
	return nil
}
