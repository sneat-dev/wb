// Package cmdtask binds task arguments and presentation to task workflows.
package cmdtask

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/cli/sessionview"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/continuationinput"
	"github.com/sneat-dev/wb/internal/secretscan"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/sneat-dev/wb/internal/taskrun"
	"github.com/spf13/cobra"
)

type Operations struct {
	Offload func(context.Context, taskrun.Request, func([]secretscan.Finding), func(sessionrun.MoveResult) error) (taskrun.Result, error)
	Pickup  func(context.Context, taskrun.PickupRequest, func([]secretscan.Finding), func(sessionrun.MoveResult) error) (taskrun.Result, error)
}

func New(runtime shared.Runtime, operations Operations) *cobra.Command {
	command := &cobra.Command{Use: "task", Short: "Park, pick up, or offload a portion of work as its own successor session"}
	command.AddCommand(NewOffload(runtime, operations.Offload, false), NewOffload(runtime, operations.Offload, true), NewPickup(runtime, operations.Pickup))
	return command
}
func NewOffload(runtime shared.Runtime, offload func(context.Context, taskrun.Request, func([]secretscan.Finding), func(sessionrun.MoveResult) error) (taskrun.Result, error), parkOnly bool) *cobra.Command {
	var contextFile, harness, model, target, format string
	use := "offload <task> [owner/repository...]"
	short := "Create an isolated worktree and start a successor session for a portion of work"
	if parkOnly {
		use = "park <task> [owner/repository...]"
		short = "Create an isolated worktree and store continuation without starting a successor"
	}
	command := &cobra.Command{Use: use, Short: short, Args: cobra.MinimumNArgs(1), RunE: func(command *cobra.Command, args []string) error {
		if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
			return err
		}
		body, err := readContext(command.InOrStdin(), contextFile, command.ErrOrStderr())
		if err != nil {
			return err
		}
		warnings := func(findings []secretscan.Finding) { sessionview.Advisories(command.ErrOrStderr(), findings) }
		render := func(result sessionrun.MoveResult) error {
			return sessionview.Move(command.OutOrStdout(), format, result)
		}
		output, err := offload(command.Context(), taskrun.Request{ProjectsRoot: runtime.Flags().ProjectsRoot, Task: args[0], Repositories: args[1:], Continuation: body, ContextFile: contextFile, Harness: harness, Model: model, Target: target, ParkOnly: parkOnly}, warnings, render)
		if err != nil {
			return err
		}
		return writeOutput(command.OutOrStdout(), format, parkOnly, output)
	}}
	command.Flags().StringVar(&contextFile, "context-file", "", "self-contained successor brief (required; - for stdin)")
	command.Flags().StringVar(&harness, "harness", "", "successor harness: claude or codex")
	command.Flags().StringVar(&model, "model", "", "successor model, e.g. opus or sol")
	command.Flags().StringVar(&target, "to", "", "target WB machine (default: this machine)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	_ = command.MarkFlagRequired("context-file")
	return command
}
func NewPickup(runtime shared.Runtime, pickup func(context.Context, taskrun.PickupRequest, func([]secretscan.Finding), func(sessionrun.MoveResult) error) (taskrun.Result, error)) *cobra.Command {
	var harness, model, target, format string
	command := &cobra.Command{Use: "pickup <parked-task-id>", Short: "Start a successor for a previously parked task", Args: cobra.ExactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
			return err
		}
		warnings := func(findings []secretscan.Finding) { sessionview.Advisories(command.ErrOrStderr(), findings) }
		render := func(result sessionrun.MoveResult) error {
			return sessionview.Move(command.OutOrStdout(), format, result)
		}
		output, err := pickup(command.Context(), taskrun.PickupRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, TaskID: args[0], Harness: harness, Model: model, Target: target}, warnings, render)
		if err != nil {
			return err
		}
		return writeOutput(command.OutOrStdout(), format, false, output)
	}}
	command.Flags().StringVar(&harness, "harness", "", "successor harness override")
	command.Flags().StringVar(&model, "model", "", "successor model override")
	command.Flags().StringVar(&target, "to", "", "target WB machine override")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}
func readContext(input io.Reader, path string, diagnostics io.Writer) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		sessionview.ParkChecklist(diagnostics, "park requires an agent-authored continuation. WB derives observable state itself; record what it cannot observe:")
		return nil, fmt.Errorf("park requires --context-file; use - to read stdin")
	}
	if path == "-" {
		return continuationinput.ReadBounded(input, sessionpark.MaxContinuationBytes, "park context")
	}
	return continuationinput.ReadRegularFile(path, sessionpark.MaxContinuationBytes)
}
func writeOutput(out io.Writer, format string, parked bool, result taskrun.Result) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	verb := "offloaded"
	if parked {
		verb = "parked"
	}
	_, err := fmt.Fprintf(out, "%s task %s as %s in %s\n", verb, result.Task, result.TaskID, result.WorktreeDir)
	return err
}
