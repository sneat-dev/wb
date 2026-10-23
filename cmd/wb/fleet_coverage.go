package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdquality"
	"github.com/spf13/cobra"
)

func newFleetCoverageCmd(inv *invocation) *cobra.Command {
	return cmdquality.NewStoredCoverage(newCLIRuntime(inv), qualityCommandDependencies())
}
