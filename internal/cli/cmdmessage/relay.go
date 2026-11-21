package cmdmessage

import (
	"github.com/sneat-dev/wb/internal/agentmessage"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/spf13/cobra"
)

func newRelay(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var config sessionmove.SSHConfig
	var agent string
	var once bool
	command := &cobra.Command{Use: "relay", Short: "Hold an outbound SSH receive and copy messages into this machine's inbox", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := config.Validate(); err != nil {
			return runtime.ExitError(shared.ExitUsage, err.Error())
		}
		if err := agentmessage.ValidateAgent(agent); err != nil {
			return runtime.ExitError(shared.ExitUsage, err.Error())
		}
		store, err := deps.Store(runtime.Flags().ProjectsRoot)
		if err != nil {
			return err
		}
		return deps.Relay(cmd.Context(), config, store, agent, once, func(err error) {
			notice := err.Error()
			if len(notice) > 512 {
				notice = notice[:512]
			}
			cmd.PrintErrf("message relay: retrying in 2s after %q\n", notice)
		})
	}}
	f := command.Flags()
	f.StringVar(&config.Host, "host", "", "Reachable SSH mailbox host (this machine connects outbound)")
	f.StringVar(&config.User, "user", "", "SSH user (defaults to SSH configuration)")
	f.StringVar(&config.WBPath, "remote-wb", "", "Absolute remote wb executable path")
	f.StringVar(&agent, "agent", "", "Recipient inbox name on both machines")
	f.BoolVar(&once, "once", false, "Receive and copy one batch, then exit (default reconnects until cancelled)")
	return command
}
