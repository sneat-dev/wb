package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

type sessionParkOutput struct {
	ParkedSessionID string                 `json:"parked_session_id"`
	Status          string                 `json:"status"`
	Source          session.Record         `json:"source"`
	Worktrees       []sessionpark.Worktree `json:"worktrees"`
	Successor       *session.Record        `json:"successor,omitempty"`
}

var captureParkedSessionWorktree = worktrees.CaptureParkedSessionWorktree
var captureParkedSessionAggregate = worktrees.CaptureParkedSessionAggregate
var defaultParkedSessionRemoteDelivery = func(context.Context, sessionpark.State, string) error {
	return fmt.Errorf("parked-session remote delivery is not implemented")
}

func newSessionParkCmd() *cobra.Command {
	var contextFile, format string
	command := &cobra.Command{
		Use:   "park",
		Short: "Suspend this registered session with an auditable whole-session checkpoint",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			dir, err := sessionDirForRead()
			if err != nil {
				return err
			}
			source, ok := session.ResolveForProcess(dir, os.Getpid())
			if !ok {
				return fmt.Errorf("session park requires a live registered source session")
			}
			body, err := readParkContext(command, contextFile)
			if err != nil {
				return err
			}
			continuation := strings.TrimSpace(string(body))
			if continuation == "" {
				return fmt.Errorf("park requires non-empty continuation context via --context-file")
			}
			if len([]byte(continuation)) > sessionpark.MaxContinuationBytes {
				return fmt.Errorf("park continuation exceeds %d bytes", sessionpark.MaxContinuationBytes)
			}
			results, err := worktrees.List(command.Context(), worktrees.ListOptions{ProjectsRoot: projectsRoot, Workers: 1})
			if err != nil {
				return err
			}
			ownedResults := make([]worktrees.ListResult, 0, len(results))
			for _, result := range results {
				if ownedBySession(result, source) {
					ownedResults = append(ownedResults, result)
				}
			}
			home, err := wbhome.Root(projectsRoot)
			if err != nil {
				return err
			}
			store := sessionpark.NewStore(filepath.Join(home, "parked-sessions"))
			var id string
			var owned []sessionpark.Worktree
			err = captureParkedSessionAggregate(command.Context(), projectsRoot, ownedResults, source, func(captured []sessionpark.Worktree) error {
				owned = captured
				bundle, found, findErr := store.FindBySource(source.WBSessionID)
				if findErr != nil {
					return findErr
				}
				id = bundle.ParkedSessionID
				if !found {
					id, findErr = sessionpark.NewID()
					if findErr != nil {
						return findErr
					}
					bundle = sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: id, Source: source, Continuation: continuation, Worktrees: captured, ParkedAt: time.Now().UTC()}
					if _, createErr := store.Create(bundle); createErr != nil {
						return createErr
					}
				}
				_, markErr := session.MarkParked(dir, source.PID, id)
				return markErr
			})
			if err != nil {
				return err
			}
			out := sessionParkOutput{ParkedSessionID: id, Status: string(sessionpark.StatusParked), Source: source, Worktrees: owned}
			if format == "json" {
				enc := json.NewEncoder(command.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "parked session %s with %d owned worktrees; resume with wb session resume %s\n", id, len(owned), id)
			return err
		},
	}
	command.Flags().StringVar(&contextFile, "context-file", "", "bounded agent-authored continuation file, or - for stdin")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newSessionResumeCmd() *cobra.Command {
	return newSessionResumeCmdWithRemoteDelivery(defaultParkedSessionRemoteDelivery)
}

type parkedSessionRemoteDelivery func(context.Context, sessionpark.State, string) error

func newSessionResumeCmdWithRemoteDelivery(deliver parkedSessionRemoteDelivery) *cobra.Command {
	var target, via, configPath, format string
	command := &cobra.Command{
		Use: "resume <parked-session-id>", Short: "Resume a parked session as one fresh successor session", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			home, err := wbhome.Root(projectsRoot)
			if err != nil {
				return err
			}
			store := sessionpark.NewStore(filepath.Join(home, "parked-sessions"))
			state, err := store.Load(args[0])
			if err != nil {
				return err
			}
			if target != "" {
				if via != "" && via != "ssh" {
					return fmt.Errorf("unsupported resume courier %q; use ssh", via)
				}
				if strings.TrimSpace(configPath) == "" {
					configPath = wbconfig.DefaultPath()
				}
				config, err := sessionmove.LoadConfig(configPath)
				if err != nil {
					return fmt.Errorf("load resume courier config: %w", err)
				}
				if _, ok := config.Target(target); !ok {
					return fmt.Errorf("resume target %q is not configured in %s", target, configPath)
				}
				if err := validateParkedRemoteBundle(state.Bundle, target); err != nil {
					return err
				}
				if deliver == nil {
					return fmt.Errorf("cross-machine resume delivery boundary is unavailable; no worktree was transferred")
				}
				return fmt.Errorf("cross-machine resume to %s via ssh is gated: the parked session contains %d worktrees and the current session receive boundary accepts one worktree; no worktree was transferred", target, len(state.Bundle.Worktrees))
			}
			return fmt.Errorf("local session resume is fail-closed: coordinator launch is not wired; no session, parked aggregate, or custody mutation was performed")
		},
	}
	command.Flags().StringVar(&target, "to", "", "target WB machine for cross-machine resume")
	command.Flags().StringVar(&via, "via", "", "resume courier (ssh)")
	command.Flags().StringVar(&configPath, "config", "", "path to wb.yaml (reserved for cross-machine courier configuration)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func validateParkedRemoteBundle(bundle sessionpark.Bundle, target string) error {
	for _, wt := range bundle.Worktrees {
		if wt.Dirty || wt.Head == "" || wt.RemoteHead == "" || wt.Head != wt.RemoteHead {
			return fmt.Errorf("cannot resume parked session %s to %s: worktree %s is not remotely reconstructable at exact pushed commit (head=%s remote=%s dirty=%t); clean, push, and park again", bundle.ParkedSessionID, target, wt.WorktreeDir, wt.Head, wt.RemoteHead, wt.Dirty)
		}
	}
	return nil
}

func ownedBySession(result worktrees.ListResult, source session.Record) bool {
	for _, owner := range result.Owners {
		if owner.PID == source.PID && !owner.At.Before(source.StartedAt) {
			return true
		}
	}
	return false
}

func captureOwnedParkedWorktrees(ctx context.Context, projectsRoot string, results []worktrees.ListResult, source session.Record) ([]sessionpark.Worktree, error) {
	owned := make([]worktrees.ListResult, 0, len(results))
	for _, result := range results {
		if ownedBySession(result, source) {
			owned = append(owned, result)
		}
	}
	sort.SliceStable(owned, func(i, j int) bool {
		if owned[i].WorktreeDir != owned[j].WorktreeDir {
			return owned[i].WorktreeDir < owned[j].WorktreeDir
		}
		return owned[i].Repository < owned[j].Repository
	})
	captured := make([]sessionpark.Worktree, 0, len(owned))
	for _, result := range owned {
		member, err := captureParkedSessionWorktree(ctx, projectsRoot, result, source)
		if err != nil {
			return nil, fmt.Errorf("capture parked worktree %s: %w", result.WorktreeDir, err)
		}
		captured = append(captured, member)
	}
	return captured, nil
}

func readParkContext(command *cobra.Command, path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("park requires --context-file; use - to read stdin")
	}
	if path == "-" {
		return readBounded(command.InOrStdin(), sessionpark.MaxContinuationBytes, "park context")
	}
	return readBoundedRegularFile(path, sessionpark.MaxContinuationBytes)
}
