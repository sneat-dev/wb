package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdcatalog"
	"github.com/spf13/cobra"
)

func newCommandsCmd() *cobra.Command { return cmdcatalog.NewCommands() }
func setDiscoveryTerms(command *cobra.Command, terms string) {
	cmdcatalog.SetDiscoveryTerms(command, terms)
}
