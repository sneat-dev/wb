package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdinstall"
	"github.com/sneat-dev/wb/internal/wbupdate"
	"github.com/spf13/cobra"
)

func newUpgradeCmd() *cobra.Command {
	command := cmdinstall.NewUpgrade(newCLIErrorRuntime(), wbupdate.Config(collectVersion().Version), maintenanceUpdateService().AfterUpdate)
	setDiscoveryTerms(command, "upgrade fleet cli sibling update latest release brew cask relevant catalog check")
	return command
}
