package cmddaemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sneat-dev/wb/internal/cli/daemonview"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/daemonoperation"
	"github.com/spf13/cobra"
)

func newOperation(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "operation", Short: "Submit and inspect authenticated local daemon operations"}
	command.AddCommand(
		newSubmit(runtime, deps),
		newGet(runtime, deps),
		NewOperationWait(runtime, deps),
		newCancel(runtime, deps),
	)
	return command
}

func newSubmit(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var idempotencyKey, format string
	var cpuUnits uint32
	var wait, jsonOut bool
	command := &cobra.Command{
		Use: "submit -- <command> [args...]", Short: "Submit a trusted raw command for the daemon process to execute",
		Args: func(command *cobra.Command, args []string) error {
			if command.ArgsLenAtDash() != 0 || len(args) == 0 {
				return runtime.ExitError(shared.ExitUsage, "command is required after --")
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			selected, err := shared.SelectJSONFormat(format, jsonOut)
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			cwd, err := deps.Getwd()
			if err != nil {
				return err
			}
			operation, err := deps.Submit(command.Context(), daemonoperation.SubmitRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Cwd: cwd, IdempotencyKey: idempotencyKey, Argv: args, CPUUnits: cpuUnits, Wait: wait}, command.ErrOrStderr())
			if err != nil {
				return err
			}
			return daemonview.Operation(command.OutOrStdout(), selected, operation)
		},
	}
	command.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "deduplicate retries with this caller-owned key")
	command.Flags().Uint32Var(&cpuUnits, "cpu-units", 0, "CPU units to reserve (default: classify the command)")
	command.Flags().BoolVar(&wait, "wait", false, "wait for a terminal receipt")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func newGet(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var format string
	var jsonOut bool
	command := &cobra.Command{Use: "get <operation-id>", Short: "Get one durable operation receipt", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			selected, err := shared.SelectJSONFormat(format, jsonOut)
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			operation, err := deps.Get(command.Context(), runtime.Flags().ProjectsRoot, args[0], command.ErrOrStderr())
			if err != nil {
				return err
			}
			return daemonview.Operation(command.OutOrStdout(), selected, operation)
		}}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func NewOperationWait(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var format, afterCursor string
	var timeout time.Duration
	var jsonOut bool
	var showProgress bool
	var progressFile string
	command := &cobra.Command{Use: "wait <operation-id>", Short: "Wait for a durable operation to reach a terminal state", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			selected, err := shared.SelectJSONFormat(format, jsonOut)
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			progress, closeProgress, err := progressWriter(runtime, command.ErrOrStderr(), showProgress, progressFile)
			if err != nil {
				return err
			}
			defer closeProgress()
			ctx := command.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			operation, err := deps.Wait(ctx, runtime.Flags().ProjectsRoot, args[0], afterCursor, progress)
			if err != nil {
				return err
			}
			return daemonview.Operation(command.OutOrStdout(), selected, operation)
		}}
	command.Flags().BoolVar(&showProgress, "progress", true, "emit human progress while waiting; disable for terminal-only agent results")
	command.Flags().StringVar(&progressFile, "progress-file", "", "append human progress to a file instead of stderr; agent streams contain only the terminal result")
	command.Flags().StringVar(&afterCursor, "after-cursor", "", "wait for a receipt newer than this opaque cursor")
	command.Flags().DurationVar(&timeout, "timeout", 0, "total wait limit (default: no limit)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func newCancel(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var format string
	var jsonOut bool
	command := &cobra.Command{Use: "cancel <operation-id>", Short: "Cancel a queued or running local operation", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			selected, err := shared.SelectJSONFormat(format, jsonOut)
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			operation, err := deps.Cancel(command.Context(), runtime.Flags().ProjectsRoot, args[0], command.ErrOrStderr())
			if err != nil {
				return err
			}
			return daemonview.Operation(command.OutOrStdout(), selected, operation)
		}}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

// daemonOperationProgressWriter separates human liveness from agent results.
// Opening an explicit destination must succeed; silently falling back to stderr
// would wake a harness that requested a terminal-only result stream.
func progressWriter(runtime shared.Runtime, stderr io.Writer, enabled bool, path string) (io.Writer, func(), error) {
	if !enabled {
		if path != "" {
			return nil, nil, runtime.ExitError(shared.ExitUsage, "--progress-file cannot be combined with --progress=false")
		}
		return io.Discard, func() {}, nil
	}
	if path == "" {
		return stderr, func() {}, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open human progress file: %w", err)
	}
	return file, func() { _ = file.Close() }, nil
}
