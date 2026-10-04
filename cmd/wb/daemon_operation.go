package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sneat-dev/wb/internal/daemonruntime"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/sneat-dev/wb/internal/operationreceipt"
)

type daemonOperationResult = operationreceipt.Receipt

func newDaemonOperationCmd(inv *invocation, deps daemonDependencies) *cobra.Command {
	command := &cobra.Command{Use: "operation", Short: "Submit and inspect authenticated local daemon operations"}
	command.AddCommand(
		newDaemonOperationSubmitCmd(inv, deps),
		newDaemonOperationGetCmd(inv, deps),
		newDaemonOperationWaitCmd(inv, deps),
		newDaemonOperationCancelCmd(inv, deps),
	)
	return command
}

func newDaemonOperationSubmitCmd(inv *invocation, deps daemonDependencies) *cobra.Command {
	var idempotencyKey, format string
	var cpuUnits uint32
	var wait, jsonOut bool
	command := &cobra.Command{
		Use: "submit -- <command> [args...]", Short: "Submit a trusted raw command for the daemon process to execute",
		Args: func(command *cobra.Command, args []string) error {
			if command.ArgsLenAtDash() != 0 || len(args) == 0 {
				return usageError("command is required after --")
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			selected, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := requireDaemonRawExecutionPolicy(deps, inv.projectsRoot); err != nil {
				return err
			}
			client, err := daemonruntime.OperationClient(command.Context(), deps.Dependencies, inv.projectsRoot, command.ErrOrStderr())
			if err != nil {
				return err
			}
			response, err := client.SubmitOperation(command.Context(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
				IdempotencyKey: idempotencyKey, WorkingDirectory: cwd, Argv: args,
				CpuUnits: cpuUnits, LocalRawCommand: true,
			}))
			if err != nil {
				return err
			}
			operation := response.Msg
			if wait {
				operation, err = waitForDaemonOperation(command.Context(), command.ErrOrStderr(), client, operation)
				if err != nil {
					return err
				}
			}
			return writeDaemonOperation(command.OutOrStdout(), selected, operation)
		},
	}
	command.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "deduplicate retries with this caller-owned key")
	command.Flags().Uint32Var(&cpuUnits, "cpu-units", 0, "CPU units to reserve (default: classify the command)")
	command.Flags().BoolVar(&wait, "wait", false, "wait for a terminal receipt")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func newDaemonOperationGetCmd(inv *invocation, deps daemonDependencies) *cobra.Command {
	var format string
	var jsonOut bool
	command := &cobra.Command{Use: "get <operation-id>", Short: "Get one durable operation receipt", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			selected, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			client, err := daemonruntime.OperationClient(command.Context(), deps.Dependencies, inv.projectsRoot, command.ErrOrStderr())
			if err != nil {
				return err
			}
			response, err := client.GetOperation(command.Context(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: args[0]}))
			if err != nil {
				return err
			}
			return writeDaemonOperation(command.OutOrStdout(), selected, response.Msg)
		}}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func newDaemonOperationWaitCmd(inv *invocation, deps daemonDependencies) *cobra.Command {
	var format, afterCursor string
	var timeout time.Duration
	var jsonOut bool
	var showProgress bool
	var progressFile string
	command := &cobra.Command{Use: "wait <operation-id>", Short: "Wait for a durable operation to reach a terminal state", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			selected, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			progress, closeProgress, err := daemonOperationProgressWriter(command.ErrOrStderr(), showProgress, progressFile)
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
			client, err := daemonruntime.OperationClient(ctx, deps.Dependencies, inv.projectsRoot, progress)
			if err != nil {
				return err
			}
			operation, err := waitForDaemonOperation(ctx, progress, client, &daemonv1.Operation{OperationId: args[0], Cursor: afterCursor})
			if err != nil {
				return err
			}
			return writeDaemonOperation(command.OutOrStdout(), selected, operation)
		}}
	command.Flags().BoolVar(&showProgress, "progress", true, "emit human progress while waiting; disable for terminal-only agent results")
	command.Flags().StringVar(&progressFile, "progress-file", "", "append human progress to a file instead of stderr; agent streams contain only the terminal result")
	command.Flags().StringVar(&afterCursor, "after-cursor", "", "wait for a receipt newer than this opaque cursor")
	command.Flags().DurationVar(&timeout, "timeout", 0, "total wait limit (default: no limit)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func newDaemonOperationCancelCmd(inv *invocation, deps daemonDependencies) *cobra.Command {
	var format string
	var jsonOut bool
	command := &cobra.Command{Use: "cancel <operation-id>", Short: "Cancel a queued or running local operation", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			selected, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			client, err := daemonruntime.OperationClient(command.Context(), deps.Dependencies, inv.projectsRoot, command.ErrOrStderr())
			if err != nil {
				return err
			}
			response, err := client.CancelOperation(command.Context(), connect.NewRequest(&daemonv1.CancelOperationRequest{OperationId: args[0]}))
			if err != nil {
				return err
			}
			return writeDaemonOperation(command.OutOrStdout(), selected, response.Msg)
		}}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

// daemonOperationProgressWriter separates human liveness from agent results.
// Opening an explicit destination must succeed; silently falling back to stderr
// would wake a harness that requested a terminal-only result stream.
func daemonOperationProgressWriter(stderr io.Writer, enabled bool, path string) (io.Writer, func(), error) {
	if !enabled {
		if path != "" {
			return nil, nil, usageError("--progress-file cannot be combined with --progress=false")
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

func waitForDaemonOperation(ctx context.Context, progress io.Writer, client daemonv1connect.DaemonServiceClient, operation *daemonv1.Operation) (*daemonv1.Operation, error) {
	for !daemonOperationTerminal(operation.State) {
		response, err := client.WaitOperation(ctx, connect.NewRequest(&daemonv1.WaitOperationRequest{
			OperationId: operation.OperationId, AfterCursor: operation.Cursor, WaitMilliseconds: 10_000,
		}))
		if err != nil {
			return nil, err
		}
		operation = response.Msg
		if !daemonOperationTerminal(operation.State) {
			_, _ = fmt.Fprintf(progress, "wb: operation %s still %s\n", operation.OperationId, daemonOperationState(operation.State))
		}
	}
	return operation, nil
}

func daemonOperationTerminal(state daemonv1.OperationState) bool {
	return state == daemonv1.OperationState_OPERATION_STATE_SUCCEEDED ||
		state == daemonv1.OperationState_OPERATION_STATE_FAILED ||
		state == daemonv1.OperationState_OPERATION_STATE_CANCELLED ||
		state == daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED
}

func daemonOperationState(state daemonv1.OperationState) string {
	return operationreceipt.StateName(state)
}

func writeDaemonOperation(out io.Writer, format string, operation *daemonv1.Operation) error {
	result := operationreceipt.FromOperation(operation)
	if format == "json" {
		return writeJSONTo(out, result)
	}
	if _, err := fmt.Fprintf(out, "operation %s: state=%s, cursor=%s, cpu_units=%d", result.OperationID, result.State, result.Cursor, result.CPUUnits); err != nil {
		return err
	}
	if result.TargetWorkerID != "" {
		if _, err := fmt.Fprintf(out, ", target_worker=%s", result.TargetWorkerID); err != nil {
			return err
		}
	}
	if result.FinishedUnixMilli != 0 {
		if _, err := fmt.Fprintf(out, ", exit_code=%d", result.ExitCode); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	if result.StdoutTail != "" {
		if _, err := io.WriteString(out, result.StdoutTail); err != nil {
			return err
		}
	}
	if result.StderrTail != "" {
		_, err := fmt.Fprintf(out, "\nstderr:\n%s", result.StderrTail)
		return err
	}
	return nil
}

func requireDaemonRawExecutionPolicy(deps daemonDependencies, root string) error {
	check := deps.RawPolicy
	if check == nil {
		check = defaultDaemonDependencies().RawPolicy
	}
	allowed, path, err := check(root)
	if err != nil {
		return fmt.Errorf("load daemon raw-execution policy: %w", err)
	}
	if allowed {
		return nil
	}
	return fmt.Errorf("raw daemon execution is disabled; an administrator must create %s with mode 0600 and contents {\"version\":1,\"allow_raw_daemon_execution\":true}", path)
}
