package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdstream"
	"github.com/sneat-dev/wb/internal/streamrun"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func newStreamCmd(inv *invocation) *cobra.Command {
	service := newStreamService(defaultRemoteDeps())
	return cmdstream.New(newCLIRuntime(inv), cmdstream.Dependencies{Start: service.Start, Join: service.Join, List: service.List, Status: service.Status, End: service.End, Delete: service.Delete, Sync: service.Sync, RegisteredSession: func() bool { _, ok := worktrees.RegisteredIdentity(); return ok }, PrepareWorkLog: worktrees.PrepareWorkLogOptions})
}
func newStreamService(deps remoteDeps) *streamrun.Service {
	return streamrun.New(streamrun.Config{Machine: func(root string) (string, error) {
		config, _, err := loadRemote(deps, root)
		return config.Machine, err
	}, Login: deps.login, Executable: hookExecutable})
}
