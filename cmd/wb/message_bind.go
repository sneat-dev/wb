package main

import (
	"context"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/agentmessage"
	"github.com/sneat-dev/wb/internal/agentmessagewake"
	"github.com/sneat-dev/wb/internal/cli/cmdmessage"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

func messageWakeService(projectsRoot string) (agentmessagewake.Service, error) {
	root, err := wbhome.Root(projectsRoot)
	return agentmessagewake.Service{Root: filepath.Join(root, "messages", ".wake"), ProjectsRoot: filepath.Dir(root)}, err
}

func newMessageNotifier(inv *invocation) func(context.Context, agentmessage.Message) (string, error) {
	return func(ctx context.Context, message agentmessage.Message) (string, error) {
		service, err := messageWakeService(inv.projectsRoot)
		if err != nil {
			return "preflight-failed", err
		}
		return service.Notify(ctx, message)
	}
}

func newMessageBindingDependencies() cmdmessage.BindingDependencies {
	return cmdmessage.BindingDependencies{
		Bind: func(ctx context.Context, root, agent, socket, thread string) error {
			service, err := messageWakeService(root)
			if err != nil {
				return err
			}
			return service.Bind(ctx, agent, socket, thread)
		},
		Unbind: func(root, agent string) error {
			service, err := messageWakeService(root)
			if err != nil {
				return err
			}
			return service.Unbind(agent)
		},
	}
}

func newMessageBindCmd(inv *invocation) *cobra.Command {
	return cmdmessage.NewBind(newCLIRuntime(inv), newMessageBindingDependencies())
}

func newMessageUnbindCmd(inv *invocation) *cobra.Command {
	return cmdmessage.NewUnbind(newCLIRuntime(inv), newMessageBindingDependencies())
}
