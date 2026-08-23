package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/gitops"
)

func newRepoIgnoreCmd() *cobra.Command {
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
				if err := gitops.UnsetSkipSync(path); err != nil {
					return err
				}
				fmt.Printf("%s: wb sync re-enabled\n", path)
				return nil
			}
			if err := gitops.SetSkipSync(path); err != nil {
				return err
			}
			fmt.Printf("%s: ignored by wb sync\n", path)
			return nil
		},
	}
	command.Flags().BoolVar(&unset, "unset", false, "clear the marker and let wb sync manage this repository again")
	return command
}
