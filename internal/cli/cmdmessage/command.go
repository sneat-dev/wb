// Package cmdmessage exposes the bidirectional remote-agent inbox CLI.
package cmdmessage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sneat-dev/wb/internal/agentmessage"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/spf13/cobra"
)

type Dependencies struct {
	Relay func(context.Context, sessionmove.SSHConfig, agentmessage.Store, string, bool, func(error)) error
	Store func(string) (agentmessage.Store, error)
	Send  func(context.Context, sessionmove.SSHConfig, agentmessage.Message) (agentmessage.Receipt, error)
}

func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "message", Short: "Send, receive and acknowledge messages between remote agents"}
	command.AddCommand(newSend(runtime, deps), newDeliver(runtime, deps), newReceive(runtime, deps), newAck(runtime, deps), newRelay(runtime, deps))
	for _, child := range command.Commands() {
		withSignals(child)
	}
	return command
}

func outputReceipt(cmd *cobra.Command, receipt agentmessage.Receipt, err error) error {
	if receipt.ID != "" {
		if writeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(receipt); writeErr != nil {
			return writeErr
		}
	}
	return err
}

func newSend(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var host, user, remoteWB, from, to, body, path, id string
	var local bool
	command := &cobra.Command{Use: "send", Short: "Send a durable message over authenticated SSH (JSON receipt)", Args: cobra.NoArgs}
	command.RunE = func(cmd *cobra.Command, _ []string) error {
		if (host == "") == !local || from == "" || to == "" || cmd.Flags().Changed("message") == cmd.Flags().Changed("message-file") {
			return runtime.ExitError(shared.ExitUsage, "send requires exactly one of --host or --local, --from, --to and exactly one of --message or --message-file")
		}
		if path != "" {
			reader := cmd.InOrStdin()
			if path != "-" {
				file, err := os.Open(path)
				if err != nil {
					return err
				}
				defer func() { _ = file.Close() }()
				reader = file
			}
			raw, err := io.ReadAll(io.LimitReader(reader, agentmessage.MaxBodyBytes+1))
			if err != nil {
				return err
			}
			body = string(raw)
		}
		if id == "" {
			id = agentmessage.NewID()
		}
		message := agentmessage.Message{Version: 1, ID: id, From: from, To: to, Body: body}
		if err := message.Validate(); err != nil {
			return runtime.ExitError(shared.ExitUsage, err.Error())
		}
		config := sessionmove.SSHConfig{Host: host, User: user, WBPath: remoteWB}
		if local && (user != "" || remoteWB != "") {
			return runtime.ExitError(shared.ExitUsage, "--user and --remote-wb require --host")
		}
		if !local && cmd.Flags().Changed("projects-root") {
			return runtime.ExitError(shared.ExitUsage, "--projects-root applies only to --local sends")
		}
		if !local {
			if err := config.Validate(); err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Message ID: %s (keep this ID and exact content for retries)\n", id)
		if local {
			store, err := deps.Store(runtime.Flags().ProjectsRoot)
			if err != nil {
				return err
			}
			receipt, err := store.Deliver(cmd.Context(), message)
			return outputReceipt(cmd, receipt, err)
		}
		receipt, err := deps.Send(cmd.Context(), config, message)
		return outputReceipt(cmd, receipt, err)
	}
	f := command.Flags()
	f.BoolVar(&local, "local", false, "Deliver into the local mailbox (for an outbound remote relay)")
	f.StringVar(&host, "host", "", "Configured SSH host alias of the receiving machine")
	f.StringVar(&user, "user", "", "SSH user (defaults to SSH configuration)")
	f.StringVar(&remoteWB, "remote-wb", "", "Absolute path to remote wb executable (default: wb on PATH)")
	f.StringVar(&from, "from", "", "Sender agent name (an advisory label, not an authenticated identity)")
	f.StringVar(&to, "to", "", "Recipient agent inbox name")
	f.StringVar(&body, "message", "", "UTF-8 message (prefer --message-file to keep content out of argv)")
	f.StringVar(&path, "message-file", "", "Read UTF-8 message from file, or - for stdin")
	f.StringVar(&id, "id", "", "32-character hex message ID for an idempotent retry")
	return command
}

func newDeliver(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	return &cobra.Command{Use: "deliver", Short: "Receive one bounded message envelope on stdin (SSH endpoint)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		message, err := agentmessage.Decode(cmd.InOrStdin())
		if err != nil {
			return runtime.ExitError(shared.ExitUsage, err.Error())
		}
		store, err := deps.Store(runtime.Flags().ProjectsRoot)
		if err != nil {
			return err
		}
		receipt, err := store.Deliver(cmd.Context(), message)
		return outputReceipt(cmd, receipt, err)
	}}
}

func newReceive(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var agent string
	var wait time.Duration
	command := &cobra.Command{Use: "receive", Short: "Read unacknowledged messages, optionally waiting for immediate delivery", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := agentmessage.ValidateAgent(agent); err != nil {
			return runtime.ExitError(shared.ExitUsage, err.Error())
		}
		if wait < 0 || wait > 24*time.Hour {
			return runtime.ExitError(shared.ExitUsage, "--wait must be between zero and 24h")
		}
		store, err := deps.Store(runtime.Flags().ProjectsRoot)
		if err != nil {
			return err
		}
		messages, err := store.Receive(cmd.Context(), agent, wait)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(messages)
	}}
	command.Flags().StringVar(&agent, "agent", "", "Local recipient agent inbox name")
	command.Flags().DurationVar(&wait, "wait", 0, "Wait for a message, up to 24h; full inbox socket path must fit 103 bytes (run as an asynchronous harness tool)")
	return command
}

func newAck(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var agent string
	command := &cobra.Command{Use: "ack <message-id>", Short: "Acknowledge processing without deleting replay protection", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := agentmessage.ValidateAgent(agent); err != nil {
			return runtime.ExitError(shared.ExitUsage, err.Error())
		}
		if err := agentmessage.ValidateID(args[0]); err != nil {
			return runtime.ExitError(shared.ExitUsage, err.Error())
		}
		store, err := deps.Store(runtime.Flags().ProjectsRoot)
		if err != nil {
			return err
		}
		if err = store.Ack(agent, args[0]); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"id": args[0], "status": "acknowledged"})
	}}
	command.Flags().StringVar(&agent, "agent", "", "Local recipient agent inbox name")
	return command
}
