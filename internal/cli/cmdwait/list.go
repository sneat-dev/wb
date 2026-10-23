package cmdwait

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/waitrun"
	"github.com/spf13/cobra"
	"strings"
	"time"
)

func NewList(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var jsonOut bool
	var prune bool
	command := &cobra.Command{
		Use:   "list",
		Short: "Show what WB is currently waiting for on this machine",
		Long: `List every outstanding wait recorded on this machine.

This exists so a quiet session can be told apart from a stopped one. An agent
that has correctly delegated its waiting produces no output until the wait ends;
without this, that is indistinguishable from a crash.

A wait whose process is gone is reported as stale rather than hidden, because a
waiter that died is the thing most worth knowing about. Listing never deletes;
pass --prune to remove stale records.`,
		Example: `wb wait list
wb wait list --json`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			result, err := deps.Inspect(waitrun.ListRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Prune: prune})
			if err != nil {
				return err
			}
			if result.Pruned {
				if _, err := fmt.Fprintf(command.OutOrStdout(), "pruned %d stale wait(s)\n", result.Removed); err != nil {
					return err
				}
				return nil
			}
			records := result.Records
			if jsonOut {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(map[string]any{"schema_version": 1, "waits": records})
			}
			if len(records) == 0 {
				_, err := fmt.Fprintln(command.OutOrStdout(), "no outstanding waits")
				return err
			}
			for _, record := range records {
				state := "waiting"
				if record.Stale {
					state = "stale"
				}
				line := fmt.Sprintf("%s %s %s until %s for %s since %s",
					state, record.Kind, strings.Join(record.Targets, " "), record.Until,
					record.WBSessionID, record.StartedAt.Format(time.RFC3339))
				if _, err := fmt.Fprintln(command.OutOrStdout(), line); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&prune, "prune", false, "remove records whose waiting process is gone")
	shared.AddJSONFormatFlags(command, &jsonOut)
	deps.Discovery(command, "wait list outstanding waiting pending stale session visible stopped quiet")
	return command
}
