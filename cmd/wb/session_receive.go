package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/wbhome"
)

const maxSessionReceiveBytes = 1 << 20

type sessionReceiveDependencies struct {
	localMachine func() (string, error)
	store        func(string) (sessionmove.Store, error)
	receive      func(context.Context, sessionreceive.Options) (sessionreceive.Result, error)
}

func defaultSessionReceiveDependencies() sessionReceiveDependencies {
	return sessionReceiveDependencies{
		localMachine: func() (string, error) {
			config, err := remotestate.LoadConfig(wbconfig.DefaultPath())
			if err != nil {
				return "", err
			}
			return config.Machine, nil
		},
		store: func(projectsRoot string) (sessionmove.Store, error) {
			home, err := wbhome.Root(projectsRoot)
			if err != nil {
				return sessionmove.Store{}, err
			}
			return sessionmove.NewStore(filepath.Join(home, sessionmove.DirName)), nil
		},
		receive: sessionreceive.Receive,
	}
}

func newSessionReceiveCmd() *cobra.Command {
	return newSessionReceiveCmdWithDeps(defaultSessionReceiveDependencies())
}

func newSessionReceiveCmdWithDeps(deps sessionReceiveDependencies) *cobra.Command {
	var format string
	command := &cobra.Command{
		Use:   "receive",
		Short: "Receive exact session-move bytes into a pinned target worktree",
		Long: `Receive one portable session handoff from exact stdin bytes.

The receiver authenticates its target against the validated remote.machine in
the local wb.yaml, admits the exact request bytes idempotently, fetches the
declared branch directly, and creates or verifies one clean worktree pinned to
the exact bundle commit. It records target phases but does not start a harness,
write a receipt, or transfer predecessor custody.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			raw, err := readSessionReceiveRequest(command)
			if err != nil {
				return err
			}
			machine, err := deps.localMachine()
			if err != nil {
				return fmt.Errorf("load validated local remote.machine for session receive: %w", err)
			}
			store, err := deps.store(projectsRoot)
			if err != nil {
				return err
			}
			result, err := deps.receive(command.Context(), sessionreceive.Options{
				Store: store, ProjectsRoot: projectsRoot, LocalMachine: machine, RawRequest: raw,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(result)
			}
			if result.Receipt != nil {
				_, err = fmt.Fprintf(command.OutOrStdout(), "replayed completed handoff %s receipt for successor %s\n",
					result.Request.HandoffID, result.Receipt.SuccessorWBSessionID)
				return err
			}
			worktree := ""
			if result.Worktree != nil {
				worktree = result.Worktree.WorktreeDir
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "handoff %s phase %s at pinned target worktree %s; no successor started\n",
				result.Request.HandoffID, result.Phase, worktree)
			return err
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func readSessionReceiveRequest(command *cobra.Command) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(command.InOrStdin(), maxSessionReceiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read session receive request: %w", err)
	}
	if len(raw) > maxSessionReceiveBytes {
		return nil, fmt.Errorf("session receive request exceeds %d bytes", maxSessionReceiveBytes)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("session receive request must not be empty")
	}
	return raw, nil
}
