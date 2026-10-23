package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdinstall"
	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/cliinstall/cobracmd"
)

func newInstallCmd() *cobra.Command {
	command := cmdinstall.NewInstall(newCLIErrorRuntime(), cobracmd.CommandOptions{})
	setDiscoveryTerms(command, "install fleet cli sibling download release brew cask relevant catalog upgrade")
	return command
}
