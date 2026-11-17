package cmdmessage

import (
	"github.com/sneat-dev/wb/internal/agentmessage"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/spf13/cobra"
)

func newForward(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var config sessionmove.SSHConfig
	var from, to string
	var once bool
	command := &cobra.Command{Use: "forward", Short: "Forward queued messages over an operator-configured transport", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := config.Validate(); err != nil {
			return runtime.ExitError(shared.ExitUsage, err.Error())
		}
		for _, agent := range []string{from, to} {
			if err := agentmessage.ValidateAgent(agent); err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
		}
		if from == to {
			return runtime.ExitError(shared.ExitUsage, "forward requires distinct --from and --to agents")
		}
		store, err := deps.Store(runtime.Flags().ProjectsRoot)
		if err != nil {
			return err
		}
		return deps.Forward(cmd.Context(), config, store, from, to, once, func(err error) {
			notice := err.Error()
			if len(notice) > 512 {
				notice = notice[:512]
			}
			cmd.PrintErrf("message forward: retrying in 2s after %q\n", notice)
		})
	}}
	f := command.Flags()
	f.StringVar(&config.Host, "host", "", "Fixed remote mailbox SSH host configured by the operator")
	f.StringVar(&config.User, "user", "", "SSH user (defaults to SSH configuration)")
	f.StringVar(&config.WBPath, "remote-wb", "", "Absolute remote wb executable path")
	f.StringVar(&from, "from", "", "Forward only messages carrying this sender label (not an authenticated identity)")
	f.StringVar(&to, "to", "", "Local queued recipient and remote recipient inbox")
	f.BoolVar(&once, "once", false, "Scan the queue once in bounded pages, then exit (default retries until cancelled)")
	return command
}
