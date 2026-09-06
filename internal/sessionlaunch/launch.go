package sessionlaunch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/wbhome"
)

const PrivateLauncherArgument = "--wb-internal-session-launch"

// ErrNotReleased is the only recovery-safe absence signal. Inspect returns it
// only when no immutable launch/release exists; missing artifacts after a
// release remain hard errors and must never fall back to Git replay.
var ErrNotReleased = errors.New("successor launcher is not released")

// ErrRetryableLaunch means Inspect proved that the latest released attempt has
// exact terminal failure evidence, a dead PID, and no tmux session. Receive may
// route this one outcome to Start without touching Git; Start rechecks all gates.
var ErrRetryableLaunch = errors.New("successor launcher has an exactly retryable terminal attempt")

type Prepared struct {
	Request       sessionmove.Request
	RequestDigest sessionmove.Digest
	Authority     sessionauthority.Launch
	Session       session.Record
	AttemptID     string
	AttemptIndex  uint64
	WorktreeDir   string
	PinnedCommit  string
}

// PreparedEvidence is the narrow recovery identity for a launcher that
// durably published exact ready/process artifacts but may not yet have a
// release. It authorizes only that attempt ID at an external custody barrier;
// Start still decides whether the same wrapper can release or must be replaced.
type PreparedEvidence struct {
	HandoffID     string
	RequestDigest sessionmove.Digest
	AttemptID     string
	AttemptIndex  uint64
	PID           int
	StartedAt     time.Time
	authority     *preparedEvidenceAuthority
}

type preparedEvidenceAuthority struct {
	HandoffID     string
	RequestDigest sessionmove.Digest
	AttemptID     string
	AttemptIndex  uint64
	PID           int
	StartedAt     time.Time
}

func (e PreparedEvidence) Authenticates(handoffID string, digest sessionmove.Digest) bool {
	a := e.authority
	return a != nil && e.HandoffID == a.HandoffID && e.RequestDigest == a.RequestDigest && e.AttemptID == a.AttemptID &&
		e.AttemptIndex == a.AttemptIndex && e.PID == a.PID && e.StartedAt.Equal(a.StartedAt) &&
		e.HandoffID == handoffID && e.RequestDigest == digest
}

// FailureEvidence is descriptor-validated immutable launcher evidence for one
// released attempt. Receivers use it to append Work Log failure lineage
// without parsing error text or guessing a PID/attempt identity.
type FailureEvidence struct {
	HandoffID              string
	RequestDigest          sessionmove.Digest
	AttemptID              string
	AttemptIndex           uint64
	PID                    int
	StartedAt              time.Time
	FailedAt               time.Time
	TargetWorkLogReference string
	Diagnostic             string
	authority              *failureEvidenceAuthority
}

type failureEvidenceAuthority struct {
	HandoffID              string
	RequestDigest          sessionmove.Digest
	AttemptID              string
	AttemptIndex           uint64
	PID                    int
	StartedAt              time.Time
	FailedAt               time.Time
	TargetWorkLogReference string
	Diagnostic             string
}

// Authenticates reports whether this value was constructed from descriptor-
// validated Task4 artifacts and binds the supplied external custody identity.
func (e FailureEvidence) Authenticates(handoffID string, digest sessionmove.Digest, targetWorkLogReference string) bool {
	a := e.authority
	return a != nil && e.HandoffID == a.HandoffID && e.RequestDigest == a.RequestDigest && e.AttemptID == a.AttemptID &&
		e.AttemptIndex == a.AttemptIndex && e.PID == a.PID && e.StartedAt.Equal(a.StartedAt) && e.FailedAt.Equal(a.FailedAt) &&
		e.TargetWorkLogReference == a.TargetWorkLogReference && e.Diagnostic == a.Diagnostic &&
		e.HandoffID == handoffID && e.RequestDigest == digest && e.TargetWorkLogReference == targetWorkLogReference
}

// AttemptFailureError carries exact immutable post-release failure proof.
type AttemptFailureError struct {
	Evidence FailureEvidence
}

func (failure *AttemptFailureError) Error() string {
	return "successor launcher failed after release: " + failure.Evidence.Diagnostic
}

// BeforeRelease is the Task 5 custody seam. It prepares the successor-owned
// Work Log before the wrapper is allowed to Exec and returns the durable target
// Work Log reference that the immutable release must bind to this exact PID.
type BeforeRelease func(context.Context, Prepared) (targetWorkLogRef string, err error)

type Options struct {
	Store         sessionmove.Store
	ProjectsRoot  string
	Request       sessionmove.Request
	RequestDigest sessionmove.Digest
	WorktreeDir   string
	PinnedCommit  string
	ExecutionLock *sessionmove.ExecutionLock
	// Authority/StoreRoot/Fence are the protocol-neutral path used by parked
	// bundles. Existing session-move callers leave them zero and are adapted
	// byte-for-byte from Request, Store, and ExecutionLock.
	Authority     *sessionauthority.Launch
	StoreRoot     string
	Fence         sessionauthority.Fence
	BeforeRelease BeforeRelease
}

type resolvedAuthority struct {
	launch    sessionauthority.Launch
	storeRoot string
	fence     sessionauthority.Fence
}

func resolveAuthority(options Options) (resolvedAuthority, error) {
	if options.Authority == nil {
		if _, err := sessionmove.EncodeRequest(options.Request); err != nil {
			return resolvedAuthority{}, err
		}
		// A request with inline handover content (every checkpoint created
		// after the ContinuationPrivate cutover) is delivered as a private
		// file outside the pinned worktree, materialized here into the same
		// retained handoff directory the request itself lives in. A
		// pre-cutover request has no inline content and keeps the deprecated
		// ContinuationTracked delivery, reading the handover already
		// committed into the pinned worktree at its legacy HandoverPath.
		continuationKind := sessionauthority.ContinuationTracked
		continuationPath := options.Request.HandoverPath
		if options.Request.HandoverContent != "" {
			if options.ExecutionLock == nil {
				return resolvedAuthority{}, fmt.Errorf("resolve private handover authority requires the exact held handoff execution lock")
			}
			path, err := options.Store.EnsureHandoverUnderLock(options.ExecutionLock, options.Request.HandoffID, options.RequestDigest)
			if err != nil {
				return resolvedAuthority{}, fmt.Errorf("materialize private handover: %w", err)
			}
			continuationKind = sessionauthority.ContinuationPrivate
			continuationPath = path
		}
		launch := sessionauthority.Launch{
			AggregateID: options.Request.HandoffID, AggregateDigest: string(options.RequestDigest), AggregateFile: "request.json",
			SuccessorWBSessionID: options.Request.SuccessorWBSessionID, PredecessorWBSessionID: options.Request.PredecessorWBSessionID,
			TargetMachine: options.Request.TargetMachine, SourceRuntime: options.Request.SourceRuntime,
			SourceModel: options.Request.SourceModel, RequestedHarness: options.Request.RequestedHarness,
			PinnedCommit: options.Request.BundleCommit, PinnedBranch: "wb-session/" + options.Request.HandoffID,
			ContinuationKind: continuationKind, ContinuationPath: continuationPath,
			ContinuationDigest: string(options.Request.HandoverDigest),
		}
		if err := launch.Validate(); err != nil {
			return resolvedAuthority{}, err
		}
		return resolvedAuthority{launch: launch, storeRoot: options.Store.Root, fence: options.ExecutionLock}, nil
	}
	launch := *options.Authority
	if err := launch.Validate(); err != nil {
		return resolvedAuthority{}, err
	}
	return resolvedAuthority{launch: launch, storeRoot: options.StoreRoot, fence: options.Fence}, nil
}

type Result struct {
	HandoffID              string    `json:"handoff_id"`
	WBSessionID            string    `json:"wb_session_id"`
	PredecessorWBSessionID string    `json:"predecessor_wb_session_id"`
	TargetMachine          string    `json:"target_machine"`
	PID                    int       `json:"pid"`
	AttemptID              string    `json:"attempt_id"`
	AttemptIndex           uint64    `json:"attempt_index"`
	TmuxName               string    `json:"tmux_name"`
	Runtime                string    `json:"runtime"`
	Model                  string    `json:"model,omitempty"`
	NativeHarnessID        string    `json:"native_harness_id,omitempty"`
	TargetWorkLogRef       string    `json:"target_work_log_ref,omitempty"`
	WorktreeDir            string    `json:"worktree_dir"`
	PinnedCommit           string    `json:"pinned_commit"`
	StartedAt              time.Time `json:"started_at"`
	Reused                 bool      `json:"reused"`
}

type dependencies struct {
	tmux          tmux
	lookPath      func(string) (string, error)
	wbExecutable  func() (string, error)
	sessionDir    func(string) (string, error)
	now           func() time.Time
	pollInterval  time.Duration
	startTimeout  time.Duration
	verifyPinned  func(context.Context, launchPlan) error
	processStatus func(int) error
}

func defaultDependencies(projectsRoot string) (dependencies, error) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return dependencies{}, fmt.Errorf("fixed tmux executable is unavailable: %w", err)
	}
	tmuxPath, err = cleanAbsoluteExecutable(tmuxPath)
	if err != nil {
		return dependencies{}, err
	}
	return dependencies{
		tmux: osTmux{executable: tmuxPath}, lookPath: exec.LookPath, wbExecutable: os.Executable,
		sessionDir: func(root string) (string, error) {
			home, err := wbhome.EnsureRoot(root)
			if err != nil {
				return "", err
			}
			return filepath.Join(home, session.DirName), nil
		},
		now: func() time.Time { return time.Now().UTC() }, pollInterval: 25 * time.Millisecond, startTimeout: 30 * time.Second,
		verifyPinned:  verifyPinnedWorktree,
		processStatus: processStatus,
	}, nil
}

func Start(ctx context.Context, options Options) (Result, error) {
	deps, err := defaultDependencies(options.ProjectsRoot)
	if err != nil {
		return Result{}, err
	}
	return startWithDependencies(ctx, options, deps)
}

// PreflightLocal verifies the fixed local launcher dependencies without
// creating a launch plan, claiming a resume route, or changing custody.
func PreflightLocal(sourceRuntime string) error {
	deps, err := defaultDependencies("")
	if err != nil {
		return err
	}
	authority := sessionauthority.Launch{SourceRuntime: sourceRuntime}
	spec, err := harnessSpecForAuthority(authority, "/")
	if err != nil {
		return err
	}
	path, err := deps.lookPath(spec.Executable)
	if err != nil {
		return fmt.Errorf("fixed %s harness executable %q is unavailable: %w", spec.Runtime, spec.Executable, err)
	}
	if _, err := cleanAbsoluteExecutable(path); err != nil {
		return err
	}
	path, err = deps.wbExecutable()
	if err != nil {
		return fmt.Errorf("locate private WB launcher: %w", err)
	}
	_, err = cleanAbsoluteExecutable(path)
	return err
}

func Inspect(ctx context.Context, options Options) (Result, error) {
	deps, err := defaultDependencies(options.ProjectsRoot)
	if err != nil {
		return Result{}, err
	}
	return inspectWithDependencies(ctx, options, deps, true)
}

// InspectPrepared authenticates the most recent descriptor-bound ready
// attempt when no launch has been released. It never declares success and
// never chooses retry policy; Start performs the live-wrapper/abandonment
// checks again while holding the same aggregate fence.
func InspectPrepared(_ context.Context, options Options) (PreparedEvidence, error) {
	resolved, err := resolveAuthority(options)
	if err != nil {
		return PreparedEvidence{}, err
	}
	authority := resolved.launch
	if resolved.fence == nil || !resolved.fence.HeldForSession(resolved.storeRoot, authority.AggregateID, authority.AggregateDigest) {
		return PreparedEvidence{}, fmt.Errorf("prepared successor inspection requires the exact held handoff execution lock")
	}
	handoffAuthority, err := resolved.fence.RetainSessionDir(resolved.storeRoot, authority.AggregateID, authority.AggregateDigest)
	if err != nil {
		return PreparedEvidence{}, err
	}
	state, err := openLaunchStateFromHandoff(authority.AggregateID, handoffAuthority, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PreparedEvidence{}, ErrNotReleased
		}
		return PreparedEvidence{}, err
	}
	defer func() { _ = state.Close() }()
	plan, planDigest, err := state.loadPlan()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PreparedEvidence{}, ErrNotReleased
		}
		return PreparedEvidence{}, err
	}
	worktree, err := filepath.Abs(options.WorktreeDir)
	if err != nil || filepath.Clean(worktree) != worktree {
		return PreparedEvidence{}, fmt.Errorf("successor worktree must be a clean absolute path")
	}
	if err := validatePlanForOptions(plan, options, resolved, worktree); err != nil {
		return PreparedEvidence{}, err
	}
	if _, err := state.loadStarted(); err == nil {
		return PreparedEvidence{}, fmt.Errorf("successor already has an immutable started marker")
	} else if !errors.Is(err, os.ErrNotExist) {
		return PreparedEvidence{}, err
	}
	refs, err := state.listAttempts()
	if err != nil {
		return PreparedEvidence{}, err
	}
	for index := len(refs) - 1; index >= 0; index-- {
		attempt, openErr := state.openAttempt(refs[index].id)
		if openErr != nil {
			return PreparedEvidence{}, openErr
		}
		release, releaseDigest, releaseErr := attempt.loadRelease()
		if releaseErr == nil {
			// A newer claimed attempt can exist only after Start proved this
			// released attempt terminal and retryable. If the coordinator then
			// crashed before the newer wrapper became ready, external custody
			// still names this exact prior attempt.
			if index == len(refs)-1 {
				_ = attempt.Close()
				return PreparedEvidence{}, fmt.Errorf("prepared successor inspection found an already released latest attempt")
			}
			ready, readyDigest, readyErr := attempt.loadReady(release.PID)
			if readyErr != nil {
				_ = attempt.Close()
				return PreparedEvidence{}, readyErr
			}
			failure, found, failureErr := attempt.loadExecFailure(release.PID)
			if failureErr != nil {
				_ = attempt.Close()
				return PreparedEvidence{}, failureErr
			}
			if !found || release.RequestDigest != plan.RequestDigest || release.PlanDigest != planDigest || release.ReadyDigest != readyDigest ||
				ready.RequestDigest != plan.RequestDigest || ready.PlanDigest != planDigest || failure.RequestDigest != plan.RequestDigest ||
				failure.PlanDigest != planDigest || failure.ReadyDigest != readyDigest || failure.ReleaseDigest != releaseDigest ||
				!sameReadySession(plan, ready, ready.Session) {
				_ = attempt.Close()
				return PreparedEvidence{}, fmt.Errorf("prior released custody attempt lacks exact terminal evidence")
			}
			evidence := preparedEvidenceFrom(attempt, plan, ready)
			_ = attempt.Close()
			return evidence, nil
		} else if !errors.Is(releaseErr, os.ErrNotExist) {
			_ = attempt.Close()
			return PreparedEvidence{}, releaseErr
		}
		pid, found, evidenceErr := attempt.preReleaseProcessEvidence()
		if evidenceErr != nil {
			_ = attempt.Close()
			return PreparedEvidence{}, evidenceErr
		}
		if !found {
			_ = attempt.Close()
			continue
		}
		ready, _, readyErr := attempt.loadReady(pid)
		if errors.Is(readyErr, os.ErrNotExist) {
			_ = attempt.Close()
			continue
		}
		if readyErr != nil {
			_ = attempt.Close()
			return PreparedEvidence{}, readyErr
		}
		if ready.RequestDigest != plan.RequestDigest || ready.PlanDigest != planDigest || !sameReadySession(plan, ready, ready.Session) {
			_ = attempt.Close()
			return PreparedEvidence{}, fmt.Errorf("prepared successor evidence conflicts with its immutable plan")
		}
		evidence := preparedEvidenceFrom(attempt, plan, ready)
		_ = attempt.Close()
		return evidence, nil
	}
	return PreparedEvidence{}, ErrNotReleased
}

func preparedEvidenceFrom(attempt *launchAttempt, plan launchPlan, ready launcherReady) PreparedEvidence {
	evidence := PreparedEvidence{HandoffID: plan.HandoffID, RequestDigest: plan.RequestDigest,
		AttemptID: attempt.id, AttemptIndex: attempt.index, PID: ready.PID, StartedAt: ready.Session.StartedAt}
	evidence.authority = &preparedEvidenceAuthority{HandoffID: evidence.HandoffID, RequestDigest: evidence.RequestDigest,
		AttemptID: evidence.AttemptID, AttemptIndex: evidence.AttemptIndex, PID: evidence.PID, StartedAt: evidence.StartedAt}
	return evidence
}

func startWithDependencies(ctx context.Context, options Options, deps dependencies) (Result, error) {
	resolved, err := resolveAuthority(options)
	if err != nil {
		return Result{}, err
	}
	authority := resolved.launch
	digest := sessionmove.Digest(authority.AggregateDigest)
	if resolved.fence == nil || !resolved.fence.HeldForSession(resolved.storeRoot, authority.AggregateID, authority.AggregateDigest) {
		return Result{}, fmt.Errorf("successor launch requires the exact held handoff execution lock")
	}
	if options.PinnedCommit != authority.PinnedCommit {
		return Result{}, fmt.Errorf("successor pinned commit %s does not match admitted commit %s", options.PinnedCommit, authority.PinnedCommit)
	}
	worktree, err := filepath.Abs(options.WorktreeDir)
	if err != nil || filepath.Clean(worktree) != worktree {
		return Result{}, fmt.Errorf("successor worktree must be a clean absolute path")
	}
	storeRoot, err := filepath.Abs(resolved.storeRoot)
	if err != nil || filepath.Clean(storeRoot) != storeRoot || storeRoot != resolved.storeRoot {
		return Result{}, fmt.Errorf("successor handoff store must be a clean absolute path")
	}
	handoffAuthority, err := resolved.fence.RetainSessionDir(resolved.storeRoot, authority.AggregateID, authority.AggregateDigest)
	if err != nil {
		return Result{}, err
	}
	state, err := openLaunchStateFromHandoff(authority.AggregateID, handoffAuthority, true)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = state.Close() }()
	plan, planDigest, loadPlanErr := state.loadPlan()
	planReplay := loadPlanErr == nil
	if planReplay {
		if err := validatePlanForOptions(plan, options, resolved, worktree); err != nil {
			return Result{}, err
		}
	} else {
		if !errors.Is(loadPlanErr, os.ErrNotExist) {
			return Result{}, loadPlanErr
		}
		spec, specErr := harnessSpecForAuthority(authority, worktree)
		if specErr != nil {
			return Result{}, specErr
		}
		harnessPath, pathErr := deps.lookPath(spec.Executable)
		if pathErr != nil {
			return Result{}, fmt.Errorf("fixed %s harness executable %q is unavailable: %w", spec.Runtime, spec.Executable, pathErr)
		}
		harnessPath, pathErr = cleanAbsoluteExecutable(harnessPath)
		if pathErr != nil {
			return Result{}, pathErr
		}
		wbExecutable, executableErr := deps.wbExecutable()
		if executableErr != nil {
			return Result{}, fmt.Errorf("locate private WB launcher: %w", executableErr)
		}
		wbExecutable, executableErr = cleanAbsoluteExecutable(wbExecutable)
		if executableErr != nil {
			return Result{}, executableErr
		}
		plan = launchPlan{
			SchemaVersion: launchSchemaVersion, HandoffID: authority.AggregateID, RequestDigest: digest,
			SuccessorWBSessionID: authority.SuccessorWBSessionID, PredecessorWBSessionID: authority.PredecessorWBSessionID,
			Machine: authority.TargetMachine, TmuxName: "wb-session-" + authority.SuccessorWBSessionID,
			Runtime: spec.Runtime, Model: spec.Model, StoreRoot: storeRoot, WorktreeDir: worktree,
			PinnedCommit: options.PinnedCommit, PinnedBranch: authority.PinnedBranch,
			RootMode:     string(authority.RootMode),
			HandoverPath: authority.ContinuationPath, AuthorityFile: authority.AggregateFile,
			ContinuationKind: string(authority.ContinuationKind), ContinuationDigest: sessionmove.Digest(authority.ContinuationDigest),
			WBExecutable:      wbExecutable,
			HarnessExecutable: harnessPath, HarnessArgs: append([]string(nil), spec.Args...),
		}
		plan, planDigest, _, err = state.savePlan(plan)
		if err != nil {
			return Result{}, err
		}
	}
	attempt, attemptReused, completed, done, err := selectAttemptForStart(ctx, options, deps, state, plan, planDigest)
	if err != nil {
		return Result{}, err
	}
	if done {
		completed.Reused = true
		return completed, nil
	}
	defer func() { _ = attempt.Close() }()
	if err := validatePlanExecutables(plan); err != nil {
		return Result{}, err
	}

	pid, exists, err := deps.tmux.PanePID(ctx, plan.TmuxName)
	if err != nil {
		return Result{}, err
	}
	abandonment, abandonmentErr := attempt.loadAbandonment()
	if abandonmentErr == nil {
		if exists {
			return Result{}, fmt.Errorf("abandoned launcher attempt %s conflicts with live tmux session %s", attempt.id, plan.TmuxName)
		}
		if err := validateAbandonment(ctx, deps, state, attempt, plan, planDigest, abandonment); err != nil {
			return Result{}, err
		}
		if err := deps.verifyPinned(ctx, plan); err != nil {
			return Result{}, fmt.Errorf("verify pinned worktree after launcher abandonment: %w", err)
		}
		_ = attempt.Close()
		attempt, err = state.createAttempt()
		if err != nil {
			return Result{}, err
		}
		attemptReused, pid, exists = true, 0, false
	} else if !errors.Is(abandonmentErr, os.ErrNotExist) {
		return Result{}, abandonmentErr
	}
	if !exists {
		evidencePID, hasEvidence, evidenceErr := attempt.preReleaseProcessEvidence()
		if evidenceErr != nil {
			return Result{}, evidenceErr
		}
		if hasEvidence {
			held, fenceErr := attempt.execFenceHeld(evidencePID)
			if fenceErr != nil {
				return Result{}, fenceErr
			}
			if held {
				return Result{}, fmt.Errorf("launcher attempt %s has live or ambiguous pre-release PID %d", attempt.id, evidencePID)
			}
			if err := proveProcessDead(deps, evidencePID); err != nil {
				return Result{}, fmt.Errorf("launcher attempt %s cannot prove pre-release PID %d dead: %w", attempt.id, evidencePID, err)
			}
			abandonment, _, err = attempt.saveAbandonment(plan, planDigest, evidencePID, deps.now())
			if err != nil {
				return Result{}, fmt.Errorf("persist exact terminal launcher abandonment: %w", err)
			}
			// Recheck every external and descriptor-bound proof after the
			// immutable marker is durable. A crash after publication can replay
			// this same validation and create at most the immediate next attempt.
			if err := validateAbandonment(ctx, deps, state, attempt, plan, planDigest, abandonment); err != nil {
				return Result{}, err
			}
			if err := deps.verifyPinned(ctx, plan); err != nil {
				return Result{}, fmt.Errorf("verify pinned worktree after launcher abandonment: %w", err)
			}
			_ = attempt.Close()
			attempt, err = state.createAttempt()
			if err != nil {
				return Result{}, err
			}
			attemptReused = true
		}
		if err := deps.tmux.StartDetached(ctx, plan.TmuxName, plan.WorktreeDir, plan.WBExecutable,
			[]string{PrivateLauncherArgument, plan.StoreRoot, plan.HandoffID, attempt.id, string(planDigest)}); err != nil {
			if adoptedPID, adopted, inspectErr := deps.tmux.PanePID(ctx, plan.TmuxName); inspectErr != nil {
				return Result{}, errors.Join(err, inspectErr)
			} else if !adopted {
				return Result{}, err
			} else {
				pid, exists = adoptedPID, true
			}
		}
	}
	ready, err := waitReady(ctx, options, deps, attempt, plan, planDigest)
	if err != nil {
		return Result{}, err
	}
	if exists && ready.PID != pid {
		return Result{}, fmt.Errorf("tmux successor PID changed while authorizing existing launch")
	}
	if err := deps.verifyPinned(ctx, plan); err != nil {
		return Result{}, err
	}
	prepared := Prepared{Request: options.Request, RequestDigest: digest, Authority: authority, Session: ready.Session,
		AttemptID: attempt.id, AttemptIndex: attempt.index,
		WorktreeDir: plan.WorktreeDir, PinnedCommit: plan.PinnedCommit}
	targetWorkLogRef := ""
	if options.BeforeRelease != nil {
		targetWorkLogRef, err = options.BeforeRelease(ctx, prepared)
		if err != nil {
			return Result{}, fmt.Errorf("prepare successor before release: %w", err)
		}
	}
	release, releaseReplay, err := attempt.saveRelease(plan, planDigest, ready, targetWorkLogRef, deps.now())
	if err != nil {
		return Result{}, err
	}
	result, err := inspectReleased(ctx, options, deps, attempt, plan, planDigest, release)
	if err != nil {
		return Result{}, err
	}
	if _, _, err := finalizeStarted(state, attempt, plan, planDigest, release, deps.now()); err != nil {
		return Result{}, err
	}
	result.Reused = planReplay || attemptReused || exists || releaseReplay
	return result, nil
}

func selectAttemptForStart(
	ctx context.Context,
	options Options,
	deps dependencies,
	state *launchState,
	plan launchPlan,
	planDigest sessionmove.Digest,
) (*launchAttempt, bool, Result, bool, error) {
	started, startedErr := state.loadStarted()
	if startedErr == nil {
		result, err := inspectStarted(ctx, options, deps, state, plan, planDigest, started)
		return nil, true, result, err == nil, err
	}
	if !errors.Is(startedErr, os.ErrNotExist) {
		return nil, false, Result{}, false, startedErr
	}
	refs, err := state.listAttempts()
	if err != nil {
		return nil, false, Result{}, false, err
	}
	if len(refs) == 0 {
		attempt, err := state.createAttempt()
		return attempt, false, Result{}, false, err
	}
	latest := refs[len(refs)-1]
	attempt, err := state.openAttempt(latest.id)
	if errors.Is(err, os.ErrNotExist) {
		attempt, err = state.openOrRecoverClaimedAttempt(latest.id)
	}
	if err != nil {
		return nil, true, Result{}, false, err
	}
	release, releaseDigest, releaseErr := attempt.loadRelease()
	if errors.Is(releaseErr, os.ErrNotExist) {
		return attempt, true, Result{}, false, nil
	}
	if releaseErr != nil {
		_ = attempt.Close()
		return nil, true, Result{}, false, releaseErr
	}
	result, inspectErr := inspectReleased(ctx, options, deps, attempt, plan, planDigest, release)
	if inspectErr == nil {
		if _, _, err := finalizeStarted(state, attempt, plan, planDigest, release, deps.now()); err != nil {
			_ = attempt.Close()
			return nil, true, Result{}, false, err
		}
		_ = attempt.Close()
		return nil, true, result, true, nil
	}
	if retryErr := failedAttemptRetryable(ctx, options, deps, state, attempt, plan, planDigest, release, releaseDigest); retryErr != nil {
		_ = attempt.Close()
		return nil, true, Result{}, false, fmt.Errorf("%w; launcher attempt %s cannot be replaced: %v", inspectErr, attempt.id, retryErr)
	}
	_ = attempt.Close()
	if err := deps.verifyPinned(ctx, plan); err != nil {
		return nil, true, Result{}, false, fmt.Errorf("verify corrected pinned worktree before launcher retry: %w", err)
	}
	next, err := state.createAttempt()
	return next, true, Result{}, false, err
}

func failedAttemptRetryable(
	ctx context.Context,
	options Options,
	deps dependencies,
	state *launchState,
	attempt *launchAttempt,
	plan launchPlan,
	planDigest sessionmove.Digest,
	release launcherRelease,
	releaseDigest sessionmove.Digest,
) error {
	if _, err := attempt.loadAbandonment(); err == nil {
		return fmt.Errorf("released launcher attempt also has conflicting abandonment evidence")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := state.loadStarted(); err == nil {
		return fmt.Errorf("an immutable started attempt already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ready, readyDigest, err := attempt.loadReady(release.PID)
	if err != nil {
		return err
	}
	if release.PlanDigest != planDigest || release.ReadyDigest != readyDigest || ready.PlanDigest != planDigest {
		return fmt.Errorf("release does not bind the exact failed attempt")
	}
	held, err := attempt.execFenceHeld(release.PID)
	if err != nil {
		return err
	}
	if held {
		return fmt.Errorf("prior launcher still holds its exec fence")
	}
	failure, found, err := attempt.loadExecFailure(release.PID)
	if err != nil {
		return err
	}
	if !found || failure.RequestDigest != plan.RequestDigest || failure.PlanDigest != planDigest ||
		failure.ReadyDigest != readyDigest || failure.ReleaseDigest != releaseDigest || failure.PID != release.PID {
		return fmt.Errorf("prior launcher has no exact immutable terminal failure evidence")
	}
	if _, exists, err := deps.tmux.PanePID(ctx, plan.TmuxName); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("exact tmux session %s still exists", plan.TmuxName)
	}
	if err := proveProcessDead(deps, release.PID); err != nil {
		return fmt.Errorf("prior launcher PID %d is still live or ambiguous: %w", release.PID, err)
	}
	return nil
}

func validateAbandonment(
	ctx context.Context,
	deps dependencies,
	state *launchState,
	attempt *launchAttempt,
	plan launchPlan,
	planDigest sessionmove.Digest,
	abandonment launcherAbandonment,
) error {
	if abandonment.HandoffID != plan.HandoffID || abandonment.AttemptID != attempt.id ||
		abandonment.AttemptIndex != attempt.index || abandonment.RequestDigest != plan.RequestDigest ||
		abandonment.PlanDigest != planDigest {
		return fmt.Errorf("immutable launcher abandonment does not bind its exact attempt and plan")
	}
	if _, _, err := attempt.loadRelease(); err == nil {
		return fmt.Errorf("abandoned launcher attempt also has a conflicting release")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := state.loadStarted(); err == nil {
		return fmt.Errorf("abandoned launcher attempt conflicts with immutable started marker")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	evidencePID, found, err := attempt.preReleaseProcessEvidence()
	if err != nil {
		return err
	}
	if !found || evidencePID != abandonment.PID {
		return fmt.Errorf("immutable launcher abandonment does not match exact process evidence")
	}
	ready, readyDigest, readyErr := attempt.loadReady(abandonment.PID)
	if abandonment.ReadyDigest == "" {
		if readyErr == nil {
			return fmt.Errorf("launcher ready evidence appeared after attempt abandonment")
		}
		if !errors.Is(readyErr, os.ErrNotExist) {
			return readyErr
		}
	} else {
		if readyErr != nil {
			return readyErr
		}
		if readyDigest != abandonment.ReadyDigest || ready.RequestDigest != plan.RequestDigest || ready.PlanDigest != planDigest {
			return fmt.Errorf("immutable launcher abandonment conflicts with its ready evidence")
		}
	}
	held, err := attempt.execFenceHeld(abandonment.PID)
	if err != nil {
		return err
	}
	if held {
		return fmt.Errorf("abandoned launcher attempt still holds its exec fence")
	}
	if _, exists, err := deps.tmux.PanePID(ctx, plan.TmuxName); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("abandoned launcher attempt conflicts with exact tmux session %s", plan.TmuxName)
	}
	if err := proveProcessDead(deps, abandonment.PID); err != nil {
		return fmt.Errorf("abandoned launcher PID %d is live or ambiguous: %w", abandonment.PID, err)
	}
	return nil
}

func proveProcessDead(deps dependencies, pid int) error {
	probe := deps.processStatus
	if probe == nil {
		probe = processStatus
	}
	err := probe(pid)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("PID is live")
	}
	if errors.Is(err, syscall.EPERM) {
		return fmt.Errorf("PID liveness is permission-ambiguous: %w", err)
	}
	return fmt.Errorf("PID liveness probe is ambiguous: %w", err)
}

func finalizeStarted(state *launchState, attempt *launchAttempt, plan launchPlan, planDigest sessionmove.Digest, release launcherRelease, now time.Time) (launcherStarted, bool, error) {
	_, releaseDigest, err := attempt.loadRelease()
	if err != nil {
		return launcherStarted{}, false, err
	}
	return state.saveStarted(attempt, plan, planDigest, releaseDigest, release, now)
}

func inspectStarted(ctx context.Context, options Options, deps dependencies, state *launchState, plan launchPlan, planDigest sessionmove.Digest, started launcherStarted) (Result, error) {
	attempt, err := state.openAttempt(started.AttemptID)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = attempt.Close() }()
	release, releaseDigest, err := attempt.loadRelease()
	if err != nil {
		return Result{}, err
	}
	if started.AttemptIndex != attempt.index || started.RequestDigest != plan.RequestDigest || started.PlanDigest != planDigest ||
		started.ReleaseDigest != releaseDigest || started.PID != release.PID {
		return Result{}, fmt.Errorf("immutable started marker does not match its selected launch attempt")
	}
	return inspectReleased(ctx, options, deps, attempt, plan, planDigest, release)
}

func verifyPinnedWorktree(ctx context.Context, plan launchPlan) error {
	mode := sessionauthority.LaunchRootMode(plan.RootMode)
	if mode == "" {
		mode = sessionauthority.LaunchRootPinnedClean
	}
	if mode == sessionauthority.LaunchRootParkedNeutral {
		want := filepath.Join(plan.StoreRoot, plan.HandoffID, sessionpark.LocalNeutralDirName)
		info, err := os.Lstat(plan.WorktreeDir)
		if err != nil || plan.WorktreeDir != want || !info.IsDir() || info.Mode().Perm() != 0o700 {
			return fmt.Errorf("parked-neutral successor root is not the exact private 0700 aggregate directory")
		}
		return nil
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("fixed git executable is unavailable for launch verification: %w", err)
	}
	gitPath, err = cleanAbsoluteExecutable(gitPath)
	if err != nil {
		return err
	}
	head, err := exec.CommandContext(ctx, gitPath, "-C", plan.WorktreeDir, "rev-parse", "--verify", "HEAD^{commit}").Output()
	if err != nil || string(bytes.TrimSpace(head)) != plan.PinnedCommit {
		return fmt.Errorf("pinned successor worktree HEAD no longer equals %s", plan.PinnedCommit)
	}
	pinnedBranch := plan.PinnedBranch
	if pinnedBranch == "" { // Read compatibility for already-written launch-plan schema v1 artifacts.
		pinnedBranch = "wb-session/" + plan.HandoffID
	}
	branch, err := exec.CommandContext(ctx, gitPath, "-C", plan.WorktreeDir, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err != nil || string(bytes.TrimSpace(branch)) != pinnedBranch {
		return fmt.Errorf("pinned successor worktree is not on its exact WB session branch")
	}
	status, err := exec.CommandContext(ctx, gitPath, "-C", plan.WorktreeDir, "status", "--porcelain=v1", "--untracked-files=all").Output()
	if err != nil {
		return fmt.Errorf("inspect pinned successor worktree status: %w", err)
	}
	if len(status) != 0 && mode != sessionauthority.LaunchRootParkedLocal {
		return fmt.Errorf("pinned successor worktree is dirty before harness release")
	}
	return nil
}

func inspectWithDependencies(ctx context.Context, options Options, deps dependencies, replay bool) (Result, error) {
	resolved, err := resolveAuthority(options)
	if err != nil {
		return Result{}, err
	}
	if resolved.fence == nil || !resolved.fence.HeldForSession(resolved.storeRoot, resolved.launch.AggregateID, resolved.launch.AggregateDigest) {
		return Result{}, fmt.Errorf("successor inspection requires the exact held handoff execution lock")
	}
	handoffAuthority, err := resolved.fence.RetainSessionDir(resolved.storeRoot, resolved.launch.AggregateID, resolved.launch.AggregateDigest)
	if err != nil {
		return Result{}, err
	}
	state, err := openLaunchStateFromHandoff(resolved.launch.AggregateID, handoffAuthority, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, ErrNotReleased
		}
		return Result{}, err
	}
	defer func() { _ = state.Close() }()
	plan, planDigest, err := state.loadPlan()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, ErrNotReleased
		}
		return Result{}, err
	}
	worktree, err := filepath.Abs(options.WorktreeDir)
	if err != nil || filepath.Clean(worktree) != worktree {
		return Result{}, fmt.Errorf("successor worktree must be a clean absolute path")
	}
	if err := validatePlanForOptions(plan, options, resolved, worktree); err != nil {
		return Result{}, err
	}
	started, err := state.loadStarted()
	if err == nil {
		result, inspectErr := inspectStarted(ctx, options, deps, state, plan, planDigest, started)
		result.Reused = replay
		return result, inspectErr
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	attempt, err := latestAttempt(state)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, ErrNotReleased
		}
		return Result{}, err
	}
	defer func() { _ = attempt.Close() }()
	release, releaseDigest, err := attempt.loadRelease()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, ErrNotReleased
		}
		return Result{}, err
	}
	result, err := inspectReleased(ctx, options, deps, attempt, plan, planDigest, release)
	if err != nil {
		if retryErr := failedAttemptRetryable(ctx, options, deps, state, attempt, plan, planDigest, release, releaseDigest); retryErr == nil {
			return Result{}, fmt.Errorf("%w: %w", ErrRetryableLaunch, err)
		}
		return result, err
	}
	_, _, err = finalizeStarted(state, attempt, plan, planDigest, release, deps.now())
	result.Reused = replay
	return result, err
}

func inspectReleased(ctx context.Context, options Options, deps dependencies, attempt *launchAttempt, plan launchPlan, planDigest sessionmove.Digest, release launcherRelease) (Result, error) {
	if _, err := attempt.loadAbandonment(); err == nil {
		return Result{}, fmt.Errorf("released launcher attempt has conflicting immutable abandonment evidence")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	ready, readyDigest, err := attempt.loadReady(release.PID)
	if err != nil {
		return Result{}, err
	}
	if release.RequestDigest != plan.RequestDigest || release.PlanDigest != planDigest || release.ReadyDigest != readyDigest ||
		ready.PlanDigest != planDigest || ready.RequestDigest != plan.RequestDigest {
		return Result{}, fmt.Errorf("immutable launcher release does not match its plan and ready artifact")
	}
	if err := waitExecSuccess(ctx, deps, attempt, plan, planDigest, ready, release); err != nil {
		return Result{}, err
	}
	pid, exists, err := deps.tmux.PanePID(ctx, plan.TmuxName)
	if err != nil {
		return Result{}, err
	}
	if !exists || pid != release.PID {
		return Result{}, fmt.Errorf("released tmux successor %s is not live at PID %d", plan.TmuxName, release.PID)
	}
	directory, err := deps.sessionDir(options.ProjectsRoot)
	if err != nil {
		return Result{}, err
	}
	record, live := session.Lookup(directory, pid)
	if !live || !sameReadySession(plan, ready, record) {
		return Result{}, fmt.Errorf("released successor PID %d has no matching live WB session registration", pid)
	}
	return resultFrom(attempt, plan, release, record), nil
}

func waitReady(ctx context.Context, options Options, deps dependencies, attempt *launchAttempt, plan launchPlan, planDigest sessionmove.Digest) (launcherReady, error) {
	startCtx, cancel := context.WithTimeout(ctx, deps.startTimeout)
	defer cancel()
	ticker := time.NewTicker(deps.pollInterval)
	defer ticker.Stop()
	for {
		pid, exists, err := deps.tmux.PanePID(startCtx, plan.TmuxName)
		if err != nil {
			return launcherReady{}, err
		}
		if exists {
			ready, _, readErr := attempt.loadReady(pid)
			if readErr == nil {
				directory, dirErr := deps.sessionDir(options.ProjectsRoot)
				if dirErr != nil {
					return launcherReady{}, dirErr
				}
				record, live := session.Lookup(directory, pid)
				if !live {
					return launcherReady{}, fmt.Errorf("launcher PID %d published ready without a live WB session", pid)
				}
				if ready.PlanDigest != planDigest || ready.RequestDigest != plan.RequestDigest || !sameReadySession(plan, ready, record) {
					return launcherReady{}, fmt.Errorf("launcher PID %d ready artifact conflicts with its WB session registration", pid)
				}
				held, fenceErr := attempt.execFenceHeld(pid)
				if fenceErr != nil {
					return launcherReady{}, fenceErr
				}
				if !held {
					return launcherReady{}, fmt.Errorf("launcher PID %d published ready without holding its exec-success fence", pid)
				}
				return ready, nil
			} else if !errors.Is(readErr, os.ErrNotExist) {
				return launcherReady{}, readErr
			}
		}
		select {
		case <-startCtx.Done():
			return launcherReady{}, fmt.Errorf("wait for registered tmux successor: %w", startCtx.Err())
		case <-ticker.C:
		}
	}
}

func waitExecSuccess(ctx context.Context, deps dependencies, attempt *launchAttempt, plan launchPlan, planDigest sessionmove.Digest, ready launcherReady, release launcherRelease) error {
	waitCtx, cancel := context.WithTimeout(ctx, deps.startTimeout)
	defer cancel()
	ticker := time.NewTicker(deps.pollInterval)
	defer ticker.Stop()
	for {
		held, err := attempt.execFenceHeld(release.PID)
		if err != nil {
			return err
		}
		if !held {
			failure, found, failureErr := attempt.loadExecFailure(release.PID)
			if failureErr != nil {
				return failureErr
			}
			if found {
				_, releaseDigest, digestErr := attempt.loadRelease()
				if digestErr != nil {
					return digestErr
				}
				if failure.RequestDigest != plan.RequestDigest || failure.PlanDigest != planDigest ||
					failure.ReadyDigest != release.ReadyDigest || failure.ReleaseDigest != releaseDigest {
					return fmt.Errorf("launcher exec-failure evidence conflicts with immutable release")
				}
				evidence := FailureEvidence{
					HandoffID: plan.HandoffID, RequestDigest: plan.RequestDigest,
					AttemptID: attempt.id, AttemptIndex: attempt.index, PID: release.PID,
					StartedAt: ready.Session.StartedAt, FailedAt: failure.FailedAt,
					TargetWorkLogReference: release.TargetWorkLogRef, Diagnostic: failure.Diagnostic,
				}
				evidence.authority = &failureEvidenceAuthority{
					HandoffID: evidence.HandoffID, RequestDigest: evidence.RequestDigest,
					AttemptID: evidence.AttemptID, AttemptIndex: evidence.AttemptIndex, PID: evidence.PID,
					StartedAt: evidence.StartedAt, FailedAt: evidence.FailedAt,
					TargetWorkLogReference: evidence.TargetWorkLogReference, Diagnostic: evidence.Diagnostic,
				}
				return &AttemptFailureError{Evidence: evidence}
			}
			terminal, terminalFound, terminalErr := deps.tmux.PaneFailure(waitCtx, plan.TmuxName)
			if terminalErr != nil {
				return terminalErr
			}
			if terminalFound {
				diagnostic := fmt.Sprintf("fixed %s harness exited with status %d", plan.Runtime, terminal.ExitStatus)
				if terminal.Diagnostic != "" {
					diagnostic += ": " + terminal.Diagnostic
				}
				_, releaseDigest, digestErr := attempt.loadRelease()
				if digestErr != nil {
					return digestErr
				}
				if err := attempt.saveExecFailure(plan, planDigest, release.ReadyDigest, releaseDigest, release.PID,
					errors.New(diagnostic), deps.now()); err != nil {
					return err
				}
				continue
			}
			pid, exists, paneErr := deps.tmux.PanePID(waitCtx, plan.TmuxName)
			if paneErr != nil {
				return paneErr
			}
			if !exists || pid != release.PID {
				return fmt.Errorf("successor launcher exited before a live harness could be proven")
			}
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("wait for successor harness Exec: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func sameReadySession(plan launchPlan, ready launcherReady, record session.Record) bool {
	readyRecord := ready.Session
	nativeCompatible := readyRecord.NativeHarnessID == record.NativeHarnessID || readyRecord.NativeHarnessID == "" && record.NativeHarnessID != ""
	legacyCompatible := readyRecord.AgentID == record.AgentID || readyRecord.AgentID == "" && record.AgentID != ""
	return ready.HandoffID == plan.HandoffID && ready.RequestDigest == plan.RequestDigest && ready.PID == record.PID &&
		readyRecord.PID == record.PID && readyRecord.WBSessionID == record.WBSessionID && readyRecord.Machine == record.Machine &&
		readyRecord.Runtime == record.Runtime && readyRecord.Model == record.Model && readyRecord.TmuxName == record.TmuxName &&
		readyRecord.PredecessorWBSessionID == record.PredecessorWBSessionID && readyRecord.HandoffID == record.HandoffID &&
		readyRecord.StartedAt.Equal(record.StartedAt) && nativeCompatible && legacyCompatible &&
		record.WBSessionID == plan.SuccessorWBSessionID && record.Machine == plan.Machine &&
		record.Runtime == plan.Runtime && record.Model == plan.Model && record.TmuxName == plan.TmuxName &&
		record.PredecessorWBSessionID == plan.PredecessorWBSessionID && record.HandoffID == plan.HandoffID
}

func resultFrom(attempt *launchAttempt, plan launchPlan, release launcherRelease, record session.Record) Result {
	return Result{HandoffID: plan.HandoffID, WBSessionID: record.WBSessionID,
		PredecessorWBSessionID: record.PredecessorWBSessionID, TargetMachine: record.Machine, PID: record.PID,
		AttemptID: attempt.id, AttemptIndex: attempt.index,
		TmuxName: record.TmuxName, Runtime: record.Runtime, Model: record.Model, NativeHarnessID: record.NativeHarnessID,
		TargetWorkLogRef: release.TargetWorkLogRef, WorktreeDir: plan.WorktreeDir,
		PinnedCommit: plan.PinnedCommit, StartedAt: record.StartedAt}
}

func cleanAbsoluteExecutable(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("executable path %q is not absolute", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect executable path %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("executable path %q is not one executable regular file", path)
	}
	return path, nil
}

func validatePlanForOptions(plan launchPlan, options Options, resolved resolvedAuthority, worktree string) error {
	authority := resolved.launch
	if plan.SchemaVersion != launchSchemaVersion || plan.HandoffID != authority.AggregateID || plan.RequestDigest != sessionmove.Digest(authority.AggregateDigest) ||
		plan.SuccessorWBSessionID != authority.SuccessorWBSessionID || plan.PredecessorWBSessionID != authority.PredecessorWBSessionID ||
		plan.Machine != authority.TargetMachine || plan.TmuxName != "wb-session-"+authority.SuccessorWBSessionID ||
		plan.StoreRoot != resolved.storeRoot || plan.WorktreeDir != worktree ||
		plan.PinnedCommit != options.PinnedCommit || plan.PinnedCommit != authority.PinnedCommit || plan.RootMode != string(authority.RootMode) ||
		plan.HandoverPath != authority.ContinuationPath {
		return fmt.Errorf("immutable launch plan conflicts with the admitted request or pinned worktree")
	}
	if options.Authority == nil {
		if plan.PinnedBranch != "" && plan.PinnedBranch != authority.PinnedBranch ||
			plan.AuthorityFile != "" && plan.AuthorityFile != authority.AggregateFile ||
			plan.ContinuationKind != "" && plan.ContinuationKind != string(authority.ContinuationKind) ||
			plan.ContinuationDigest != "" && plan.ContinuationDigest != sessionmove.Digest(authority.ContinuationDigest) {
			return fmt.Errorf("immutable legacy launch plan conflicts with admitted session-move authority")
		}
	} else if plan.PinnedBranch != authority.PinnedBranch || plan.AuthorityFile != authority.AggregateFile ||
		plan.ContinuationKind != string(authority.ContinuationKind) || plan.ContinuationDigest != sessionmove.Digest(authority.ContinuationDigest) {
		return fmt.Errorf("immutable launch plan conflicts with admitted parked-session authority")
	}
	spec, err := harnessSpecForAuthority(authority, worktree)
	if err != nil {
		return err
	}
	if plan.Runtime != spec.Runtime || plan.Model != spec.Model || filepath.Base(plan.HarnessExecutable) != spec.Executable ||
		!reflect.DeepEqual(plan.HarnessArgs, spec.Args) {
		return fmt.Errorf("immutable launch plan conflicts with fixed harness specification")
	}
	return nil
}

func validatePlanExecutables(plan launchPlan) error {
	if _, err := cleanAbsoluteExecutable(plan.WBExecutable); err != nil {
		return fmt.Errorf("immutable launch plan has invalid WB executable: %w", err)
	}
	if _, err := cleanAbsoluteExecutable(plan.HarnessExecutable); err != nil {
		return fmt.Errorf("immutable launch plan has invalid harness executable: %w", err)
	}
	return nil
}
