package main

import (
	"github.com/sneat-dev/wb/internal/archiveprune"
	"github.com/sneat-dev/wb/internal/cli/cmdarchive"
	"github.com/spf13/cobra"
)

func newArchiveCmd(inv *invocation) *cobra.Command {
	return cmdarchive.New(newCLIRuntime(inv), cmdarchive.Dependencies{Clean: archiveprune.Clean})
}
