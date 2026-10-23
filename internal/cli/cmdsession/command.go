package cmdsession

import (
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "session",
		Short: "Record and inspect the agent sessions running on this machine",
		Long: `Record and inspect the agent sessions running on this machine.

WB is a short-lived command with no daemon, so it cannot observe a session
starting. A session announces itself once — from a harness start-up hook, or by
hand — and everything WB writes afterwards can be attributed to it without each
command being told again.

A record is a claim, not an observation: WB stores what it was told, adds only
what it can see for itself (its own version and binary path), and evaluates
liveness from the declared PID when the record is read.`,
	}
	command.AddCommand(NewRegister(runtime, deps))
	command.AddCommand(NewList(runtime, deps))
	command.AddCommand(NewPrune(runtime, deps))
	command.AddCommand(NewMove(runtime, deps))
	command.AddCommand(NewPark(runtime, deps))
	command.AddCommand(NewResume(runtime, deps))
	command.AddCommand(NewReceive(runtime, deps))
	command.AddCommand(NewReceivePark(runtime, deps))
	command.AddCommand(NewSend(runtime, deps))
	command.AddCommand(NewRecall(runtime, deps))
	command.AddCommand(NewReceiveMessage(runtime, deps))
	return command
}
