package cmdmessage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sneat-dev/wb/internal/agentmessage"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

// BindingDependencies keeps receiver-local wake configuration outside CLI parsing.
type BindingDependencies struct {
	Bind   func(ctx context.Context, projectsRoot, agent, socket, thread string) error
	Unbind func(projectsRoot, agent string) error
}

// NewBind opts one local inbox into native Codex wake-up after verifying its thread.
func NewBind(runtime shared.Runtime, deps BindingDependencies) *cobra.Command {
	var agent, socket, thread string
	command := newBindingCommand(runtime, "bind", "Bind a local inbox to an existing Codex thread", &agent,
		func(command *cobra.Command) error {
			if socket == "" || thread == "" {
				return runtime.ExitError(shared.ExitUsage, "--codex-socket and --thread are required")
			}
			return deps.Bind(command.Context(), runtime.Flags().ProjectsRoot, agent, socket, thread)
		})
	command.Long = "Opt a local inbox into automatic wake-up of an existing Codex app-server thread.\nThe receiver chooses the local socket and exact thread. Senders cannot select them.\nCodex must already be running; this command does not launch another agent."
	command.Flags().StringVar(&socket, "codex-socket", "", "absolute path to the receiver's Codex app-server control socket")
	command.Flags().StringVar(&thread, "thread", "", "exact existing Codex thread ID")
	return command
}

// NewUnbind disables future automatic wake-up while preserving inbox messages.
func NewUnbind(runtime shared.Runtime, deps BindingDependencies) *cobra.Command {
	var agent string
	return newBindingCommand(runtime, "unbind", "Disable automatic wake-up for a local inbox", &agent,
		func(_ *cobra.Command) error { return deps.Unbind(runtime.Flags().ProjectsRoot, agent) })
}

func newBindingCommand(runtime shared.Runtime, verb, short string, agent *string, action func(*cobra.Command) error) *cobra.Command {
	var format string
	command := &cobra.Command{
		Use:   verb,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := agentmessage.ValidateAgent(*agent); err != nil {
				return runtime.ExitError(shared.ExitUsage, "--agent: "+err.Error())
			}
			if format != "json" && format != "text" {
				return runtime.ExitError(shared.ExitUsage, "--format must be json or text")
			}
			if err := action(command); err != nil {
				return err
			}
			if format == "json" {
				return json.NewEncoder(command.OutOrStdout()).Encode(struct {
					Agent  string `json:"agent"`
					Action string `json:"action"`
				}{*agent, verb})
			}
			_, err := fmt.Fprintf(command.OutOrStdout(), "%s: %s complete\n", *agent, verb)
			return err
		},
	}
	command.Flags().StringVar(agent, "agent", "", "local recipient inbox name")
	command.Flags().StringVar(&format, "format", "json", "output format: json or text")
	return withSignals(command)
}
