package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/sneat-dev/wb/internal/landingcontext"
	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

func newLaneCmd(inv *invocation) *cobra.Command {
	return newLaneCommand(inv, landingcontext.ResolveOwner, wbhome.Root, landinglane.ReleaseIdle)
}

func newLaneCommand(inv *invocation, resolve func(string, string, int) landinglane.Owner, root func(string) (string, error), releaseIdle func(string, string, string, string) (bool, error)) *cobra.Command {
	command := &cobra.Command{Use: "lane", Short: "Manage landing lane ownership"}
	var target string
	release := &cobra.Command{
		Use: "release <owner/repository>", Short: "Release your idle landing lane claim",
		Long: "Release your registered session's idle landing lane claim after its WB command exits. Refuses another owner's claim or a running operation; an absent lane succeeds. Does not alter branches, worktrees or pull requests.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repository := strings.TrimSpace(args[0])
			if len(strings.Split(repository, "/")) != 2 || strings.HasPrefix(repository, "/") || strings.HasSuffix(repository, "/") || strings.TrimSpace(target) == "" {
				return usageError("lane release requires owner/repository and a nonempty --target")
			}
			owner := resolve(inv.projectsRoot, "wb lane release", os.Getpid())
			home, err := root(inv.projectsRoot)
			if err != nil {
				return err
			}
			released, err := releaseIdle(home, repository, target, owner.WBSessionID)
			if err != nil {
				return err
			}
			status := "already absent"
			if released {
				status = "released"
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "landing lane %s %s: %s\n", repository, target, status)
			return err
		},
	}
	release.Flags().StringVar(&target, "target", "main", "Target branch whose landing claim to release")
	command.AddCommand(release)
	return command
}
