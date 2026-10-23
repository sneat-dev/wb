package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdcatalog"

	"github.com/spf13/cobra"
)

const (
	rootGroupAgent    = "agent-workflow"
	rootGroupFleet    = "fleet-operations"
	rootGroupQuality  = "quality-delivery"
	rootGroupChange   = "dependencies-change"
	rootGroupMaintain = "maintenance"
	rootGroupLearn    = "learn"
)

func configureRootHelp(root *cobra.Command) {
	root.AddGroup(
		&cobra.Group{ID: rootGroupAgent, Title: "Agent workflow"},
		&cobra.Group{ID: rootGroupFleet, Title: "Fleet operations"},
		&cobra.Group{ID: rootGroupQuality, Title: "Quality and delivery"},
		&cobra.Group{ID: rootGroupChange, Title: "Dependencies and change"},
		&cobra.Group{ID: rootGroupMaintain, Title: "Maintenance"},
		&cobra.Group{ID: rootGroupLearn, Title: "Learn and configure"},
	)
	root.SetHelpCommand(newWBHelpCommand())
	root.SetHelpCommandGroupID(rootGroupLearn)
	root.SetCompletionCommandGroupID(rootGroupLearn)
}

func groupedRootCommand(command *cobra.Command, group string) *cobra.Command {
	command.GroupID = group
	return command
}

func newWBHelpCommand() *cobra.Command { return cmdcatalog.NewHelp(newCLIErrorRuntime().ExitError) }
func prepareHelpPresentation(root *cobra.Command, args []string) {
	cmdcatalog.PrepareHelp(root, args, persistentFlagSupport, persistentCommandID)
}
func usageRecoveryHint(root *cobra.Command, args []string) string {
	return cmdcatalog.UsageRecoveryHint(root, args)
}
