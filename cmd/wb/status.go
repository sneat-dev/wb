package main

import (
	"github.com/sneat-dev/wb/internal/cli/statusview"
	"github.com/spf13/cobra"
)

func newStatusCmd(inv *invocation) *cobra.Command {
	return statusview.NewHistorical(newCLIRuntime(inv), statusCommandDependencies())
}
