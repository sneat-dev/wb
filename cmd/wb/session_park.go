package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	Continuation    string                 `json:"continuation,omitempty"`
	Successor       *session.Record        `json:"successor,omitempty"`
}

func newSessionParkCmd() *cobra.Command {
	var contextFile, summary, validation, remaining, format string
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
			continuation := strings.TrimSpace(strings.Join([]string{summary, validation, remaining, string(body)}, "\n"))
			if continuation == "" {
				return fmt.Errorf("park requires continuation context via --context-file, --summary, --validation, or --remaining")
			}
			if len([]byte(continuation)) > sessionpark.MaxContinuationBytes {
				return fmt.Errorf("park continuation exceeds %d bytes", sessionpark.MaxContinuationBytes)
			}
			results, err := worktrees.List(command.Context(), worktrees.ListOptions{ProjectsRoot: projectsRoot, Workers: 1})
			if err != nil {
				return err
			}
			owned := make([]sessionpark.Worktree, 0)
			for _, result := range results {
				if !ownedBySession(result, source) {
					continue
				}
				owned = append(owned, sessionpark.Worktree{Repository: result.Repository, CanonicalDir: result.CanonicalDir, WorktreeDir: result.WorktreeDir, WorktreesRoot: result.WorktreesRoot, Branch: result.Branch, Head: result.HeadSHA, Dirty: !result.Clean, RemoteHead: result.RemoteHeadSHA, Status: fmt.Sprintf("clean=%t head=%s remote=%s", result.Clean, result.HeadSHA, result.RemoteHeadSHA)})
			}
			home, err := wbhome.Root(projectsRoot)
			if err != nil {
				return err
			}
			store := sessionpark.NewStore(filepath.Join(home, "parked-sessions"))
			bundle, found, err := store.FindBySource(source.WBSessionID)
			if err != nil {
				return err
			}
			id := bundle.ParkedSessionID
			if !found {
				id, err = sessionpark.NewID()
				if err != nil {
					return err
				}
				bundle = sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: id, Source: source, Continuation: continuation, Worktrees: owned, ParkedAt: time.Now().UTC()}
				if _, err := store.Create(bundle); err != nil {
					return err
				}
			}
			if _, err := session.MarkParked(dir, source.PID, id); err != nil {
				return err
			}
			out := sessionParkOutput{ParkedSessionID: id, Status: string(sessionpark.StatusParked), Source: source, Worktrees: owned, Continuation: continuation}
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
	command.Flags().StringVar(&summary, "summary", "", "continuation summary")
	command.Flags().StringVar(&validation, "validation", "", "validation evidence")
	command.Flags().StringVar(&remaining, "remaining", "", "remaining work and next action")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newSessionResumeCmd() *cobra.Command {
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
				return fmt.Errorf("cross-machine resume to %s via ssh is gated: the parked session contains %d worktrees and the current session receive boundary accepts one worktree; no worktree was transferred", target, len(state.Bundle.Worktrees))
			}
			dir, err := sessionDirForRead()
			if err != nil {
				return err
			}
			successor, ok := session.ResolveForProcess(dir, os.Getpid())
			if !ok {
				return fmt.Errorf("session resume requires a later live registered successor session")
			}
			if successor.WBSessionID == state.Bundle.Source.WBSessionID {
				return fmt.Errorf("successor session must be fresh; source session %s is parked", successor.WBSessionID)
			}
			if successor.PredecessorWBSessionID == "" {
				successor.PredecessorWBSessionID = state.Bundle.Source.WBSessionID
			}
			// Persist the successor's predecessor edge before attaching worktrees.
			registered, err := session.Register(dir, successor)
			if err != nil {
				return err
			}
			successor = registered
			state, err = store.Resume(args[0], successor, time.Now().UTC())
			if err != nil {
				return err
			}
			if state.Successor != nil && state.Successor.WBSessionID == successor.WBSessionID {
				identity := worktrees.AgentIdentity{Runtime: successor.Runtime, AgentID: successor.WBSessionID, Model: successor.Model, PID: successor.PID}
				for _, wt := range state.Bundle.Worktrees {
					if err := worktrees.RecordCustody(wt.WorktreeDir, "", identity.Agent(), identity); err != nil {
						return fmt.Errorf("attach resumed worktree %s: %w", wt.WorktreeDir, err)
					}
				}
			}
			out := sessionParkOutput{ParkedSessionID: args[0], Status: string(state.Status), Source: state.Bundle.Source, Worktrees: state.Bundle.Worktrees, Continuation: state.Bundle.Continuation, Successor: state.Successor}
			if format == "json" {
				enc := json.NewEncoder(command.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "resumed parked session %s as successor %s with %d owned worktrees; continuation is available from the parked checkpoint\n", args[0], successor.WBSessionID, len(state.Bundle.Worktrees))
			return err
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

func readParkContext(command *cobra.Command, path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	var reader io.Reader = command.InOrStdin()
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		reader = file
	}
	return io.ReadAll(io.LimitReader(reader, sessionpark.MaxContinuationBytes+1))
}
