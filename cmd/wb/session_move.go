package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const maxSessionHandoverBytes = 1 << 20

type sessionMoveDependencies struct {
	defaultConfigPath func() string
	loadConfig        func(string) (sessionmove.Config, error)
	resolveSource     func() (session.Record, bool, error)
	checkpoint        func(context.Context, worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error)
}

func defaultSessionMoveDependencies() sessionMoveDependencies {
	return sessionMoveDependencies{
		defaultConfigPath: wbconfig.DefaultPath,
		loadConfig:        sessionmove.LoadConfig,
		resolveSource: func() (session.Record, bool, error) {
			directory, err := sessionDirForRead()
			if err != nil {
				return session.Record{}, false, err
			}
			record, ok := session.ResolveForProcess(directory, os.Getpid())
			return record, ok, nil
		},
		checkpoint: worktrees.CreateSessionCheckpoint,
	}
}

type sessionMoveOutput struct {
	Phase        string              `json:"phase"`
	Courier      sessionmove.Courier `json:"courier"`
	SourceActive bool                `json:"source_active"`
	Request      sessionmove.Request `json:"request"`
	Digest       sessionmove.Digest  `json:"request_digest"`
}

func newSessionMoveCmd() *cobra.Command {
	return newSessionMoveCmdWithDeps(defaultSessionMoveDependencies())
}

func newSessionMoveCmdWithDeps(deps sessionMoveDependencies) *cobra.Command {
	var targetMachine, via, configPath, handoverFile, harness string
	var summary, validation, remaining, format string
	command := &cobra.Command{
		Use:   "move [worktree-path]",
		Short: "Create an exact pushed handover checkpoint for this registered session",
		Long: `Create the source-owned checkpoint for a portable session move.

WB requires a live registered session, an active managed Work Log, a clean
named branch, and a non-empty handover supplied from a file or stdin. It
generates and commits only .wb/handoffs/<id>.md, performs a normal non-force
push, verifies that exact commit as the remote branch tip, and records an
offer without transferring source custody. Courier delivery and successor
startup happen only in the later receipt-gated stages.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			targetMachine = strings.TrimSpace(targetMachine)
			if targetMachine == "" {
				return fmt.Errorf("--to is required")
			}
			if strings.TrimSpace(configPath) == "" {
				configPath = deps.defaultConfigPath()
			}
			config, err := deps.loadConfig(configPath)
			if err != nil {
				return err
			}
			target, ok := config.Target(targetMachine)
			if !ok {
				return fmt.Errorf("session move target %q is not configured in %s", targetMachine, configPath)
			}
			courier, err := selectSessionMoveCourier(target, via)
			if err != nil {
				return err
			}
			source, ok, err := deps.resolveSource()
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("session move requires a live registered source session that owns this process; run wb session register at session start")
			}
			body, err := readSessionHandover(command, handoverFile)
			if err != nil {
				return err
			}
			result, err := deps.checkpoint(command.Context(), worktrees.SessionCheckpointOptions{
				ProjectsRoot:     projectsRoot,
				Worktree:         argumentOrCurrent(args),
				SourceSession:    source,
				TargetMachine:    targetMachine,
				RequestedHarness: harness,
				Handover: worktrees.SessionHandover{
					Summary: summary, ValidationEvidence: validation, RemainingWork: remaining, Body: body,
				},
			})
			if err != nil {
				return err
			}
			output := sessionMoveOutput{
				Phase: "offered", Courier: courier, SourceActive: true,
				Request: result.Request, Digest: result.Digest,
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(output)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(),
				"offered handoff %s to %s via %s at exact remote commit %s; source session %s remains active pending a successor receipt\n",
				result.Request.HandoffID, result.Request.TargetMachine, courier, result.Request.BundleCommit,
				result.Request.PredecessorWBSessionID,
			)
			return err
		},
	}
	command.Flags().StringVar(&targetMachine, "to", "", "configured target WB machine (required)")
	command.Flags().StringVar(&via, "via", "", "configured courier: ssh or synchestra (default: target default_courier)")
	command.Flags().StringVar(&configPath, "config", "", "path to wb.yaml (default: ~/.config/wb/wb.yaml)")
	command.Flags().StringVar(&handoverFile, "handover-file", "", "agent-authored handover file, or - for stdin (required)")
	command.Flags().StringVar(&summary, "summary", "", "handover summary recorded in the tracked document and Work Log")
	command.Flags().StringVar(&validation, "validation", "", "validation evidence recorded in the tracked document")
	command.Flags().StringVar(&remaining, "remaining", "", "remaining work and next action recorded in the tracked document")
	command.Flags().StringVar(&harness, "harness", "", "requested successor harness (default: source runtime)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func selectSessionMoveCourier(target sessionmove.TargetConfig, requested string) (sessionmove.Courier, error) {
	courier := target.DefaultCourier
	if requested = strings.TrimSpace(requested); requested != "" {
		courier = sessionmove.Courier(requested)
	}
	switch courier {
	case sessionmove.CourierSSH:
		if target.SSH == nil {
			return "", fmt.Errorf("target %q has no ssh courier configured", target.Machine)
		}
	case sessionmove.CourierSynchestra:
		if target.Synchestra == nil {
			return "", fmt.Errorf("target %q has no synchestra courier configured", target.Machine)
		}
	default:
		return "", fmt.Errorf("--via must be %q or %q", sessionmove.CourierSSH, sessionmove.CourierSynchestra)
	}
	return courier, nil
}

func readSessionHandover(command *cobra.Command, path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("--handover-file is required; use - to read stdin")
	}
	var reader io.Reader
	var file *os.File
	if path == "-" {
		reader = command.InOrStdin()
	} else {
		var err error
		file, err = os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open handover file %s: %w", path, err)
		}
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil {
			return nil, fmt.Errorf("inspect handover file %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("handover file %s must be a regular file", path)
		}
		reader = file
	}
	contents, err := io.ReadAll(io.LimitReader(reader, maxSessionHandoverBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read handover: %w", err)
	}
	if len(contents) > maxSessionHandoverBytes {
		return nil, fmt.Errorf("handover exceeds %d bytes", maxSessionHandoverBytes)
	}
	if len(bytes.TrimSpace(contents)) == 0 {
		return nil, fmt.Errorf("handover must not be empty")
	}
	return contents, nil
}
