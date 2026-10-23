package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdtask"
	"github.com/sneat-dev/wb/internal/taskrun"
	"github.com/spf13/cobra"
)

func newTaskCmd(inv *invocation) *cobra.Command {
	service := taskrun.New(taskrun.DefaultDependencies(newSessionMoveService(inv).Move))
	return cmdtask.New(newCLIRuntime(inv), cmdtask.Operations{Offload: service.Offload, Pickup: service.Pickup})
}
