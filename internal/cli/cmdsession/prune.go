package cmdsession

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

func NewPrune(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "prune",
		Short: "Remove records for sessions whose process has exited",
		Long: `Remove records for sessions whose process has exited.

Records are small and a stale one is harmless — it reports as gone rather than
misleading anyone — so pruning is housekeeping, not a correctness requirement.
A live session is never removed.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			removed, err := deps.Prune(command.Context(), runtime.Flags().ProjectsRoot)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(command.OutOrStdout(), "removed %d exited session record(s)\n", removed)
			return nil

		},
	}
	return command
}
