package cmdremote

import (
	"encoding/json"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

func newMachines(runtime shared.Runtime, operations Operations) *cobra.Command {
	var jsonOut bool
	var stale time.Duration
	cmd := &cobra.Command{
		Use:   "machines",
		Short: "List every machine in the remote store with its publish age",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rows, err := operations.Machines(runtime.Flags().ProjectsRoot, stale)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
			}
			writeMachinesTable(cmd.OutOrStdout(), rows)
			return nil
		},
	}
	shared.AddJSONFormatFlags(cmd, &jsonOut)
	cmd.Flags().DurationVar(&stale, "stale", 24*time.Hour, "flag machines whose snapshot is older than this")
	return cmd
}
