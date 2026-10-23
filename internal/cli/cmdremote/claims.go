package cmdremote

import (
	"encoding/json"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

func newClaims(runtime shared.Runtime, operations Operations) *cobra.Command {
	var jsonOut bool
	var stale time.Duration
	cmd := &cobra.Command{
		Use:   "claims",
		Short: "List every claim in the remote store, with staleness",
		Long: `Reads the remote store and lists every task claim: who holds it, when
they claimed it, their heartbeat age, and whether that heartbeat is stale
against --stale. Claims that cannot be decoded are rendered as error rows
and do not change the exit code.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rows, err := operations.Claims(runtime.Flags().ProjectsRoot, stale)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
			}
			writeClaimsTable(cmd.OutOrStdout(), rows)
			return nil
		},
	}
	shared.AddJSONFormatFlags(cmd, &jsonOut)
	cmd.Flags().DurationVar(&stale, "stale", 24*time.Hour, "a claim's holder is stale once their snapshot is older than this")
	return cmd
}
