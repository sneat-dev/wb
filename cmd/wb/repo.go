package main

import (
	"github.com/spf13/cobra"
)

func newRepoCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "repo",
		Short: "Inspect a single local repository",
	}
	command.AddCommand(newRepoStatusCmd())
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
			})
		},
	}
	bindRepositoryStatusFlags(command, &options, &details, nil, false)
	return command
}
