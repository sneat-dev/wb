package main

import (
	"context"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/agentmessage"
	"github.com/sneat-dev/wb/internal/cli/cmdmessage"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

func newMessageCmd(inv *invocation) *cobra.Command {
	command := cmdmessage.New(newCLIRuntime(inv), cmdmessage.Dependencies{
		Forward: func(ctx context.Context, config sessionmove.SSHConfig, store agentmessage.Store, from, to string, once bool, report func(error)) error {
			return (agentmessage.SSH{Config: config, ReportRetry: report}).Forward(ctx, store, from, to, once)
		},
		Store: func(root string) (agentmessage.Store, error) {
			home, err := wbhome.Root(root)
			return agentmessage.Store{Root: filepath.Join(home, "messages"), Notify: newMessageNotifier(inv)}, err
		},
		Relay: func(ctx context.Context, config sessionmove.SSHConfig, store agentmessage.Store, agent string, once bool, report func(error)) error {
			return (agentmessage.SSH{Config: config, ReportRetry: report}).Relay(ctx, store, agent, once)
		},
		Send: func(ctx context.Context, config sessionmove.SSHConfig, message agentmessage.Message) (agentmessage.Receipt, error) {
			return (agentmessage.SSH{Config: config}).Send(ctx, message)
		},
	})
	command.AddCommand(newMessageBindCmd(inv), newMessageUnbindCmd(inv))
	return command
}
