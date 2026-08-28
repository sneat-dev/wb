package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmessage"
	"github.com/sneat-dev/wb/internal/sessionmessenger"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/wbhome"
)

const maxSessionMessageWireBytes = sessionmove.MaxMessageBodyBytes + (16 << 10)

type sessionMessageDependencies struct {
	resolveSource func() (session.Record, bool, error)
	store         func(string) (sessionmove.Store, error)
	newMessageID  func() (string, error)
	send          func(context.Context, sessionmessenger.Options) (sessionmessenger.Result, error)
}

func defaultSessionMessageDependencies() sessionMessageDependencies {
	return sessionMessageDependencies{
		resolveSource: func() (session.Record, bool, error) {
			directory, err := sessionDirForRead()
			if err != nil {
				return session.Record{}, false, err
			}
			record, ok := session.ResolveForProcess(directory, os.Getpid())
			return record, ok, nil
		},
		store: func(root string) (sessionmove.Store, error) {
			home, err := wbhome.Root(root)
			if err != nil {
				return sessionmove.Store{}, err
			}
			return sessionmove.NewStore(filepath.Join(home, sessionmove.DirName)), nil
		},
		newMessageID: sessionmove.NewMessageID,
		send:         sessionmessenger.Send,
	}
}

func newSessionSendCmd() *cobra.Command {
	return newSessionSendCmdWithDeps(defaultSessionMessageDependencies())
}

func newSessionSendCmdWithDeps(deps sessionMessageDependencies) *cobra.Command {
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
			return runSessionMessage(command, deps, args[0], sessionmove.MessageKindText, body, resume, format, "send")
		},
	}
	command.Flags().StringVar(&message, "message", "", "exact message text (bounded to 64 KiB)")
	command.Flags().StringVar(&messageFile, "message-file", "", "read exact message text from a regular file, or - for stdin")
	command.Flags().StringVar(&resume, "resume", "", "retry the exact durable bytes for an existing message ID")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newSessionRequestHandoffCmd() *cobra.Command {
	return newSessionRequestHandoffCmdWithDeps(defaultSessionMessageDependencies())
}

func newSessionRequestHandoffCmdWithDeps(deps sessionMessageDependencies) *cobra.Command {
	var resume, format string
	command := &cobra.Command{
		Use:   "request-handoff <wb-session-id>",
		Short: "Ask a recorded successor to hand control back to this predecessor",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runSessionMessage(command, deps, args[0], sessionmove.MessageKindRequestHandoff, "",
				strings.TrimSpace(resume), format, "request-handoff")
		},
	}
	command.Flags().StringVar(&resume, "resume", "", "retry the exact durable handoff request for an existing message ID")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func runSessionMessage(command *cobra.Command, deps sessionMessageDependencies, target string, kind sessionmove.MessageKind,
	body, resume, format, retryVerb string,
) error {
	if err := requireOutputFormat(format, "text", "json"); err != nil {
		return err
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("successor WB session ID is required")
	}
	source, ok, err := deps.resolveSource()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("session messaging requires the live registered predecessor session that owns this process")
	}
	store, err := deps.store(projectsRoot)
	if err != nil {
		return err
	}
	messageID := ""
	if resume == "" {
		messageID, err = deps.newMessageID()
		if err != nil {
			return err
		}
	}
	result, err := deps.send(command.Context(), sessionmessenger.Options{
		Store: store, ProjectsRoot: projectsRoot, TargetWBSessionID: target, SourceSession: source,
		Kind: kind, Body: body, MessageID: messageID, ResumeMessageID: resume, Now: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		var deliveryErr *sessionmessenger.DeliveryError
		if errors.As(err, &deliveryErr) && deliveryErr.MessageID != "" {
			return fmt.Errorf("%w; retry the exact durable bytes with: wb session %s %s --resume %s",
				err, retryVerb, target, deliveryErr.MessageID)
		}
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

type sessionReceiveMessageDependencies struct {
	localMachine func() (string, error)
	store        func(string) (sessionmove.Store, error)
	sessionDir   func() (string, error)
	receive      func(context.Context, sessionmessage.Options) (sessionmessage.Result, error)
}

func defaultSessionReceiveMessageDependencies() sessionReceiveMessageDependencies {
	return sessionReceiveMessageDependencies{
		localMachine: func() (string, error) {
			config, err := remotestate.LoadConfig(wbconfig.DefaultPath())
			if err != nil {
				return "", err
			}
			return config.Machine, nil
		},
		store: func(root string) (sessionmove.Store, error) {
			home, err := wbhome.Root(root)
			if err != nil {
				return sessionmove.Store{}, err
			}
			return sessionmove.NewStore(filepath.Join(home, sessionmove.DirName)), nil
		},
		sessionDir: sessionDirForRead,
		receive:    sessionmessage.Receive,
	}
}

func newSessionReceiveMessageCmd() *cobra.Command {
	return newSessionReceiveMessageCmdWithDeps(defaultSessionReceiveMessageDependencies())
}

func newSessionReceiveMessageCmdWithDeps(deps sessionReceiveMessageDependencies) *cobra.Command {
	var format string
	command := &cobra.Command{
		Use:   "receive-message",
		Short: "Receive exact typed message bytes for a recorded local successor",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			raw, err := readBounded(command.InOrStdin(), maxSessionMessageWireBytes, "session message")
			if err != nil {
				return err
			}
			machine, err := deps.localMachine()
			if err != nil {
				return fmt.Errorf("load validated local remote.machine for message receiver: %w", err)
			}
			store, err := deps.store(projectsRoot)
			if err != nil {
				return err
			}
			sessions, err := deps.sessionDir()
			if err != nil {
				return err
			}
			result, err := deps.receive(command.Context(), sessionmessage.Options{
				Store: store, ProjectsRoot: projectsRoot, LocalMachine: machine, SessionDir: sessions, RawMessage: raw,
			})
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
		raw, err = readBounded(command.InOrStdin(), sessionmove.MaxMessageBodyBytes, "session message body")
	} else {
		raw, err = readBoundedRegularFile(messageFile, sessionmove.MaxMessageBodyBytes)
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

func readBounded(reader io.Reader, limit int, label string) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%s must not be empty", label)
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, limit)
	}
	return raw, nil
}

func readBoundedRegularFile(path string, limit int) ([]byte, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil || filepath.Clean(absolute) != absolute || absolute == string(filepath.Separator) {
		return nil, fmt.Errorf("message file must be one clean path")
	}
	// Resolve only the parent so standard aliases such as macOS /var work,
	// then walk that resolved identity with no-follow descriptors. The final
	// user-selected file is never symlink-resolved and remains O_NOFOLLOW.
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return nil, fmt.Errorf("resolve message file parent: %w", err)
	}
	absolute = filepath.Join(resolvedParent, filepath.Base(absolute))
	segments := strings.Split(strings.TrimPrefix(absolute, string(filepath.Separator)), string(filepath.Separator))
	directoryFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open message file root: %w", err)
	}
	for _, segment := range segments[:len(segments)-1] {
		next, openErr := unix.Openat(directoryFD, segment, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(directoryFD)
		if openErr != nil {
			return nil, fmt.Errorf("open message file parent: %w", openErr)
		}
		directoryFD = next
	}
	fd, err := unix.Openat(directoryFD, segments[len(segments)-1], unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	_ = unix.Close(directoryFD)
	if err != nil {
		return nil, fmt.Errorf("open message file: %w", err)
	}
	file := os.NewFile(uintptr(fd), "wb-session-message-input")
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open message file descriptor")
	}
	defer func() { _ = file.Close() }()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 ||
		before.Size <= 0 || before.Size > int64(limit) {
		if err != nil {
			return nil, fmt.Errorf("inspect message file: %w", err)
		}
		return nil, fmt.Errorf("message file must be one non-empty bounded regular single-link file")
	}
	first, err := readBounded(file, limit, "message file")
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind message file: %w", err)
	}
	second, err := readBounded(file, limit, "message file verification")
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(first, second) {
		return nil, fmt.Errorf("message file changed while it was read")
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || before.Dev != after.Dev || before.Ino != after.Ino ||
		before.Mode != after.Mode || before.Nlink != after.Nlink || before.Size != after.Size {
		if err != nil {
			return nil, fmt.Errorf("reinspect message file: %w", err)
		}
		return nil, fmt.Errorf("message file identity changed while it was verified")
	}
	return first, nil
}
