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
	"github.com/sneat-dev/wb/internal/sessionparkcourier"
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
	Receipt         *sessionpark.Receipt   `json:"receipt,omitempty"`
}

// sessionResumeDependencies isolates only the bounded courier construction for
// command-level regression tests. The product journey keeps the real CLI,
// process transport, and target receiver boundary in session_park_journey_test.
type sessionResumeDependencies struct {
	defaultConfigPath func() string
	loadConfig        func(string) (sessionmove.Config, error)
	store             func(string) (sessionpark.Store, error)
	newDeliverer      func(sessionmove.SSHConfig) (sessionparkcourier.Deliverer, error)
	now               func() time.Time
}

func defaultSessionResumeDependencies() sessionResumeDependencies {
	return sessionResumeDependencies{
		defaultConfigPath: wbconfig.DefaultPath,
		loadConfig:        sessionmove.LoadConfig,
		store: func(projectsRoot string) (sessionpark.Store, error) {
			home, err := wbhome.Root(projectsRoot)
			if err != nil {
				return sessionpark.Store{}, err
			}
			return sessionpark.NewStore(filepath.Join(home, "parked-sessions")), nil
		},
		newDeliverer: sessionparkcourier.NewSSHDeliverer,
		now:          func() time.Time { return time.Now().UTC() },
	}
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
			if strings.TrimSpace(contextFile) == "" {
				return fmt.Errorf("park requires a private continuation via --context-file, or --context-file - for stdin")
			}
			body, err := readParkContext(command, contextFile)
			if err != nil {
				return err
			}
			continuation := strings.TrimSpace(string(body))
			if continuation == "" {
				return fmt.Errorf("park continuation must not be empty")
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
				member, err := worktrees.CaptureParkedSessionWorktree(command.Context(), projectsRoot, result, source)
				if err != nil {
					return fmt.Errorf("park worktree %s: %w", result.WorktreeDir, err)
				}
				owned = append(owned, member)
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
	return newSessionResumeCmdWithDeps(defaultSessionResumeDependencies())
}

func newSessionResumeCmdWithDeps(deps sessionResumeDependencies) *cobra.Command {
	var target, via, configPath, format string
	command := &cobra.Command{
		Use: "resume <parked-session-id>", Short: "Resume a parked session as one fresh successor session", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			store, err := deps.store(projectsRoot)
			if err != nil {
				return err
			}
			if target != "" {
				if via != "" && via != "ssh" {
					return fmt.Errorf("unsupported resume courier %q; use ssh", via)
				}
				if strings.TrimSpace(configPath) == "" {
					configPath = deps.defaultConfigPath()
				}
				config, err := deps.loadConfig(configPath)
				if err != nil {
					return fmt.Errorf("load resume courier config: %w", err)
				}
				targetConfig, ok := config.Target(target)
				if !ok {
					return fmt.Errorf("resume target %q is not configured in %s", target, configPath)
				}
				if targetConfig.SSH == nil {
					return fmt.Errorf("resume target %q has no ssh courier configured", target)
				}
				lock, err := store.Acquire(command.Context(), args[0])
				if err != nil {
					return err
				}
				defer func() { _ = lock.Close() }()
				state, err := store.LoadUnderLock(lock)
				if err != nil {
					return err
				}
				if err := validateParkedRemoteBundle(state.Bundle, target); err != nil {
					return err
				}
				admission, err := store.PrepareRemoteUnderLock(lock, target, "", deps.now())
				if err != nil {
					return err
				}
				receipt, err := store.LoadRemoteReceiptUnderLock(lock, admission)
				if err != nil {
					return err
				}
				if receipt == nil {
					deliverer, delivererErr := deps.newDeliverer(*targetConfig.SSH)
					if delivererErr != nil {
						return delivererErr
					}
					delivery, deliveryErr := deliverer.Deliver(command.Context(), admission.Raw)
					if deliveryErr != nil {
						return fmt.Errorf("resume parked session %s to %s: %w", args[0], target, deliveryErr)
					}
					receipt = &delivery.Receipt
					if err := store.SaveRemoteReceiptUnderLock(lock, admission, *receipt); err != nil {
						return err
					}
				}
				state, err = store.FinalizeRemoteUnderLock(lock, admission, deps.now())
				if err != nil {
					return err
				}
				out := sessionParkOutput{ParkedSessionID: args[0], Status: string(state.Status), Source: state.Bundle.Source,
					Worktrees: state.Bundle.Worktrees, Successor: state.Successor, Receipt: receipt}
				if format == "json" {
					encoder := json.NewEncoder(command.OutOrStdout())
					encoder.SetIndent("", "  ")
					return encoder.Encode(out)
				}
				_, err = fmt.Fprintf(command.OutOrStdout(), "resumed parked session %s as successor %s on %s with %d exact target members; durable target receipt and source custody completion recorded\n",
					args[0], receipt.SuccessorWBSessionID, target, len(receipt.Members))
				return err
			}
			state, err := store.Load(args[0])
			if err != nil {
				return err
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
			state, err = store.Resume(args[0], successor, deps.now())
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
			out := sessionParkOutput{ParkedSessionID: args[0], Status: string(state.Status), Source: state.Bundle.Source, Worktrees: state.Bundle.Worktrees, Successor: state.Successor}
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
		if wt.Dirty || wt.Head == "" || wt.RemoteHead == "" || wt.Head != wt.RemoteHead || wt.RepositoryRemote == "" || wt.WorkLogReference == "" {
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
	reader := command.InOrStdin()
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = file.Close() }()
		reader = file
	}
	raw, err := io.ReadAll(io.LimitReader(reader, sessionpark.MaxContinuationBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > sessionpark.MaxContinuationBytes {
		return nil, fmt.Errorf("park continuation exceeds %d bytes", sessionpark.MaxContinuationBytes)
	}
	return raw, nil
}
