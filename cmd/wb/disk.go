package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmddisk"
	"github.com/sneat-dev/wb/internal/dependencycache"
	"github.com/sneat-dev/wb/internal/disk"
	"github.com/spf13/cobra"
)

func newDiskCmd(inv *invocation) *cobra.Command {
	command := cmddisk.New(newCLIRuntime(inv), cmddisk.Dependencies{Collect: disk.Collect, DependencyCache: dependencycache.Run, Prune: cmddisk.DefaultPruneDependencies()})
	for _, sub := range command.Commands() {
		if sub.Name() == "prune" {
			setDiscoveryTerms(sub, "cache prune prune cache clean cache free disk space reclaim space no space left on device disk full go build cache go module cache pnpm store scratch tmp")
		}
	}
	return command
}

func dependencyCachePlan(cmd *cobra.Command) bool {
	if persistentCommandID(cmd) != "disk dependency-cache" {
		return false
	}
	apply, _ := cmd.Flags().GetBool("apply")
	return !apply
}
