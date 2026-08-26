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
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkreceive"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/wbhome"
)

type sessionReceiveParkDependencies struct {
	localMachine func() (string, error)
	store        func(string) (sessionpark.TargetStore, error)
	receive      func(context.Context, sessionparkreceive.Options) (sessionparkreceive.Result, error)
}

func defaultSessionReceiveParkDependencies() sessionReceiveParkDependencies {
	return sessionReceiveParkDependencies{
		localMachine: func() (string, error) {
			config, err := remotestate.LoadConfig(wbconfig.DefaultPath())
			if err != nil {
				return "", err
			}
			return config.Machine, nil
		},
		store: func(projectsRoot string) (sessionpark.TargetStore, error) {
			home, err := wbhome.Root(projectsRoot)
			if err != nil {
				return sessionpark.TargetStore{}, err
			}
			return sessionpark.NewTargetStore(filepath.Join(home, sessionpark.TargetDirName)), nil
		},
		receive: sessionparkreceive.Receive,
	}
}

func newSessionReceiveParkCmd() *cobra.Command {
	return newSessionReceiveParkCmdWithDeps(defaultSessionReceiveParkDependencies())
}

func newSessionReceiveParkCmdWithDeps(deps sessionReceiveParkDependencies) *cobra.Command {
	var format string
	command := &cobra.Command{
		Use:   "receive-park",
		Short: "Receive an exact parked-session bundle and return its durable target receipt",
		Long: `Receive one canonical parked-session envelope from bounded stdin.

The receiver authenticates the declared target against the local remote.machine,
admits the exact envelope bytes into a private no-follow aggregate, reconstructs
every exact pushed member, and creates every target Work Log claim before one
successor is released. It returns one durable receipt only after all members are
attached to that successor. Private continuation material is never printed.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			raw, err := readSessionReceiveParkEnvelope(command)
			if err != nil {
				return err
			}
			machine, err := deps.localMachine()
			if err != nil {
				return fmt.Errorf("load validated local remote.machine for parked-session receive: %w", err)
			}
			store, err := deps.store(projectsRoot)
			if err != nil {
				return err
			}
			result, err := deps.receive(command.Context(), sessionparkreceive.Options{
				Store: store, ProjectsRoot: projectsRoot, LocalMachine: machine, RawEnvelope: raw,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(result)
			}
			if result.Receipt == nil {
				return fmt.Errorf("park resume %s ended at phase %s without a durable receipt", result.ResumeID, result.Phase)
			}
			verb := "completed"
			if result.Replay {
				verb = "replayed"
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "%s park resume %s for successor %s with %d exact members; durable target receipt recorded\n",
				verb, result.ResumeID, result.Receipt.SuccessorWBSessionID, len(result.Receipt.Members))
			return err
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func readSessionReceiveParkEnvelope(command *cobra.Command) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(command.InOrStdin(), sessionpark.MaxEnvelopeBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read parked-session receive envelope: %w", err)
	}
	if len(raw) > sessionpark.MaxEnvelopeBytes {
		return nil, fmt.Errorf("park resume envelope exceeds %d bytes", sessionpark.MaxEnvelopeBytes)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("park resume envelope must not be empty")
	}
	return raw, nil
}
