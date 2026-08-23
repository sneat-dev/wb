package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/gitops"
)

func newRepoInitRemoteCmd() *cobra.Command {
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
			return runRepoInitRemote(path)
		},
	}
	return command
}

// runRepoInitRemote validates before it mutates: a repo that fails any of the
// first three checks is left exactly as it was found, rather than carrying an
// empty commit created for a push that was never going to run.
func runRepoInitRemote(path string) error {
	skip, err := gitops.SkipSync(path)
	if err != nil {
		return err
	}
	if skip {
		return fmt.Errorf("%s is marked %s, so wb sync would skip it anyway; run `wb repo ignore --unset %s` first",
			path, gitops.SkipSyncKey, path)
	}

	if _, err := gitops.OriginURL(path); err != nil {
		return fmt.Errorf("%s has no origin remote to publish to: %w", path, err)
	}

	branch, err := gitops.CurrentBranch(path)
	if err != nil {
		return err
	}
	if branch == "" {
		return fmt.Errorf("%s has a detached HEAD; check out a branch first", path)
	}

	hasCommits, err := gitops.HasCommits(path)
	if err != nil {
		return err
	}
	if !hasCommits {
		if err := gitops.CommitEmpty(path, "Initial commit"); err != nil {
			return err
		}
		fmt.Printf("%s: created an empty initial commit on %s\n", path, branch)
	}

	if err := gitops.PushSetUpstream(path, branch); err != nil {
		return err
	}
	fmt.Printf("%s: pushed %s to origin and set it as upstream\n", path, branch)
	return nil
}
