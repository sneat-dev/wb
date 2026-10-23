package cmdremote

import (
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/remoterun"
	"github.com/spf13/cobra"
)

func newClaim(runtime shared.Runtime, operations Operations) *cobra.Command {
	var note string
	var takeOver, force, jsonOut bool
	var stale time.Duration
	cmd := &cobra.Command{
		Use:   "claim <task>",
		Short: "Claim a task in the remote store, or refresh your own claim",
		Long: `Claims <task> for this login/machine. Refreshing your own claim (same
login and machine) always succeeds. Taking over someone else's claim needs
either --take-over, which only replaces a claim whose holder's heartbeat has
gone stale (see --stale), or --force, which replaces any claim, stale or
fresh, loudly.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := operations.Claim(remoterun.ClaimRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Task: args[0], Note: note, TakeOver: takeOver, Force: force, JSON: jsonOut, Stale: stale})
			if err != nil {
				return err
			}
			return writeClaimOutcome(cmd.OutOrStdout(), jsonOut, result.Outcome, result.Text)
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "free-form note stored with the claim")
	cmd.Flags().BoolVar(&takeOver, "take-over", false, "replace another holder's claim, but only if it is stale")
	cmd.Flags().BoolVar(&force, "force", false, "replace any claim, stale or fresh")
	shared.AddJSONFormatFlags(cmd, &jsonOut)
	cmd.Flags().DurationVar(&stale, "stale", 24*time.Hour, "a claim's holder is stale once their snapshot is older than this")
	return cmd
}
