package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"github.com/spf13/cobra"
)

func newWorktreeActiveCmd(inv *invocation) *cobra.Command {
	remote := defaultRemoteDeps()
	deps := worktreerun.DefaultActiveDependencies(worktreerun.ActiveRemoteDependencies{
		Load: func(projectsRoot string) (remotestate.Config, remotestate.Provider, error) {
			return loadRemote(remote, projectsRoot)
		},
		Login: remote.login, Now: remote.now,
	})
	return cmdworktree.NewActive(newCLIRuntime(inv), deps)
}
