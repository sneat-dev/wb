package main

import (
	"strings"

	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/spf13/cobra"
)

type daemonResult = daemonruntime.Result
type daemonHubStatus = daemonruntime.HubStatus
type daemonRecoveryResult = daemonruntime.RecoveryResult

// daemonCommandForTest selects a fresh child of the actual production family binding.
func daemonCommandForTest(path string, inv *invocation, deps daemonDependencies) *cobra.Command {
	root := newDaemonCmdWithDependencies(inv, deps)
	command, _, err := root.Find(strings.Fields(path))
	if err != nil {
		panic(err)
	}
	command.Parent().RemoveCommand(command)
	return command
}
