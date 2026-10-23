package main

import (
	"context"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cli/cmdagent"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/spf13/cobra"
	"os"
)

func newAgentCmd(inv *invocation) *cobra.Command {
	return cmdagent.New(newCLIRuntime(inv), agentDependencies())
}
func newAgentAwaitCmd(inv *invocation) *cobra.Command {
	return cmdagent.NewAwait(newCLIRuntime(inv), agentDependencies())
}
func agentDependencies() cmdagent.Dependencies {
	return cmdagent.Dependencies{Operations: newAgentService().Operations(), ResolveRemote: func(machine string) (agents.RemoteTarget, error) {
		return agents.ResolveRemoteTarget(wbconfig.DefaultPath(), machine)
	}, CallRemote: func(ctx context.Context, target agents.RemoteTarget, request agents.RemoteRequest) (agents.RemoteResponse, error) {
		return agents.CallRemote(ctx, target, request, agents.DefaultRemoteDeps())
	}, ReadTaskFile: os.ReadFile, Discovery: setDiscoveryTerms}
}
