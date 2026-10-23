// Package cmdworker owns worker command arguments and announcements.
package cmdworker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/workerrun"
	"github.com/spf13/cobra"
)

type Dependencies struct {
	CanonicalRoots func([]string) ([]string, error)
	Budget         func() int
	Connect        func(context.Context, workerrun.ConnectRequest, func(workerrun.ConnectResult) error, io.Writer) error
}

func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "worker", Short: "Run sandbox-inherited workers for durable daemon jobs"}
	command.AddCommand(newConnect(runtime, deps))
	return command
}
func newConnect(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var workerID, format string
	var roots []string
	var cpuCapacity uint32
	var jsonOut bool
	command := &cobra.Command{
		Use: "connect", Short: "Connect a sandboxed worker to the local WB scheduler",
		Long: `Connect one long-lived worker from inside the caller or harness sandbox.

The daemon only schedules and journals normal jobs. This process independently
checks every assigned working directory against --root, executes with its own
inherited sandbox and environment, heartbeats every five seconds, and returns a
bounded terminal receipt. It uses the protected local socket first; when the
sandbox denies or cannot reach that socket, it reports the fallback and uses
authenticated atomic envelopes under the projects root. Repeat --root to permit
more canonical roots.`, Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			selected, err := shared.SelectJSONFormat(format, jsonOut)
			if err != nil {
				return runtime.ExitError(2, err.Error())
			}
			id := strings.TrimSpace(workerID)
			if id == "" {
				return runtime.ExitError(2, "--id is required and must stay stable across reconnects")
			}
			permitted, err := deps.CanonicalRoots(roots)
			if err != nil {
				return runtime.ExitError(2, err.Error())
			}
			capacity := cpuCapacity
			if capacity == 0 {
				capacity = uint32(deps.Budget())
			}
			return deps.Connect(command.Context(), workerrun.ConnectRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, WorkerID: id, Roots: permitted, CPUCapacity: capacity}, func(result workerrun.ConnectResult) error { return announce(command.OutOrStdout(), selected, result) }, command.ErrOrStderr())
		}}
	command.Flags().StringVar(&workerID, "id", "", "stable worker ID (required; reuse it after reconnect)")
	command.Flags().StringArrayVar(&roots, "root", nil, "canonical root this worker may execute within (repeatable, required)")
	command.Flags().Uint32Var(&cpuCapacity, "cpu-capacity", 0, "maximum CPU units this worker accepts (default: WB machine budget)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}
func announce(out io.Writer, format string, result workerrun.ConnectResult) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	_, err := fmt.Fprintf(out, "worker %s connected: generation=%s scheduler=%s cpu=%d roots=%d\n", result.WorkerID, result.WorkerGeneration, result.SchedulerGeneration, result.CPUCapacity, len(result.PermittedRoots))
	return err
}
