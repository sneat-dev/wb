package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

type daemonOperationResult struct {
	OperationID           string `json:"operation_id"`
	IdempotencyKey        string `json:"idempotency_key,omitempty"`
	State                 string `json:"state"`
	Cursor                string `json:"cursor"`
	CommandKind           string `json:"command_kind"`
	ArgsSHA256            string `json:"args_sha256"`
	ArgumentCount         uint32 `json:"argument_count"`
	CPUUnits              uint32 `json:"cpu_units"`
	ExitCode              int32  `json:"exit_code,omitempty"`
	SubmittedUnixMilli    int64  `json:"submitted_unix_milli"`
	StartedUnixMilli      int64  `json:"started_unix_milli,omitempty"`
	FinishedUnixMilli     int64  `json:"finished_unix_milli,omitempty"`
	QueueWaitMilliseconds int64  `json:"queue_wait_milliseconds,omitempty"`
	WallMilliseconds      int64  `json:"wall_milliseconds,omitempty"`
	Error                 string `json:"error,omitempty"`
	TargetWorkerID        string `json:"target_worker_id,omitempty"`
	StdoutTail            string `json:"stdout_tail,omitempty"`
	StderrTail            string `json:"stderr_tail,omitempty"`
}

func newDaemonOperationCmd(deps daemonDependencies) *cobra.Command {
	command := &cobra.Command{Use: "operation", Short: "Submit and inspect authenticated local daemon operations"}
	command.AddCommand(
		newDaemonOperationSubmitCmd(deps),
		newDaemonOperationGetCmd(deps),
		newDaemonOperationWaitCmd(deps),
		newDaemonOperationCancelCmd(deps),
	)
	return command
}

func newDaemonOperationSubmitCmd(deps daemonDependencies) *cobra.Command {
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
			if err := requireDaemonRawExecutionPolicy(deps, projectsRoot); err != nil {
				return err
			}
			client, err := daemonOperationClient(command.Context(), deps, projectsRoot, command.ErrOrStderr())
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

func newDaemonOperationGetCmd(deps daemonDependencies) *cobra.Command {
	var format string
	var jsonOut bool
	command := &cobra.Command{Use: "get <operation-id>", Short: "Get one durable operation receipt", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			selected, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			client, err := daemonOperationClient(command.Context(), deps, projectsRoot, command.ErrOrStderr())
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

func newDaemonOperationWaitCmd(deps daemonDependencies) *cobra.Command {
	var format, afterCursor string
	var timeout time.Duration
	var jsonOut bool
	command := &cobra.Command{Use: "wait <operation-id>", Short: "Wait for a durable operation to reach a terminal state", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			selected, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			ctx := command.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			client, err := daemonOperationClient(ctx, deps, projectsRoot, command.ErrOrStderr())
			if err != nil {
				return err
			}
			operation, err := waitForDaemonOperation(ctx, command.ErrOrStderr(), client, &daemonv1.Operation{OperationId: args[0], Cursor: afterCursor})
			if err != nil {
				return err
			}
			return writeDaemonOperation(command.OutOrStdout(), selected, operation)
		}}
	command.Flags().StringVar(&afterCursor, "after-cursor", "", "wait for a receipt newer than this opaque cursor")
	command.Flags().DurationVar(&timeout, "timeout", 0, "total wait limit (default: no limit)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func newDaemonOperationCancelCmd(deps daemonDependencies) *cobra.Command {
	var format string
	var jsonOut bool
	command := &cobra.Command{Use: "cancel <operation-id>", Short: "Cancel a queued or running local operation", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			selected, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			client, err := daemonOperationClient(command.Context(), deps, projectsRoot, command.ErrOrStderr())
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
	return strings.ToLower(strings.TrimPrefix(state.String(), "OPERATION_STATE_"))
}

func writeDaemonOperation(out io.Writer, format string, operation *daemonv1.Operation) error {
	result := daemonOperationResult{
		OperationID: operation.OperationId, IdempotencyKey: operation.IdempotencyKey,
		State: daemonOperationState(operation.State), Cursor: operation.Cursor,
		CommandKind: operation.CommandKind, ArgsSHA256: operation.ArgsSha256,
		ArgumentCount: operation.ArgumentCount, CPUUnits: operation.CpuUnits,
		ExitCode: operation.ExitCode, SubmittedUnixMilli: operation.SubmittedUnixMilli,
		StartedUnixMilli: operation.StartedUnixMilli, FinishedUnixMilli: operation.FinishedUnixMilli,
		QueueWaitMilliseconds: operation.QueueWaitMilliseconds, WallMilliseconds: operation.WallMilliseconds,
		Error: operation.Error, StdoutTail: string(operation.StdoutTail), StderrTail: string(operation.StderrTail),
		TargetWorkerID: operation.TargetWorkerId,
	}
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

func submitWorkerOperation(command *cobra.Command, deps daemonDependencies, targetWorkerID, idempotencyKey string, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	client, err := daemonOperationClient(command.Context(), deps, projectsRoot, command.ErrOrStderr())
	if err != nil {
		return err
	}
	response, err := client.SubmitOperation(command.Context(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: cwd, Argv: args, TargetWorkerId: targetWorkerID, IdempotencyKey: idempotencyKey,
	}))
	if err != nil {
		return err
	}
	return writeDaemonOperation(command.OutOrStdout(), "json", response.Msg)
}

func requireDaemonRawExecutionPolicy(deps daemonDependencies, root string) error {
	check := deps.rawPolicy
	if check == nil {
		check = defaultDaemonDependencies().rawPolicy
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
