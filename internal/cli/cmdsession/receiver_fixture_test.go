package cmdsession

import (
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/spf13/cobra"
)

func receiveCommand(projectsRoot string, deps sessionrun.ReceiveDependencies) *cobra.Command {
	return NewReceive(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: projectsRoot} }}, Dependencies{Receive: sessionrun.NewReceive(deps).Receive})
}
func receiveParkCommand(projectsRoot string, deps sessionrun.ReceiveParkDependencies) *cobra.Command {
	return NewReceivePark(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: projectsRoot} }}, Dependencies{ReceivePark: sessionrun.NewReceivePark(deps).ReceivePark})
}
