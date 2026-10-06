package cmdsession

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/continuationinput"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/spf13/cobra"
	"strings"
	"unicode/utf8"
)

const maxSessionMessageWireBytes = sessionmove.MaxMessageBodyBytes + (16 << 10)

func NewSend(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var message, messageFile, resume, format string
	command := &cobra.Command{
		Use:   "send <wb-session-id>",
		Short: "Durably send exact typed input to a recorded successor session",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			resume = strings.TrimSpace(resume)
			if resume != "" && (command.Flags().Changed("message") || command.Flags().Changed("message-file")) {
				return fmt.Errorf("--resume does not accept replacement message input")
			}
			var body string
			if resume == "" {
				messageChanged, fileChanged := command.Flags().Changed("message"), command.Flags().Changed("message-file")
				if messageChanged == fileChanged {
					return fmt.Errorf("fresh session send requires exactly one of --message or --message-file")
				}
				var err error
				body, err = readSessionMessageBody(command, message, messageFile, messageChanged)
				if err != nil {
					return err
				}
			}
			return runSessionMessage(runtime, command, deps, args[0], sessionmove.MessageKindText, body, resume, format, "send")
		},
	}
	command.Flags().StringVar(&message, "message", "", "exact message text (bounded to 64 KiB)")
	command.Flags().StringVar(&messageFile, "message-file", "", "read exact message text from a regular file, or - for stdin")
	command.Flags().StringVar(&resume, "resume", "", "retry the exact durable bytes for an existing message ID")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}
func NewRecall(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var resume, format string
	command := &cobra.Command{
		Use:     "recall <wb-session-id>",
		Aliases: []string{"request-handoff"},
		Short:   "Ask a recorded successor to return control to this predecessor",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runSessionMessage(runtime, command, deps, args[0], sessionmove.MessageKindRequestHandoff, "",
				strings.TrimSpace(resume), format, "recall")
		},
	}
	command.Flags().StringVar(&resume, "resume", "", "retry the exact durable handoff request for an existing message ID")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}
func runSessionMessage(runtime shared.Runtime, command *cobra.Command, deps Dependencies, target string, kind sessionmove.MessageKind,
	body, resume, format, retryVerb string) error {
	if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
		return err
	}
	result, err := deps.Send(command.Context(), sessionrun.MessageRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Target: target, Kind: kind, Body: body, ResumeID: resume, RetryVerb: retryVerb})
	if err != nil {
		return err
	}

	if format == "json" {
		encoder := json.NewEncoder(command.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	_, err = fmt.Fprintf(command.OutOrStdout(),
		"message %s acknowledged for successor %s as durably recorded and pasted to tmux %s; this does not assert agent processing\n",
		result.Message.MessageID, target, result.Receipt.TmuxName)
	return err
}
func NewReceiveMessage(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var format string
	command := &cobra.Command{
		Use:   "receive-message",
		Short: "Receive exact typed message bytes for a recorded local successor",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			raw, err := continuationinput.ReadBounded(command.InOrStdin(), maxSessionMessageWireBytes, "session message")
			if err != nil {
				return err
			}
			result, err := deps.ReceiveMessage(command.Context(), sessionrun.ReceiveRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Raw: raw})
			if err != nil {
				return err
			}
			if format == "json" {
				receipt, err := sessionmove.EncodeMessageReceipt(result.Receipt)
				if err != nil {
					return err
				}
				_, err = command.OutOrStdout().Write(receipt)
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(),
				"message %s durably recorded and pasted to tmux %s; this does not assert agent processing\n",
				result.Receipt.MessageID, result.Receipt.TmuxName)
			return err
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}
func readSessionMessageBody(command *cobra.Command, message, messageFile string, direct bool) (string, error) {
	var raw []byte
	var err error
	if direct {
		raw = []byte(message)
	} else if messageFile == "-" {
		raw, err = continuationinput.ReadBounded(command.InOrStdin(), sessionmove.MaxMessageBodyBytes, "session message body")
	} else {
		raw, err = continuationinput.ReadRegularFile(messageFile, sessionmove.MaxMessageBodyBytes)
	}
	if err != nil {
		return "", err
	}
	if len(raw) > sessionmove.MaxMessageBodyBytes {
		return "", fmt.Errorf("session message body exceeds %d bytes", sessionmove.MaxMessageBodyBytes)
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("session message body must be valid UTF-8")
	}
	return string(raw), nil
}
