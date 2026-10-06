package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmddisk"
	"github.com/sneat-dev/wb/internal/disk"
	"github.com/spf13/cobra"
)

func newDiskCmd(inv *invocation) *cobra.Command {
	return cmddisk.New(newCLIRuntime(inv), cmddisk.Dependencies{Collect: disk.Collect})
}
