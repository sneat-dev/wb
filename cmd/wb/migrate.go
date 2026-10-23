package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdmigrate"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/migraterun"
	"github.com/spf13/cobra"
	"io"
)

func newMigrateCmd(inv *invocation) *cobra.Command {
	ops := migraterun.DefaultOperations()
	return cmdmigrate.New(newCLIRuntime(inv), cmdmigrate.Dependencies{Local: ops.Local, Campaign: ops.Campaign, Interactive: func(out io.Writer, nonInteractive bool) bool { return console.Interactive(out, nonInteractive) }})
}
