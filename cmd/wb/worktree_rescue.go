package main

import (
	"github.com/sneat-dev/wb/internal/canonicalrescue"
	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/spf13/cobra"
)

func newWorktreeRescueCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewRescue(newCLIRuntime(inv), cmdworktree.RescueOperations{Inspect: canonicalrescue.Inspect, Capture: canonicalrescue.Capture, Push: canonicalrescue.Push, Restore: canonicalrescue.Restore, ScanLocal: discover.ScanLocal})
}
