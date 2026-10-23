package cmdcockpit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/cockpitrun"
	"github.com/spf13/cobra"
)

func newExportCmd(runtime shared.Runtime, export func(context.Context, cockpitrun.ExportRequest) cockpitrun.ExportResult) *cobra.Command {
	var format string
	var metricsOnly bool
	command := &cobra.Command{
		Use:   "export",
		Short: "Print this machine's Cockpit export envelope as JSON",
		Long: "Print this machine's export envelope {schema_version, machine, exported_at, fleet, metrics} as JSON, " +
			"read from this machine's running daemon over its loopback listener as the anonymous-local principal " +
			"(this machine's own entries only, whatever other machines the daemon shows), " +
			"within 8 MiB and limited to the metadata an anonymous local reader may see. " +
			"Another machine's daemon runs this over SSH to read this one. " +
			"It never starts a daemon, opens a browser or mints a login code, and writes nothing. " +
			"--metrics-only omits the fleet. When no daemon is running, or one refuses anonymous reads " +
			"(cockpit.anonymous_metadata: false), or its first scan has not finished (the fleet is still partial; " +
			"--metrics-only is not affected), or the export fails otherwise, it prints {schema_version, error} " +
			"with error daemon_not_running, export_refused, warming_up or export_failed and exits 1. " +
			"An entry of this machine that the envelope's rules refuse is left out and counted in the envelope's dropped field. " +
			"On macOS a daemon is found through launchd, so one started by hand in the foreground is reported as not running.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := shared.RequireOutputFormat(format, "json"); err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			result := export(command.Context(), cockpitrun.ExportRequest{Root: runtime.Flags().ProjectsRoot, MetricsOnly: metricsOnly})
			body, drops, failure := result.Body, result.Drops, result.Failure
			if failure != "" {
				if _, writeErr := command.OutOrStdout().Write(append(mustMarshalExportError(failure), '\n')); writeErr != nil {
					return errors.New("wb cockpit export: could not write to stdout")
				}
				return runtime.ExitError(shared.ExitFindings, "wb cockpit export: "+string(failure))
			}
			if _, err := command.OutOrStdout().Write(body); err != nil {
				return errors.New("wb cockpit export: could not write to stdout")
			}
			if drops.Total() > 0 {
				// Numbers only: what was left out is never named, here or anywhere.
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb cockpit export: left out %d entries the envelope's rules refuse (repositories %d, worktrees %d, pull requests %d, agents %d)\n",
					drops.Total(), drops.Repositories, drops.Worktrees, drops.PullRequests, drops.Agents)
			}
			return nil
		},
	}
	command.Flags().StringVar(&format, "format", "json", "stdout format: json")
	command.Flags().BoolVar(&metricsOnly, "metrics-only", false, "omit the fleet and print only the machine's metrics")
	return command
}

// mustMarshalExportError is the stdout form of a failure; the type cannot fail
// to marshal.
func mustMarshalExportError(failure cockpitrun.ExportFailure) []byte {
	body, _ := json.Marshal(cockpitfleet.NewExportError(string(failure)))
	return body
}
