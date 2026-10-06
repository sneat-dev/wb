package cmdremote

import (
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/remoterun"
	"github.com/spf13/cobra"
)

func newRelease(runtime shared.Runtime, operations Operations) *cobra.Command {
	var force, jsonOut bool
	cmd := &cobra.Command{
		Use:   "release <task>",
		Short: "Release this machine's remote claim on a task",
		Long: `Releases <task> if this login/machine holds it. Releasing a task with
no claim is a no-op, not an error. --force removes another holder's claim
too.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := operations.Release(remoterun.ReleaseRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Task: args[0], Force: force})
			if err != nil {
				return err
			}
			return writeReleaseOutcome(cmd.OutOrStdout(), jsonOut, result.Outcome, result.Text)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "release the claim even if another login/machine holds it")
	shared.AddJSONFormatFlags(cmd, &jsonOut)
	return cmd
}
