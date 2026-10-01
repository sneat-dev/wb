package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type collaborationServiceFactory func() (worktreecollab.Service, error)

type collaborationSessionPorts struct {
	root            func(string) (string, error)
	resolveAncestor func(string, int) (session.Record, bool)
	lookupExact     func(string, int) (session.Record, bool, error)
	lookupRecipient func(string, string) (session.Record, bool)
	listSessions    func(string) ([]session.View, error)
	pid             func() int
	resolveCheckout func(context.Context, string, string) (worktreecollab.Checkout, error)
	observeLegacy   func(string, string) (worktreecollab.ObservedOwner, error)
}

func defaultCollaborationSessionPorts() collaborationSessionPorts {
	return collaborationSessionPorts{root: wbhome.Root, resolveAncestor: session.ResolveForProcess,
		lookupExact: session.LookupExact, lookupRecipient: session.LookupByWBSessionID, listSessions: session.List, pid: os.Getpid,
		resolveCheckout: worktrees.ResolveCollaborationCheckout, observeLegacy: worktrees.ObserveCollaborationLegacyOwner}
}

func newCollaborationService(inv *invocation) (worktreecollab.Service, error) {
	return defaultCollaborationSessionPorts().service(inv)
}

func (ports collaborationSessionPorts) service(inv *invocation) (worktreecollab.Service, error) {
	home, err := ports.root(inv.projectsRoot)
	if err != nil {
		return worktreecollab.Service{}, err
	}
	directory := filepath.Join(home, session.DirName)
	uniqueSession := func(id string) (bool, error) {
		views, err := ports.listSessions(directory)
		if err != nil {
			return false, err
		}
		matches := 0
		for _, view := range views {
			if view.WBSessionID == id {
				matches++
			}
		}
		return matches == 1, nil
	}
	return worktreecollab.Service{Store: worktreecollab.NewStore(home), Ports: worktreecollab.ServicePorts{
		Resolve: func(ctx context.Context, idOrPath string) (worktreecollab.Checkout, error) {
			return ports.resolveCheckout(ctx, inv.projectsRoot, idOrPath)
		},
		Caller: func() (string, error) {
			declared, found := ports.resolveAncestor(directory, ports.pid())
			if !found {
				return "", fmt.Errorf("invoke this command from a live registered ancestor session")
			}
			exact, live, err := ports.lookupExact(directory, declared.PID)
			if err != nil || !live || exact.WBSessionID != declared.WBSessionID || exact.Lifecycle != "" {
				return "", fmt.Errorf("registered ancestor session cannot be corroborated: %v", err)
			}
			unique, err := uniqueSession(exact.WBSessionID)
			if err != nil || !unique {
				return "", fmt.Errorf("registered ancestor session ID is ambiguous or unavailable: %v", err)
			}
			return exact.WBSessionID, nil
		},
		Live: func(id string) (bool, error) {
			unique, err := uniqueSession(id)
			if err != nil || !unique {
				return false, err
			}
			record, found := ports.lookupRecipient(directory, id)
			if !found {
				return false, nil
			}
			exact, live, err := ports.lookupExact(directory, record.PID)
			if err != nil {
				return false, err
			}
			return live && exact.WBSessionID == id && exact.Lifecycle == "", nil
		},
		OwnerStatus: func(id string) (string, error) {
			views, err := ports.listSessions(directory)
			if err != nil {
				return "unknown", nil
			}
			status := "unknown"
			matches := 0
			for _, view := range views {
				if view.WBSessionID != id {
					continue
				}
				matches++
				if matches != 1 {
					return "unknown", nil
				}
				exact, live, err := ports.lookupExact(directory, view.PID)
				if err != nil || exact.WBSessionID != id || exact.Lifecycle != "" {
					return "unknown", nil
				}
				if live {
					status = "live"
				} else {
					status = "inactive"
				}
			}
			return status, nil
		},
		ObserveLegacy: func(checkout worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
			return ports.observeLegacy(checkout.Root, directory)
		},
		ObserveLegacyForInspection: func(checkout worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
			return worktrees.ObserveCollaborationLegacyOwnerReadOnly(checkout.Root, directory)
		},
		Now: time.Now, NewMessageID: session.NewID,
	}}, nil
}

func collaborationFactory(inv *invocation) collaborationServiceFactory {
	return func() (worktreecollab.Service, error) { return newCollaborationService(inv) }
}

func addCollaborationCommands(command *cobra.Command, inv *invocation) {
	factory := collaborationFactory(inv)
	for _, child := range []*cobra.Command{newWorktreeJoinCmd(factory), newWorktreeLeaveCmd(factory),
		newWorktreeTakeOwnershipCmd(factory), newWorktreeTransferOwnershipCmd(factory),
		newWorktreeMessageCmd(factory)} {
		child.GroupID = "recover"
		command.AddCommand(child)
	}
}

func newWorktreeJoinCmd(factory collaborationServiceFactory) *cobra.Command {
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

func newWorktreeLeaveCmd(factory collaborationServiceFactory) *cobra.Command {
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

func newWorktreeTakeOwnershipCmd(factory collaborationServiceFactory) *cobra.Command {
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

func newWorktreeTransferOwnershipCmd(factory collaborationServiceFactory) *cobra.Command {
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

func newWorktreeMessageCmd(factory collaborationServiceFactory) *cobra.Command {
	command := &cobra.Command{Use: "message", Short: "Record and consume local participant messages"}
	command.AddCommand(newWorktreeMessageSendCmd(factory), newWorktreeMessageInboxCmd(factory), newWorktreeMessageAckCmd(factory))
	return command
}

func newWorktreeMessageSendCmd(factory collaborationServiceFactory) *cobra.Command {
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

func newWorktreeMessageInboxCmd(factory collaborationServiceFactory) *cobra.Command {
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

func newWorktreeMessageAckCmd(factory collaborationServiceFactory) *cobra.Command {
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

func formatCollaborationInfo(view worktreecollab.View) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Current owner: %s (%s)\n", view.Owner, view.OwnerStatus)
	fmt.Fprintf(&b, "Worktree ID: %s\n", view.Checkout.ID)
	for _, participant := range view.Joined {
		fmt.Fprintf(&b, "Joined session: %s (live=%t)\n", participant.SessionID, participant.Live)
	}
	return b.String()
}
