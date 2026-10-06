package cmdremote

import (
	"encoding/json"
	"fmt"
	"time"

	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/remoterun"
	"github.com/spf13/cobra"
)

func newStatus(runtime shared.Runtime, operations Operations) *cobra.Command {
	var jsonOut bool
	var stale time.Duration
	var machine string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Cross-machine worklist: every machine's attention repositories and worktrees",
		Long: `Reads the remote store and renders one section per machine. The local
machine is shown as last published, not re-scanned: wb status stays the live
local view. Entries that cannot be decoded are rendered as error rows and do
not change the exit code.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			live := cliprogress.NewLiveWithHeartbeat(cliprogress.Output(cmd.ErrOrStderr(), false), true, operations.Heartbeat)
			result, err := operations.Status(remoterun.StatusRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Machine: machine, Stale: stale}, remoterun.StatusProgress{Start: live.Start, Finish: live.Finish})
			if err != nil {
				return err
			}
			if result.MissingMachine {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "no machine %s in the remote store\n", machine)
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Report)
			}
			if machine == "" || len(result.Report.Entries) > 0 {
				writeRemoteStatusDiagnostics(cmd.OutOrStdout(), result.Report.Diagnostics)
			}
			writeStatusWorklist(cmd.OutOrStdout(), result.Report.Entries, result.Report.Machines, result.AllClaims)
			return nil
		},
	}
	shared.AddJSONFormatFlags(cmd, &jsonOut)
	cmd.Flags().DurationVar(&stale, "stale", 24*time.Hour, "flag machines whose snapshot is older than this")
	cmd.Flags().StringVar(&machine, "machine", "", "only this <login>/<machine>")
	return cmd
}
