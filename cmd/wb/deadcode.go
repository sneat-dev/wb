package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdquality"
	"github.com/spf13/cobra"
)

func newDeadcodeCmd() *cobra.Command {
	return cmdquality.NewDeadcode(newCLIRuntime(&invocation{}), qualityCommandDependencies())
}
