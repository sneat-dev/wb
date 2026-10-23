package cmdremote

import (
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/remoterun"
	"github.com/spf13/cobra"
)

const defaultRemoteHubURL = "https://wb-github-app.sneat.dev"

func newEnroll(runtime shared.Runtime, operations Operations) *cobra.Command {
	var machine, hubURL, tokenFile string
	var tokenStdin, restartDaemon, jsonOut bool
	command := &cobra.Command{
		Use:   "enroll",
		Short: "Securely enroll this machine with the Workbench event hub",
		Long: `Reads the one-time machine credential only from explicitly selected stdin,
verifies it against the hub, stores it in a private file, updates the remote
section of wb.yaml without replacing unrelated settings, and restarts a running
WB daemon so event polling begins immediately.

The credential is never accepted in argv and is never printed or recorded in
WB command telemetry. From the dashboard, copy the credential and run:

  pbpaste | wb remote enroll --machine studio-mac --token-stdin`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, err := operations.Enroll(command.Context(), remoterun.EnrollRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Machine: machine, HubURL: hubURL, TokenFile: tokenFile, TokenStdin: tokenStdin, RestartDaemon: restartDaemon, Input: command.InOrStdin()})
			if err != nil {
				return err
			}
			return writeEnroll(command.OutOrStdout(), result, jsonOut)
		},
	}
	command.Flags().StringVar(&machine, "machine", "", "unique name for this machine (required)")
	command.Flags().StringVar(&hubURL, "url", defaultRemoteHubURL, "Workbench event hub HTTPS origin")
	command.Flags().StringVar(&tokenFile, "token-file", "", "private destination for the machine credential (default: managed file beside wb.yaml)")
	command.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the machine credential from stdin")
	command.Flags().BoolVar(&restartDaemon, "restart-daemon", true, "restart WB daemon if it is running")
	shared.AddJSONFormatFlags(command, &jsonOut)
	return command
}
