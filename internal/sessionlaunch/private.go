package sessionlaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

type privateLauncherDependencies struct {
	pid          func() int
	register     func(string, session.Record) (session.Record, error)
	sleep        func(time.Duration)
	exec         func(string, []string, []string) error
	verifyPinned func(context.Context, launchPlan) error
	now          func() time.Time
	wbExecutable func() (string, error)
}

func defaultPrivateLauncherDependencies() privateLauncherDependencies {
	return privateLauncherDependencies{pid: os.Getpid, register: session.Register, sleep: time.Sleep,
		exec: syscall.Exec, verifyPinned: verifyPinnedWorktree, now: func() time.Time { return time.Now().UTC() },
		wbExecutable: os.Executable}
}

// RunPrivateLauncher is the early-dispatch child started only through the
// receiver's fixed tmux argv. It registers the preallocated WB identity on its
// own PID, publishes readiness, waits for the receiver's immutable release,
// and then replaces itself with the fixed harness executable.
func RunPrivateLauncher(args []string) int {
	if err := runPrivateLauncher(args, defaultPrivateLauncherDependencies()); err != nil {
		return launcherError(err)
	}
	return 0
}

func runPrivateLauncher(args []string, deps privateLauncherDependencies) error {
	if len(args) != 4 || args[0] == "" || args[1] == "" || args[2] == "" || args[3] == "" {
		return fmt.Errorf("invalid arguments")
	}
	storeRoot, handoffID, attemptID, expectedPlanDigest := args[0], args[1], args[2], sessionmove.Digest(args[3])
	if !filepath.IsAbs(storeRoot) || filepath.Clean(storeRoot) != storeRoot || filepath.Base(storeRoot) != sessionmove.DirName {
		return fmt.Errorf("private launcher requires the exact clean absolute handoff store root")
	}
	store := sessionmove.NewStore(storeRoot)
	launchState, err := openLaunchState(storeRoot, handoffID, true)
	if err != nil {
		return err
	}
	defer launchState.Close()
	attempt, err := launchState.openAttempt(attemptID)
	if err != nil {
		return err
	}
	defer attempt.Close()
	state, err := store.Load(handoffID)
	if err != nil {
		return err
	}
	plan, planDigest, err := launchState.loadPlan()
	if err != nil {
		return err
	}
	if planDigest != expectedPlanDigest {
		return fmt.Errorf("private launcher plan digest %s does not match fixed tmux argv %s", planDigest, expectedPlanDigest)
	}
	if err := validatePrivatePlan(state, plan); err != nil {
		return err
	}
	if err := verifyLauncherWorktree(plan, state.Request); err != nil {
		return err
	}
	if plan.StoreRoot != storeRoot {
		return fmt.Errorf("private launcher store root does not match immutable launch plan")
	}
	if abandonment, abandonErr := attempt.loadAbandonment(); abandonErr == nil {
		if abandonment.HandoffID != plan.HandoffID || abandonment.AttemptID != attempt.id ||
			abandonment.AttemptIndex != attempt.index || abandonment.RequestDigest != plan.RequestDigest ||
			abandonment.PlanDigest != planDigest {
			return fmt.Errorf("private launcher found conflicting immutable abandonment evidence")
		}
		return fmt.Errorf("private launcher attempt %s was immutably abandoned before registration", attempt.id)
	} else if !errors.Is(abandonErr, os.ErrNotExist) {
		return abandonErr
	}
	if release, _, releaseErr := attempt.loadRelease(); releaseErr == nil {
		return fmt.Errorf("private launcher attempt %s was already immutably released for PID %d", attempt.id, release.PID)
	} else if !errors.Is(releaseErr, os.ErrNotExist) {
		return releaseErr
	}
	if evidencePID, found, evidenceErr := attempt.preReleaseProcessEvidence(); evidenceErr != nil {
		return evidenceErr
	} else if found {
		return fmt.Errorf("private launcher attempt %s already has process evidence for PID %d", attempt.id, evidencePID)
	}
	wbExecutable := deps.wbExecutable
	if wbExecutable == nil {
		wbExecutable = os.Executable
	}
	runningWB, err := wbExecutable()
	if err != nil {
		return fmt.Errorf("locate running private WB executable: %w", err)
	}
	runningInfo, runningErr := os.Stat(runningWB)
	plannedInfo, plannedErr := os.Stat(plan.WBExecutable)
	if runningErr != nil || plannedErr != nil || !os.SameFile(runningInfo, plannedInfo) {
		return fmt.Errorf("running private WB executable does not match immutable launch plan")
	}
	sessionDirectory := filepath.Join(filepath.Dir(storeRoot), session.DirName)
	launcherPID := deps.pid()
	views, err := session.List(sessionDirectory)
	if err != nil {
		return err
	}
	for _, view := range views {
		if view.State == session.StateLive && view.WBSessionID == plan.SuccessorWBSessionID && view.PID != launcherPID {
			return fmt.Errorf("successor WB session ID %s is already live at PID %d", plan.SuccessorWBSessionID, view.PID)
		}
	}
	execFence, err := attempt.acquireExecFence(launcherPID)
	if err != nil {
		return err
	}
	defer execFence.Close()
	record, err := deps.register(sessionDirectory, session.Record{
		PID: launcherPID, WBSessionID: plan.SuccessorWBSessionID, Machine: plan.Machine,
		Runtime: plan.Runtime, Model: plan.Model, TmuxName: plan.TmuxName,
		PredecessorWBSessionID: plan.PredecessorWBSessionID, HandoffID: plan.HandoffID,
	})
	if err != nil {
		return err
	}
	loaded, live := session.Lookup(sessionDirectory, record.PID)
	if !live || loaded.WBSessionID != plan.SuccessorWBSessionID || loaded.HandoffID != plan.HandoffID || loaded.TmuxName != plan.TmuxName {
		return fmt.Errorf("preallocated successor registration was not durably readable")
	}
	ready, err := attempt.saveReady(plan, planDigest, record)
	if err != nil {
		return err
	}
	_, readyDigest, err := attempt.loadReady(record.PID)
	if err != nil {
		return err
	}
	for {
		release, _, releaseErr := attempt.loadRelease()
		if releaseErr == nil {
			if release.PID != record.PID || release.RequestDigest != state.Digest || release.PlanDigest != planDigest ||
				release.ReadyDigest != readyDigest || ready.PlanDigest != planDigest {
				return fmt.Errorf("immutable release selects a different launcher")
			}
			break
		}
		if !errors.Is(releaseErr, os.ErrNotExist) {
			return releaseErr
		}
		deps.sleep(25 * time.Millisecond)
	}
	_, releaseDigest, err := attempt.loadRelease()
	if err != nil {
		return err
	}
	recordFailure := func(failure error) error {
		now := time.Now().UTC()
		if deps.now != nil {
			now = deps.now()
		}
		if saveErr := attempt.saveExecFailure(plan, planDigest, readyDigest, releaseDigest, record.PID, failure, now); saveErr != nil {
			return fmt.Errorf("%w; persist immutable launcher failure: %v", failure, saveErr)
		}
		return failure
	}
	if err := deps.verifyPinned(context.Background(), plan); err != nil {
		return recordFailure(err)
	}
	argv := append([]string{plan.HarnessExecutable}, plan.HarnessArgs...)
	if err := deps.exec(plan.HarnessExecutable, argv, console.Env()); err != nil {
		return recordFailure(fmt.Errorf("exec fixed %s harness: %w", plan.Runtime, err))
	}
	return nil
}

func validatePrivatePlan(state sessionmove.State, plan launchPlan) error {
	request := state.Request
	if plan.SchemaVersion != launchSchemaVersion || plan.HandoffID != request.HandoffID || plan.RequestDigest != state.Digest ||
		plan.SuccessorWBSessionID != request.SuccessorWBSessionID || plan.PredecessorWBSessionID != request.PredecessorWBSessionID ||
		plan.Machine != request.TargetMachine || plan.TmuxName != "wb-session-"+request.SuccessorWBSessionID ||
		plan.PinnedCommit != request.BundleCommit || plan.HandoverPath != request.HandoverPath ||
		plan.StoreRoot == "" {
		return fmt.Errorf("immutable launch plan does not match admitted handoff")
	}
	spec, err := harnessSpec(request, plan.WorktreeDir)
	if err != nil {
		return err
	}
	if plan.Runtime != spec.Runtime || plan.Model != spec.Model || filepath.Base(plan.HarnessExecutable) != spec.Executable ||
		!reflect.DeepEqual(plan.HarnessArgs, spec.Args) {
		return fmt.Errorf("immutable launch plan does not match fixed %s harness argv", spec.Runtime)
	}
	if !filepath.IsAbs(plan.WBExecutable) || !filepath.IsAbs(plan.HarnessExecutable) || !filepath.IsAbs(plan.WorktreeDir) ||
		!filepath.IsAbs(plan.StoreRoot) || filepath.Clean(plan.StoreRoot) != plan.StoreRoot {
		return fmt.Errorf("immutable launch plan contains a non-absolute target path")
	}
	if _, err := cleanAbsoluteExecutable(plan.WBExecutable); err != nil {
		return fmt.Errorf("validate fixed WB executable: %w", err)
	}
	if _, err := cleanAbsoluteExecutable(plan.HarnessExecutable); err != nil {
		return fmt.Errorf("validate fixed harness executable: %w", err)
	}
	return nil
}

func verifyLauncherWorktree(plan launchPlan, request sessionmove.Request) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	wantInfo, err := os.Stat(plan.WorktreeDir)
	if err != nil {
		return err
	}
	gotInfo, err := os.Stat(cwd)
	if err != nil || !os.SameFile(wantInfo, gotInfo) {
		return fmt.Errorf("private launcher is not rooted in the pinned target worktree")
	}
	handover, err := os.ReadFile(filepath.Join(plan.WorktreeDir, filepath.FromSlash(request.HandoverPath)))
	if err != nil {
		return fmt.Errorf("read pinned handover before harness exec: %w", err)
	}
	if !request.HandoverDigest.Matches(handover) {
		return fmt.Errorf("pinned handover digest changed before harness exec")
	}
	return nil
}

func launcherError(err error) int {
	_, _ = fmt.Fprintln(os.Stderr, "wb private session launcher:", err)
	return 1
}
