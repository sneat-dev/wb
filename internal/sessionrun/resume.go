package sessionrun

import (
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkcourier"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"io"
	"path/filepath"
	"strings"
	"time"
)

type ResumeRequest struct {
	ProjectsRoot, ParkedSessionID, Target, Via, ConfigPath string
	Stderr                                                 io.Writer
}
type ResumeResult struct {
	ParkedSessionID      string             `json:"parked_session_id"`
	Status               string             `json:"status"`
	TargetMachine        string             `json:"target_machine"`
	SuccessorWBSessionID string             `json:"successor_wb_session_id"`
	MemberCount          int                `json:"member_count"`
	ReceiptDigest        sessionmove.Digest `json:"receipt_digest,omitempty"`
	Replay               bool               `json:"replay"`
}

type ResumeDependencies struct {
	Home func(string) (string, error)

	Now               func() time.Time
	DeliverSSH        func(context.Context, sessionmove.SSHConfig, []byte, sessionparkcourier.Options) (sessionparkcourier.Result, error)
	WithRemoteCustody func(context.Context, string, sessionpark.Bundle, func() error) error
	WithLocalCustody  func(context.Context, string, sessionpark.Bundle, string, func(*worktrees.ParkedLocalCustody) error) error
	AttachLocal       func(context.Context, *worktrees.ParkedLocalCustody, session.Record, string, uint64) error
	StartLocal        func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error)
	InspectLocal      func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error)
	InspectPrepared   func(context.Context, sessionlaunch.Options) (string, error)
	AfterLocalLaunch  func(sessionlaunch.Result) error
	MarkResumed       func(string, int, string, string) (session.Record, error)
	PreflightLocal    func(session.Record) error
}

func DefaultResumeDependencies() ResumeDependencies {
	return ResumeDependencies{Home: wbhome.Root, PreflightLocal: func(source session.Record) error { return sessionlaunch.PreflightLocal(source.Runtime) },
		Now: func() time.Time { return time.Now().UTC() },
		DeliverSSH: func(ctx context.Context, config sessionmove.SSHConfig, raw []byte, options sessionparkcourier.Options) (sessionparkcourier.Result, error) {
			deliverer, err := sessionparkcourier.NewSSHDeliverer(config, options)
			if err != nil {
				return sessionparkcourier.Result{}, err
			}
			return deliverer.Deliver(ctx, raw)
		},
		WithRemoteCustody: worktrees.WithParkedRemoteResumeCustody,
		WithLocalCustody:  worktrees.WithParkedLocalResumeCustodyForAttempt,
		AttachLocal: func(ctx context.Context, custody *worktrees.ParkedLocalCustody, successor session.Record, attemptID string, attemptIndex uint64) error {
			return custody.Attach(ctx, successor, attemptID, attemptIndex)
		},
		StartLocal:      sessionlaunch.Start,
		InspectLocal:    sessionlaunch.Inspect,
		InspectPrepared: sessionlaunch.InspectPreparedParkAttempt,
		MarkResumed:     session.MarkResumed,
	}
}

type ResumeService struct{ deps ResumeDependencies }

func NewResume(deps ResumeDependencies) *ResumeService { return &ResumeService{deps: deps} }
func (s *ResumeService) Resume(ctx context.Context, request ResumeRequest) (ResumeResult, error) {
	deps := s.deps
	projectsRoot, target, via, configPath := request.ProjectsRoot, request.Target, request.Via, request.ConfigPath
	home, err := deps.Home(projectsRoot)
	if err != nil {
		return ResumeResult{}, err
	}
	store := sessionpark.NewStore(filepath.Join(home, sessionpark.SourceDirName))
	lock, err := store.Acquire(ctx, request.ParkedSessionID)
	if err != nil {
		return ResumeResult{}, err
	}
	defer func() { _ = lock.Close() }()
	state, err := store.LoadUnderLock(lock)
	if err != nil {
		return ResumeResult{}, err
	}
	Now := time.Now().UTC()
	if deps.Now != nil {
		Now = deps.Now()
	}
	if target != "" {
		output, err := resumeParkedRemote(projectsRoot, ctx, deps, store, lock, state, target, via, configPath, Now, request.Stderr, home)
		if err != nil {
			return ResumeResult{}, err
		}
		if deps.MarkResumed == nil {
			return ResumeResult{}, fmt.Errorf("session resumed lifecycle projection dependency is unavailable")
		}
		if _, err := deps.MarkResumed(filepath.Join(home, session.DirName), state.Bundle.Source.PID, state.Bundle.ParkedSessionID, output.SuccessorWBSessionID); err != nil {
			return ResumeResult{}, err
		}
		return output, nil
	}
	if via != "" || configPath != "" {
		return ResumeResult{}, fmt.Errorf("--via and --config require --to for remote resume")
	}
	output, err := resumeParkedLocal(projectsRoot, ctx, deps, store, lock, state, Now)
	if err != nil {
		return ResumeResult{}, err
	}
	if deps.MarkResumed == nil {
		return ResumeResult{}, fmt.Errorf("session resumed lifecycle projection dependency is unavailable")
	}
	if _, err := deps.MarkResumed(filepath.Join(home, session.DirName), state.Bundle.Source.PID, state.Bundle.ParkedSessionID, output.SuccessorWBSessionID); err != nil {
		return ResumeResult{}, err
	}
	return output, nil
}
func resumeParkedRemote(projectsRoot string, ctx context.Context, deps ResumeDependencies, store sessionpark.Store, lock *sessionpark.SourceLock, state sessionpark.State, target, via, configPath string, Now time.Time, warn io.Writer, home string) (ResumeResult, error) {
	if via != "" && via != string(sessionmove.CourierSSH) {
		return ResumeResult{}, fmt.Errorf("unsupported resume courier %q; use ssh", via)
	}
	if state.Status == sessionpark.StatusResumed {
		if state.ResumeRoute == nil || state.ResumeRoute.Mode != sessionpark.ResumeRouteRemote || state.ResumeRoute.TargetMachine != target ||
			state.RemoteReceipt == nil || state.RemoteReceipt.TargetMachine != target || state.Successor == nil {
			return ResumeResult{}, fmt.Errorf("parked session was already resumed by a different local or remote winner")
		}
		if _, err := parkedRemoteSSHConfig(state.ResumeRoute, target, via, configPath); err != nil {
			return ResumeResult{}, err
		}
		return ResumeResult{ParkedSessionID: state.Bundle.ParkedSessionID, Status: string(state.Status), TargetMachine: target,
			SuccessorWBSessionID: state.Successor.WBSessionID, MemberCount: len(state.Bundle.Worktrees),
			ReceiptDigest: publicReceiptDigest(*state.RemoteReceipt), Replay: true}, nil
	}
	sshConfig, err := parkedRemoteSSHConfig(state.ResumeRoute, target, via, configPath)
	if err != nil {
		return ResumeResult{}, err
	}
	if err := validateParkedRemoteBundle(state.Bundle, target); err != nil {
		return ResumeResult{}, err
	}
	if deps.WithRemoteCustody == nil || deps.DeliverSSH == nil {
		return ResumeResult{}, fmt.Errorf("remote parked-session resume dependencies are unavailable")
	}
	var final sessionpark.State
	var receipt *sessionpark.Receipt
	replay := false
	err = deps.WithRemoteCustody(ctx, projectsRoot, state.Bundle, func() error {
		admission, err := store.PrepareRemoteUnderLock(lock, target, "", string(sessionmove.CourierSSH), sshConfig, Now)
		if err != nil {
			return err
		}
		replay = admission.Replay
		receipt, err = store.LoadRemoteReceiptUnderLock(lock, admission)
		if err != nil {
			return err
		}
		if receipt == nil {
			// PrepareRemoteUnderLock binds this validated config to the exact
			// durable route; LoadRemoteReceiptUnderLock revalidates admission.
			// Remote diagnostics land in the private local journal, under the
			// same posture as the Work Log: never in source Git, public
			// reports, or Synchestra envelopes.
			courierOptions := sessionparkcourier.Options{
				Warn:          warn,
				DiagnosticDir: parkResumeDiagnosticDir(home, state.Bundle.ParkedSessionID),
			}
			delivery, deliverErr := deps.DeliverSSH(ctx, sshConfig, admission.Raw, courierOptions)
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
		final, err = store.FinalizeRemoteUnderLock(lock, admission, Now)
		return err
	})
	if err != nil {
		return ResumeResult{}, err
	}
	if final.Status != sessionpark.StatusResumed || final.Successor == nil || receipt == nil {
		return ResumeResult{}, fmt.Errorf("remote parked-session resume completed without one finalized source receipt")
	}
	return ResumeResult{ParkedSessionID: final.Bundle.ParkedSessionID, Status: string(final.Status), TargetMachine: target,
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
	// Admission subsequently refuses nonempty wb_path for the fixed remote
	// park protocol; configuration loading itself preserves that value.
	if !ok || targetConfig.SSH == nil || via == "" && targetConfig.DefaultCourier != sessionmove.CourierSSH {
		return sessionmove.SSHConfig{}, fmt.Errorf("parked-session target does not configure the fixed SSH courier")
	}
	// LoadConfig already validates every nonnil SSH config before returning.
	return *targetConfig.SSH, nil
}
func resumeParkedLocal(projectsRoot string, ctx context.Context, deps ResumeDependencies, store sessionpark.Store, lock *sessionpark.SourceLock, state sessionpark.State, Now time.Time) (ResumeResult, error) {
	if state.Status == sessionpark.StatusResumed {
		if state.ResumeRoute == nil || state.ResumeRoute.Mode != sessionpark.ResumeRouteLocal || state.RemoteReceipt != nil || state.Successor == nil {
			return ResumeResult{}, fmt.Errorf("parked session was already resumed by a remote winner")
		}
		return ResumeResult{ParkedSessionID: state.Bundle.ParkedSessionID, Status: string(state.Status),
			TargetMachine: state.Successor.Machine, SuccessorWBSessionID: state.Successor.WBSessionID,
			MemberCount: len(state.Bundle.Worktrees), Replay: true}, nil
	}
	if state.ResumeRoute == nil && deps.PreflightLocal != nil {
		if err := deps.PreflightLocal(state.Bundle.Source); err != nil {
			return ResumeResult{}, err
		}
	}
	if deps.WithLocalCustody == nil || deps.AttachLocal == nil || deps.StartLocal == nil || deps.InspectLocal == nil || deps.InspectPrepared == nil {
		return ResumeResult{}, fmt.Errorf("local parked-session resume dependencies are unavailable")
	}
	bundleRaw, err := sessionpark.EncodeBundle(state.Bundle)
	if err != nil {
		return ResumeResult{}, err
	}
	digest := sessionmove.DigestBytes(bundleRaw)
	replayAttemptID := ""
	var inspected *sessionlaunch.Result
	if state.ResumeRoute != nil {
		if state.ResumeRoute.Mode != sessionpark.ResumeRouteLocal {
			return ResumeResult{}, fmt.Errorf("parked session resume route is already claimed by remote:%s", state.ResumeRoute.TargetMachine)
		}
		if continuationPath, continuation, found, inspectErr := store.LoadLocalSuccessorContextUnderLock(lock); inspectErr != nil {
			return ResumeResult{}, inspectErr
		} else if found {
			if root, rootFound, rootErr := store.ExistingLocalLaunchRootUnderLock(lock); rootErr != nil {
				return ResumeResult{}, rootErr
			} else if rootFound {
				authority, authorityErr := sessionpark.LocalLaunchAuthority(state.Bundle, digest, continuationPath, continuation)
				if authorityErr != nil {
					return ResumeResult{}, authorityErr
				}
				inspectOptions := sessionlaunch.Options{
					ProjectsRoot: projectsRoot, Authority: &authority, StoreRoot: store.Root, Fence: lock,
					WorktreeDir: root, PinnedCommit: authority.PinnedCommit,
				}
				candidate, candidateErr := deps.InspectLocal(ctx, inspectOptions)
				switch {
				case candidateErr == nil:
					replayAttemptID = candidate.AttemptID
					inspected = &candidate
				case errors.Is(candidateErr, sessionlaunch.ErrNotReleased):
					preparedAttemptID, preparedErr := deps.InspectPrepared(ctx, inspectOptions)
					if preparedErr == nil {
						if preparedAttemptID == "" {
							return ResumeResult{}, fmt.Errorf("prepared local launcher inspection returned an empty attempt ID")
						}
						replayAttemptID = preparedAttemptID
					} else if !errors.Is(preparedErr, sessionlaunch.ErrNotReleased) {
						return ResumeResult{}, preparedErr
					}
				case errors.Is(candidateErr, sessionlaunch.ErrRetryableLaunch):
					var failure *sessionlaunch.AttemptFailureError
					if errors.As(candidateErr, &failure) {
						replayAttemptID = failure.Evidence.AttemptID
					}
				default:
					return ResumeResult{}, candidateErr
				}
			}
		}
	}
	var final sessionpark.State
	replay := false
	err = deps.WithLocalCustody(ctx, projectsRoot, state.Bundle, replayAttemptID, func(custody *worktrees.ParkedLocalCustody) error {
		if _, _, err := store.PrepareLocalUnderLock(lock, Now); err != nil {
			return err
		}
		continuationPath, continuation, err := store.EnsureLocalSuccessorContextUnderLock(lock, custody.ResolvedWorktreeDirs())
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
			if err := deps.AttachLocal(ctx, custody, record, prepared.AttemptID, prepared.AttemptIndex); err != nil {
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
			if err := deps.AttachLocal(ctx, custody, record, launch.AttemptID, launch.AttemptIndex); err != nil {
				return err
			}
		} else {
			launch, err = deps.StartLocal(ctx, launchOptions)
			if err != nil {
				return err
			}
		}
		if deps.AfterLocalLaunch != nil {
			if err := deps.AfterLocalLaunch(launch); err != nil {
				return err
			}
		}
		successor := session.Record{PID: launch.PID, WBSessionID: launch.WBSessionID, PredecessorWBSessionID: launch.PredecessorWBSessionID,
			Machine: launch.TargetMachine, Runtime: launch.Runtime, Model: launch.Model, NativeHarnessID: launch.NativeHarnessID,
			TmuxName: launch.TmuxName, HandoffID: launch.HandoffID, StartedAt: launch.StartedAt}
		final, err = store.ResumeUnderLock(lock, successor, Now)
		replay = launch.Reused
		return err
	})
	if err != nil {
		return ResumeResult{}, err
	}
	if final.Status != sessionpark.StatusResumed || final.Successor == nil {
		return ResumeResult{}, fmt.Errorf("local parked-session resume completed without one finalized successor")
	}
	return ResumeResult{ParkedSessionID: final.Bundle.ParkedSessionID, Status: string(final.Status), TargetMachine: final.Successor.Machine,
		SuccessorWBSessionID: final.Successor.WBSessionID, MemberCount: len(final.Bundle.Worktrees), Replay: replay}, nil
}
func publicReceiptDigest(receipt sessionpark.Receipt) sessionmove.Digest {
	raw, err := sessionpark.EncodeReceipt(receipt)
	if err != nil {
		return ""
	}
	return sessionmove.DigestBytes(raw)
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
func parkResumeDiagnosticDir(home, parkedSessionID string) string {
	if home == "" || parkedSessionID == "" {
		return ""
	}
	return filepath.Join(home, "worklogs", sessionpark.TargetDirName, parkedSessionID)
}
