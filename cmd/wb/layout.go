package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdlayout"
	"github.com/sneat-dev/wb/internal/layout"
	"github.com/spf13/cobra"
)

func newLayoutCmd(inv *invocation) *cobra.Command {
	return cmdlayout.New(newCLIRuntime(inv), cmdlayout.Dependencies{
		Audit: layout.Audit, Clean: layout.Clean, Migrate: layout.Migrate,
	})
}
