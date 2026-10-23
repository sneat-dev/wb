package cmdworktree

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"github.com/spf13/cobra"
	"io"
	"os"
	"path/filepath"
)

// CollaborationOperations is the custody and messaging authority used by commands.
// The existing worktreecollab.Service implements these operations.
type CollaborationOperations interface {
	Join(context.Context, string) (worktreecollab.State, error)
	Leave(context.Context, string) (worktreecollab.State, error)
	Take(context.Context, string, string, bool, string) (worktreecollab.State, error)
	Transfer(context.Context, string, string) (worktreecollab.State, error)
	Send(context.Context, string, string, []string, string) (worktreecollab.SendReceipt, bool, error)
	Inbox(context.Context, string) (worktreecollab.InboxView, error)
	Ack(context.Context, string, string) (uint64, error)
}

// CollaborationCommands constructs worktree collaboration verbs with lazy flags.
func CollaborationCommands(runtime shared.Runtime, factory func(string) (CollaborationOperations, error)) []*cobra.Command {
	current := func() (CollaborationOperations, error) { return factory(runtime.Flags().ProjectsRoot) }
	commands := []*cobra.Command{newJoin(current), newLeave(current), newTakeOwnership(current), newTransferOwnership(current), newMessage(current)}
	for _, command := range commands {
		command.GroupID = "recover"
	}
	return commands
}
func newJoin(factory func() (CollaborationOperations, error)) *cobra.Command {
	return &cobra.Command{Use: "join <id-or-path>", Short: "Join an explicitly owned worktree as a participant", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := factory()
			if err != nil {
				return err
			}
			state, err := service.Join(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "joined %s (owner %s)\n", state.Checkout.ID, state.Owner)
			return err
		}}
}

func newLeave(factory func() (CollaborationOperations, error)) *cobra.Command {
	return &cobra.Command{Use: "leave <id-or-path>", Short: "Leave a shared worktree after transferring ownership", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := factory()
			if err != nil {
				return err
			}
			state, err := service.Leave(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "left %s\n", state.Checkout.ID)
			return err
		}}
}

func newTakeOwnership(factory func() (CollaborationOperations, error)) *cobra.Command {
	var expected, reason string
	var force bool
	command := &cobra.Command{Use: "take-ownership <id-or-path>", Short: "Take one worktree's ownership with an exact expected owner", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("expected-owner") {
				return fmt.Errorf("--expected-owner is required; inspect the current owner first")
			}
			service, err := factory()
			if err != nil {
				return err
			}
			state, err := service.Take(cmd.Context(), args[0], expected, force, reason)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "owner %s (epoch %d)\n", state.Owner, state.OwnerEpoch)
			return err
		}}
	command.Flags().StringVar(&expected, "expected-owner", "", "exact owner ID observed through worktree info, or none")
	command.Flags().BoolVar(&force, "force", false, "take over a live or unresolved owner after exact comparison")
	command.Flags().StringVar(&reason, "reason", "", "audited reason required with --force")
	return command
}

func newTransferOwnership(factory func() (CollaborationOperations, error)) *cobra.Command {
	var successor string
	command := &cobra.Command{Use: "transfer-ownership <id-or-path>", Short: "Appoint a live joined successor as current owner", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if successor == "" {
				return fmt.Errorf("--to-session is required")
			}
			service, err := factory()
			if err != nil {
				return err
			}
			state, err := service.Transfer(cmd.Context(), args[0], successor)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "owner %s (epoch %d)\n", state.Owner, state.OwnerEpoch)
			return err
		}}
	command.Flags().StringVar(&successor, "to-session", "", "live joined successor WB session ID")
	return command
}

func newMessage(factory func() (CollaborationOperations, error)) *cobra.Command {
	command := &cobra.Command{Use: "message", Short: "Record and consume local participant messages"}
	command.AddCommand(newMessageSend(factory), newMessageInbox(factory), newMessageAck(factory))
	return command
}

func newMessageSend(factory func() (CollaborationOperations, error)) *cobra.Command {
	var recipient, key, messageFile string
	command := &cobra.Command{Use: "send <id-or-path>", Short: "Record a bounded idempotent peer message", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if recipient == "" || key == "" || messageFile == "" {
				return fmt.Errorf("--to-session, --idempotency-key, and --message-file are required")
			}
			service, err := factory()
			if err != nil {
				return err
			}
			var body []byte
			if messageFile == "-" {
				body, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), worktreecollab.MaxMessageBodyBytes+1))
			} else {
				var file *os.File
				file, err = os.Open(filepath.Clean(messageFile))
				if err == nil {
					defer func() { _ = file.Close() }()
					body, err = io.ReadAll(io.LimitReader(file, worktreecollab.MaxMessageBodyBytes+1))
				}
			}
			if err != nil {
				return err
			}
			if len(body) > worktreecollab.MaxMessageBodyBytes {
				return fmt.Errorf("message exceeds %d bytes", worktreecollab.MaxMessageBodyBytes)
			}
			receipt, replay, err := service.Send(cmd.Context(), args[0], key, []string{recipient}, string(body))
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "message %s digest %s replay=%t\n", receipt.MessageID, receipt.Digest, replay)
			return err
		}}
	command.Flags().StringVar(&recipient, "to-session", "", "joined recipient WB session ID")
	command.Flags().StringVar(&key, "idempotency-key", "", "stable client retry key")
	command.Flags().StringVar(&messageFile, "message-file", "", "message body file, or - for stdin")
	return command
}

func newMessageInbox(factory func() (CollaborationOperations, error)) *cobra.Command {
	return &cobra.Command{Use: "inbox <id-or-path>", Short: "Read unacknowledged peer messages and the latest owner notice", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := factory()
			if err != nil {
				return err
			}
			view, err := service.Inbox(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if len(view.Messages) == 0 && view.Notice == nil {
				return nil
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(view)
		}}
}

func newMessageAck(factory func() (CollaborationOperations, error)) *cobra.Command {
	return &cobra.Command{Use: "ack <id-or-path> <message-id>", Short: "Acknowledge consumption of one inbox message", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := factory()
			if err != nil {
				return err
			}
			cursor, err := service.Ack(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "consumed through %d\n", cursor)
			return err
		}}
}
