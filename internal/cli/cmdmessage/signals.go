package cmdmessage

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// withSignals makes supervised message commands release their waits and stop
// their SSH children when interrupted, without replacing root pre-run policy.
func withSignals(command *cobra.Command) *cobra.Command {
	run := command.RunE
	command.RunE = func(cmd *cobra.Command, args []string) error {
		parent := cmd.Context()
		ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
		defer stop()
		defer cmd.SetContext(parent)
		cmd.SetContext(ctx)
		return run(cmd, args)
	}
	return command
}
