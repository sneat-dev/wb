package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/secretscan"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkcourier"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

type sessionParkOutput struct {
	ParkedSessionID string `json:"parked_session_id"`
	Status          string `json:"status"`
	MemberCount     int    `json:"member_count"`
}

var captureParkedSessionAggregate = worktrees.CaptureParkedSessionAggregate

// parkJudgmentCategories are the non-derivable halves of a parked session.
// WB derives every observable fact itself (branch, HEAD, dirty paths, claims,
// owners) and refuses to replay a stale claim as current state, so the only
// thing an agent must supply is judgment WB cannot observe. These categories
// are a prompt, never a schema: they are printed to stderr so an agent notices
// an omission, and are never parsed, required section-by-section, or written
// into the continuation artifact. Structure that gets filled in with "n/a" is
// worse than prose.
var parkJudgmentCategories = []string{
	"why anything was left uncommitted (a correct fix whose proving test is unwritten is not a finished fix)",
	"ordering constraints proven the hard way, and what proved them",
	"what is blocked on a human decision, and the exact question being asked",
	"which lanes were dispatched, against which repos, and what each was told",
	"corrections: claims made earlier this session that were later disproved",
}

// writeParkJudgmentChecklist prints the judgment prompt to stderr. It never
// goes to stdout: stdout carries the parked-session record, and on --format
// json it must stay machine-readable.
func writeParkJudgmentChecklist(command *cobra.Command, lead string) {
	out := command.ErrOrStderr()
	_, _ = fmt.Fprintln(out, lead)
	for _, category := range parkJudgmentCategories {
		_, _ = fmt.Fprintf(out, "  - %s\n", category)
	}
}

func newSessionParkCmd() *cobra.Command {
	var contextFile, format string
	var overrideSecrets []string
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
			overrides, err := secretscan.ParseOverrides(overrideSecrets)
			if err != nil {
				return err
			}
			// The continuation becomes immutable the instant it is stored
			// below (store.Create / the retry-equality check), so the scan
			// must run, and can only refuse, before that point: the write is
			// the damage, not a later commit or push of it.
			secretWarnings, err := scanContinuationForSecrets(overrides, secretscan.Segment{Name: "continuation", Content: []byte(continuation)})
			if err != nil {
				return err
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
				bundle, found, findErr := store.FindBySource(source.WBSessionID)
				if findErr != nil {
					return findErr
				}
				id = bundle.ParkedSessionID
				if found {
					retry := sessionpark.Bundle{
						SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: bundle.ParkedSessionID,
						Source: source, Continuation: continuation, Worktrees: captured, ParkedAt: bundle.ParkedAt,
					}
					if !sessionpark.EqualBundle(bundle, retry) {
						return fmt.Errorf("existing immutable parked session %s conflicts with the current source, continuation, or member evidence; use its original park inputs to repair lifecycle marking", bundle.ParkedSessionID)
					}
					owned = bundle.Worktrees
				} else {
					id, findErr = sessionpark.NewID()
					if findErr != nil {
						return findErr
					}
					bundle = sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: id, Source: source, Continuation: continuation, Worktrees: captured, ParkedAt: time.Now().UTC()}
					if _, createErr := store.Create(bundle); createErr != nil {
						return createErr
					}
					owned = captured
				}
				_, markErr := session.MarkParked(dir, source.PID, id)
				return markErr
			})
			if err != nil {
				return err
			}
			out := sessionParkOutput{ParkedSessionID: id, Status: string(sessionpark.StatusParked), MemberCount: len(owned)}
			printSecretScanAdvisories(command, secretWarnings)
			// The continuation is immutable once parked, so this is a
			// verification prompt rather than an invitation to edit: a
			// re-park with different continuation is refused by design.
			writeParkJudgmentChecklist(command, "parked. Continuation is now immutable; confirm it records:")
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
	command.Flags().StringArrayVar(&overrideSecrets, secretOverrideFlagName, nil, secretOverrideFlagHelp)
	return command
}

func newSessionResumeCmd() *cobra.Command {
	deps := defaultSessionResumeDependencies()
	deps.preflightLocal = func(source session.Record) error { return sessionlaunch.PreflightLocal(source.Runtime) }
	return newSessionResumeCmdWithDependencies(deps)
}

type sessionResumeOutput struct {
	ParkedSessionID      string             `json:"parked_session_id"`
	Status               string             `json:"status"`
	TargetMachine        string             `json:"target_machine"`
	SuccessorWBSessionID string             `json:"successor_wb_session_id"`
	MemberCount          int                `json:"member_count"`
	ReceiptDigest        sessionmove.Digest `json:"receipt_digest,omitempty"`
	Replay               bool               `json:"replay"`
}

type sessionResumeDependencies struct {
	now               func() time.Time
	deliverSSH        func(context.Context, sessionmove.SSHConfig, []byte, sessionparkcourier.Options) (sessionparkcourier.Result, error)
	withRemoteCustody func(context.Context, string, sessionpark.Bundle, func() error) error
	withLocalCustody  func(context.Context, string, sessionpark.Bundle, string, func(*worktrees.ParkedLocalCustody) error) error
	attachLocal       func(context.Context, *worktrees.ParkedLocalCustody, session.Record, string, uint64) error
	startLocal        func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error)
	inspectLocal      func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error)
	inspectPrepared   func(context.Context, sessionlaunch.Options) (string, error)
	afterLocalLaunch  func(sessionlaunch.Result) error
	markResumed       func(string, int, string, string) (session.Record, error)
	preflightLocal    func(session.Record) error
}

func defaultSessionResumeDependencies() sessionResumeDependencies {
	return sessionResumeDependencies{
		now: func() time.Time { return time.Now().UTC() },
		deliverSSH: func(ctx context.Context, config sessionmove.SSHConfig, raw []byte, options sessionparkcourier.Options) (sessionparkcourier.Result, error) {
			deliverer, err := sessionparkcourier.NewSSHDeliverer(config, options)
			if err != nil {
				return sessionparkcourier.Result{}, err
			}
			return deliverer.Deliver(ctx, raw)
		},
		withRemoteCustody: worktrees.WithParkedRemoteResumeCustody,
		withLocalCustody:  worktrees.WithParkedLocalResumeCustodyForAttempt,
		attachLocal: func(ctx context.Context, custody *worktrees.ParkedLocalCustody, successor session.Record, attemptID string, attemptIndex uint64) error {
			return custody.Attach(ctx, successor, attemptID, attemptIndex)
		},
		startLocal:   sessionlaunch.Start,
		inspectLocal: sessionlaunch.Inspect,
		inspectPrepared: func(ctx context.Context, options sessionlaunch.Options) (string, error) {
			evidence, err := sessionlaunch.InspectPrepared(ctx, options)
			if err != nil {
				return "", err
			}
			if options.Authority == nil || !evidence.Authenticates(options.Authority.AggregateID, sessionmove.Digest(options.Authority.AggregateDigest)) {
				return "", fmt.Errorf("prepared local launcher evidence is not authenticated to the exact parked aggregate")
			}
			return evidence.AttemptID, nil
		},
		markResumed: session.MarkResumed,
	}
}

func newSessionResumeCmdWithDependencies(deps sessionResumeDependencies) *cobra.Command {
	var target, via, configPath, format string
	command := &cobra.Command{
		Use:   "resume <parked-session-id>",
		Short: "Resume a parked session as one fresh successor session",
		Long: `Resume a parked session as one fresh successor session.

Use the parked_session_id returned by wb session park or shown for the parked
row by wb session list --format json. wb_session_id identifies the source
agent session and is not a resume argument.

Before a fresh local resume claims a route or changes custody, WB verifies the
fixed tmux, harness, and WB executables. If the released harness exits during
startup, WB retains its exit status and bounded terminal diagnostic for the
exact retryable attempt.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			home, err := wbhome.Root(projectsRoot)
			if err != nil {
				return err
			}
			store := sessionpark.NewStore(filepath.Join(home, sessionpark.SourceDirName))
			lock, err := store.Acquire(command.Context(), args[0])
			if err != nil {
				return err
			}
			defer func() { _ = lock.Close() }()
			state, err := store.LoadUnderLock(lock)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			if deps.now != nil {
				now = deps.now()
			}
			if target != "" {
				output, err := resumeParkedRemote(command.Context(), deps, store, lock, state, target, via, configPath, now, command.ErrOrStderr(), home)
				if err != nil {
					return err
				}
				if deps.markResumed == nil {
					return fmt.Errorf("session resumed lifecycle projection dependency is unavailable")
				}
				if _, err := deps.markResumed(filepath.Join(home, session.DirName), state.Bundle.Source.PID, state.Bundle.ParkedSessionID, output.SuccessorWBSessionID); err != nil {
					return err
				}
				return writeSessionResumeOutput(command, format, output)
			}
			if via != "" || configPath != "" {
				return fmt.Errorf("--via and --config require --to for remote resume")
			}
			output, err := resumeParkedLocal(command.Context(), deps, store, lock, state, now)
			if err != nil {
				return err
			}
			if deps.markResumed == nil {
				return fmt.Errorf("session resumed lifecycle projection dependency is unavailable")
			}
			if _, err := deps.markResumed(filepath.Join(home, session.DirName), state.Bundle.Source.PID, state.Bundle.ParkedSessionID, output.SuccessorWBSessionID); err != nil {
				return err
			}
			return writeSessionResumeOutput(command, format, output)
		},
	}
	command.Flags().StringVar(&target, "to", "", "target WB machine for cross-machine resume")
	command.Flags().StringVar(&via, "via", "", "resume courier (ssh)")
	command.Flags().StringVar(&configPath, "config", "", "path to wb.yaml (reserved for cross-machine courier configuration)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func resumeParkedRemote(ctx context.Context, deps sessionResumeDependencies, store sessionpark.Store, lock *sessionpark.SourceLock, state sessionpark.State, target, via, configPath string, now time.Time, warn io.Writer, home string) (sessionResumeOutput, error) {
	if via != "" && via != string(sessionmove.CourierSSH) {
		return sessionResumeOutput{}, fmt.Errorf("unsupported resume courier %q; use ssh", via)
	}
	if state.Status == sessionpark.StatusResumed {
		if state.ResumeRoute == nil || state.ResumeRoute.Mode != sessionpark.ResumeRouteRemote || state.ResumeRoute.TargetMachine != target ||
			state.RemoteReceipt == nil || state.RemoteReceipt.TargetMachine != target || state.Successor == nil {
			return sessionResumeOutput{}, fmt.Errorf("parked session was already resumed by a different local or remote winner")
		}
		if _, err := parkedRemoteSSHConfig(state.ResumeRoute, target, via, configPath); err != nil {
			return sessionResumeOutput{}, err
		}
		return sessionResumeOutput{ParkedSessionID: state.Bundle.ParkedSessionID, Status: string(state.Status), TargetMachine: target,
			SuccessorWBSessionID: state.Successor.WBSessionID, MemberCount: len(state.Bundle.Worktrees),
			ReceiptDigest: publicReceiptDigest(*state.RemoteReceipt), Replay: true}, nil
	}
	sshConfig, err := parkedRemoteSSHConfig(state.ResumeRoute, target, via, configPath)
	if err != nil {
		return sessionResumeOutput{}, err
	}
	if err := validateParkedRemoteBundle(state.Bundle, target); err != nil {
		return sessionResumeOutput{}, err
	}
	if deps.withRemoteCustody == nil || deps.deliverSSH == nil {
		return sessionResumeOutput{}, fmt.Errorf("remote parked-session resume dependencies are unavailable")
	}
	var final sessionpark.State
	var receipt *sessionpark.Receipt
	replay := false
	err = deps.withRemoteCustody(ctx, projectsRoot, state.Bundle, func() error {
		admission, err := store.PrepareRemoteUnderLock(lock, target, "", string(sessionmove.CourierSSH), sshConfig, now)
		if err != nil {
			return err
		}
		replay = admission.Replay
		receipt, err = store.LoadRemoteReceiptUnderLock(lock, admission)
		if err != nil {
			return err
		}
		if receipt == nil {
			retainedSSH, routeErr := admission.Route.SSHConfig()
			if routeErr != nil {
				return routeErr
			}
			// Remote diagnostics land in the private local journal, under the
			// same posture as the Work Log: never in source Git, public
			// reports, or Synchestra envelopes.
			courierOptions := sessionparkcourier.Options{
				Warn:          warn,
				DiagnosticDir: parkResumeDiagnosticDir(home, state.Bundle.ParkedSessionID),
			}
			delivery, deliverErr := deps.deliverSSH(ctx, retainedSSH, admission.Raw, courierOptions)
			if deliverErr != nil {
				return deliverErr
			}
			replay = replay || delivery.Replay
			if err := store.SaveRemoteReceiptUnderLock(lock, admission, delivery.Receipt); err != nil {
				return err
			}
			receipt = &delivery.Receipt
		} else {
			replay = true
		}
		final, err = store.FinalizeRemoteUnderLock(lock, admission, now)
		return err
	})
	if err != nil {
		return sessionResumeOutput{}, err
	}
	if final.Status != sessionpark.StatusResumed || final.Successor == nil || receipt == nil {
		return sessionResumeOutput{}, fmt.Errorf("remote parked-session resume completed without one finalized source receipt")
	}
	return sessionResumeOutput{ParkedSessionID: final.Bundle.ParkedSessionID, Status: string(final.Status), TargetMachine: target,
		SuccessorWBSessionID: final.Successor.WBSessionID, MemberCount: len(final.Bundle.Worktrees),
		ReceiptDigest: publicReceiptDigest(*receipt), Replay: replay}, nil
}

func parkedRemoteSSHConfig(route *sessionpark.ResumeRoute, target, via, configPath string) (sessionmove.SSHConfig, error) {
	if route != nil {
		if route.Mode != sessionpark.ResumeRouteRemote || route.TargetMachine != target {
			return sessionmove.SSHConfig{}, fmt.Errorf("parked session was already claimed by a different local or remote winner")
		}
		retained, err := route.SSHConfig()
		if err != nil {
			return sessionmove.SSHConfig{}, err
		}
		if strings.TrimSpace(configPath) == "" {
			return retained, nil
		}
		configured, err := loadParkedRemoteSSHConfig(target, via, configPath)
		if err != nil || configured != retained {
			return sessionmove.SSHConfig{}, fmt.Errorf("configured parked-session SSH route differs from the retained route")
		}
		return retained, nil
	}
	return loadParkedRemoteSSHConfig(target, via, configPath)
}

func loadParkedRemoteSSHConfig(target, via, configPath string) (sessionmove.SSHConfig, error) {
	if strings.TrimSpace(configPath) == "" {
		configPath = wbconfig.DefaultPath()
	}
	config, err := sessionmove.LoadConfig(configPath)
	if err != nil {
		return sessionmove.SSHConfig{}, fmt.Errorf("load parked-session resume courier configuration")
	}
	targetConfig, ok := config.Target(target)
	// ssh.wb_path is not rejected here: park ignores it (and warns) rather
	// than refusing an operator who set it legitimately for session move.
	if !ok || targetConfig.SSH == nil || via == "" && targetConfig.DefaultCourier != sessionmove.CourierSSH {
		return sessionmove.SSHConfig{}, fmt.Errorf("parked-session target does not configure the fixed SSH courier")
	}
	if err := targetConfig.SSH.Validate(); err != nil {
		return sessionmove.SSHConfig{}, fmt.Errorf("parked-session SSH courier configuration is invalid")
	}
	return *targetConfig.SSH, nil
}

func resumeParkedLocal(ctx context.Context, deps sessionResumeDependencies, store sessionpark.Store, lock *sessionpark.SourceLock, state sessionpark.State, now time.Time) (sessionResumeOutput, error) {
	if state.Status == sessionpark.StatusResumed {
		if state.ResumeRoute == nil || state.ResumeRoute.Mode != sessionpark.ResumeRouteLocal || state.RemoteReceipt != nil || state.Successor == nil {
			return sessionResumeOutput{}, fmt.Errorf("parked session was already resumed by a remote winner")
		}
		return sessionResumeOutput{ParkedSessionID: state.Bundle.ParkedSessionID, Status: string(state.Status),
			TargetMachine: state.Successor.Machine, SuccessorWBSessionID: state.Successor.WBSessionID,
			MemberCount: len(state.Bundle.Worktrees), Replay: true}, nil
	}
	if state.ResumeRoute == nil && deps.preflightLocal != nil {
		if err := deps.preflightLocal(state.Bundle.Source); err != nil {
			return sessionResumeOutput{}, err
		}
	}
	if deps.withLocalCustody == nil || deps.attachLocal == nil || deps.startLocal == nil || deps.inspectLocal == nil || deps.inspectPrepared == nil {
		return sessionResumeOutput{}, fmt.Errorf("local parked-session resume dependencies are unavailable")
	}
	bundleRaw, err := sessionpark.EncodeBundle(state.Bundle)
	if err != nil {
		return sessionResumeOutput{}, err
	}
	digest := sessionmove.DigestBytes(bundleRaw)
	replayAttemptID := ""
	var inspected *sessionlaunch.Result
	if state.ResumeRoute != nil {
		if state.ResumeRoute.Mode != sessionpark.ResumeRouteLocal {
			return sessionResumeOutput{}, fmt.Errorf("parked session resume route is already claimed by remote:%s", state.ResumeRoute.TargetMachine)
		}
		if continuationPath, continuation, found, inspectErr := store.LoadLocalSuccessorContextUnderLock(lock); inspectErr != nil {
			return sessionResumeOutput{}, inspectErr
		} else if found {
			if root, rootFound, rootErr := store.ExistingLocalLaunchRootUnderLock(lock); rootErr != nil {
				return sessionResumeOutput{}, rootErr
			} else if rootFound {
				authority, authorityErr := sessionpark.LocalLaunchAuthority(state.Bundle, digest, continuationPath, continuation)
				if authorityErr != nil {
					return sessionResumeOutput{}, authorityErr
				}
				inspectOptions := sessionlaunch.Options{
					ProjectsRoot: projectsRoot, Authority: &authority, StoreRoot: store.Root, Fence: lock,
					WorktreeDir: root, PinnedCommit: authority.PinnedCommit,
				}
				candidate, candidateErr := deps.inspectLocal(ctx, inspectOptions)
				switch {
				case candidateErr == nil:
					replayAttemptID = candidate.AttemptID
					inspected = &candidate
				case errors.Is(candidateErr, sessionlaunch.ErrNotReleased):
					preparedAttemptID, preparedErr := deps.inspectPrepared(ctx, inspectOptions)
					if preparedErr == nil {
						if preparedAttemptID == "" {
							return sessionResumeOutput{}, fmt.Errorf("prepared local launcher inspection returned an empty attempt ID")
						}
						replayAttemptID = preparedAttemptID
					} else if !errors.Is(preparedErr, sessionlaunch.ErrNotReleased) {
						return sessionResumeOutput{}, preparedErr
					}
				case errors.Is(candidateErr, sessionlaunch.ErrRetryableLaunch):
					var failure *sessionlaunch.AttemptFailureError
					if errors.As(candidateErr, &failure) {
						replayAttemptID = failure.Evidence.AttemptID
					}
				default:
					return sessionResumeOutput{}, candidateErr
				}
			}
		}
	}
	var final sessionpark.State
	replay := false
	err = deps.withLocalCustody(ctx, projectsRoot, state.Bundle, replayAttemptID, func(custody *worktrees.ParkedLocalCustody) error {
		if _, _, err := store.PrepareLocalUnderLock(lock, now); err != nil {
			return err
		}
		continuationPath, continuation, err := store.EnsureLocalSuccessorContextUnderLock(lock)
		if err != nil {
			return err
		}
		root, err := store.LocalLaunchRootUnderLock(lock)
		if err != nil {
			return err
		}
		authority, err := sessionpark.LocalLaunchAuthority(state.Bundle, digest, continuationPath, continuation)
		if err != nil {
			return err
		}
		beforeRelease := func(ctx context.Context, prepared sessionlaunch.Prepared) (string, error) {
			record := prepared.Session
			if err := deps.attachLocal(ctx, custody, record, prepared.AttemptID, prepared.AttemptIndex); err != nil {
				return "", err
			}
			if len(state.Bundle.Worktrees) == 0 {
				return "", nil
			}
			return state.Bundle.Worktrees[0].WorkLogReference, nil
		}
		launchOptions := sessionlaunch.Options{
			ProjectsRoot: projectsRoot, Authority: &authority, StoreRoot: store.Root, Fence: lock,
			WorktreeDir: root, PinnedCommit: authority.PinnedCommit, BeforeRelease: beforeRelease,
		}
		var launch sessionlaunch.Result
		if inspected != nil {
			launch = *inspected
			record := session.Record{PID: launch.PID, WBSessionID: launch.WBSessionID, PredecessorWBSessionID: launch.PredecessorWBSessionID,
				Machine: launch.TargetMachine, Runtime: launch.Runtime, Model: launch.Model, NativeHarnessID: launch.NativeHarnessID,
				TmuxName: launch.TmuxName, HandoffID: launch.HandoffID, StartedAt: launch.StartedAt}
			if err := deps.attachLocal(ctx, custody, record, launch.AttemptID, launch.AttemptIndex); err != nil {
				return err
			}
		} else {
			launch, err = deps.startLocal(ctx, launchOptions)
			if err != nil {
				return err
			}
		}
		if deps.afterLocalLaunch != nil {
			if err := deps.afterLocalLaunch(launch); err != nil {
				return err
			}
		}
		successor := session.Record{PID: launch.PID, WBSessionID: launch.WBSessionID, PredecessorWBSessionID: launch.PredecessorWBSessionID,
			Machine: launch.TargetMachine, Runtime: launch.Runtime, Model: launch.Model, NativeHarnessID: launch.NativeHarnessID,
			TmuxName: launch.TmuxName, HandoffID: launch.HandoffID, StartedAt: launch.StartedAt}
		final, err = store.ResumeUnderLock(lock, successor, now)
		replay = launch.Reused
		return err
	})
	if err != nil {
		return sessionResumeOutput{}, err
	}
	if final.Status != sessionpark.StatusResumed || final.Successor == nil {
		return sessionResumeOutput{}, fmt.Errorf("local parked-session resume completed without one finalized successor")
	}
	return sessionResumeOutput{ParkedSessionID: final.Bundle.ParkedSessionID, Status: string(final.Status), TargetMachine: final.Successor.Machine,
		SuccessorWBSessionID: final.Successor.WBSessionID, MemberCount: len(final.Bundle.Worktrees), Replay: replay}, nil
}

func publicReceiptDigest(receipt sessionpark.Receipt) sessionmove.Digest {
	raw, err := sessionpark.EncodeReceipt(receipt)
	if err != nil {
		return ""
	}
	return sessionmove.DigestBytes(raw)
}

func writeSessionResumeOutput(command *cobra.Command, format string, output sessionResumeOutput) error {
	if format == "json" {
		encoder := json.NewEncoder(command.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(output)
	}
	verb := "resumed"
	if output.Replay {
		verb = "replayed"
	}
	_, err := fmt.Fprintf(command.OutOrStdout(), "%s parked session %s as successor %s on %s with %d worktrees\n",
		verb, output.ParkedSessionID, output.SuccessorWBSessionID, output.TargetMachine, output.MemberCount)
	return err
}

func validateParkedRemoteBundle(bundle sessionpark.Bundle, target string) error {
	if len(bundle.Worktrees) == 0 || len(bundle.Worktrees) > sessionpark.MaxMembers {
		return fmt.Errorf("cannot resume parked session %s to %s: remote resume requires between 1 and %d owned worktrees", bundle.ParkedSessionID, target, sessionpark.MaxMembers)
	}
	for _, wt := range bundle.Worktrees {
		if wt.Dirty || wt.Head == "" || wt.RemoteHead == "" || wt.Head != wt.RemoteHead || wt.WorkLogReference == "" || wt.OwnerEventID == "" {
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
	path = strings.TrimSpace(path)
	if path == "" {
		writeParkJudgmentChecklist(command, "park requires an agent-authored continuation. WB derives observable state itself; record what it cannot observe:")
		return nil, fmt.Errorf("park requires --context-file; use - to read stdin")
	}
	if path == "-" {
		return readBounded(command.InOrStdin(), sessionpark.MaxContinuationBytes, "park context")
	}
	return readBoundedRegularFile(path, sessionpark.MaxContinuationBytes)
}

// parkResumeDiagnosticDir is the private per-resume journal directory that may
// receive remote courier diagnostics. It follows the Work Log's existing
// private-data conventions -- under WB_HOME/worklogs, never inside a worktree,
// 0700 directories and 0600 files -- rather than inventing a new location.
func parkResumeDiagnosticDir(home, parkedSessionID string) string {
	if home == "" || parkedSessionID == "" {
		return ""
	}
	return filepath.Join(home, "worklogs", sessionpark.TargetDirName, parkedSessionID)
}
