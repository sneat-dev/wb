package sessionlaunch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"golang.org/x/sys/unix"
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
	storeKind := filepath.Base(storeRoot)
	if !filepath.IsAbs(storeRoot) || filepath.Clean(storeRoot) != storeRoot ||
		storeKind != sessionmove.DirName && storeKind != sessionpark.TargetDirName {
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
	plan, planDigest, err := launchState.loadPlan()
	if err != nil {
		return err
	}
	if planDigest != expectedPlanDigest {
		return fmt.Errorf("private launcher plan digest %s does not match fixed tmux argv %s", planDigest, expectedPlanDigest)
	}
	privateContinuation := ""
	if storeKind == sessionmove.DirName {
		state, err := store.Load(handoffID)
		if err != nil {
			return err
		}
		if err := validatePrivatePlan(state, plan); err != nil {
			return err
		}
		if err := verifyLauncherWorktree(plan, state.Request); err != nil {
			return err
		}
	} else {
		privateContinuation, err = validatePrivateParkPlan(launchState, plan)
		if err != nil {
			return err
		}
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
			if release.PID != record.PID || release.RequestDigest != plan.RequestDigest || release.PlanDigest != planDigest ||
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
	environment := console.Env()
	if privateContinuation != "" {
		environment = append(environment, sessionauthority.ContinuationEnvironment+"="+privateContinuation)
	}
	if err := deps.exec(plan.HarnessExecutable, argv, environment); err != nil {
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
	if plan.PinnedBranch != "" && plan.PinnedBranch != "wb-session/"+request.HandoffID ||
		plan.AuthorityFile != "" && plan.AuthorityFile != "request.json" ||
		plan.ContinuationKind != "" && plan.ContinuationKind != string(sessionauthority.ContinuationTracked) ||
		plan.ContinuationDigest != "" && plan.ContinuationDigest != request.HandoverDigest {
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

// validatePrivateParkPlan is intentionally separate from the unchanged
// session-move validator above. It fully decodes the exact 0600 admitted
// sessionpark envelope through a descriptor retained by launchState, derives
// the expected launch authority from that envelope, and then compares every
// plan field before the private continuation path can enter the environment.
func validatePrivateParkPlan(state *launchState, plan launchPlan) (string, error) {
	if plan.AuthorityFile != sessionpark.EnvelopeFileName ||
		sessionauthority.ContinuationKind(plan.ContinuationKind) != sessionauthority.ContinuationPrivate {
		return "", fmt.Errorf("parked launcher plan does not name the fixed private authority artifacts")
	}
	raw, err := readPrivateArtifactAt(state.handoff, sessionpark.EnvelopeFileName, sessionpark.MaxEnvelopeBytes)
	if err != nil {
		return "", fmt.Errorf("read admitted parked-session envelope: %w", err)
	}
	if !plan.RequestDigest.Matches(raw) {
		return "", fmt.Errorf("admitted parked-session envelope digest changed")
	}
	envelope, err := sessionpark.DecodeEnvelope(raw)
	if err != nil {
		return "", err
	}
	canonical, err := sessionpark.EncodeEnvelope(envelope)
	if err != nil || !bytes.Equal(canonical, raw) {
		return "", fmt.Errorf("admitted parked-session envelope is not canonical")
	}
	continuationPath := filepath.Join(plan.StoreRoot, plan.HandoffID, sessionpark.SuccessorContextFileName)
	continuation, err := readPrivateArtifactAt(state.handoff, sessionpark.SuccessorContextFileName, sessionpark.MaxSuccessorContextBytes)
	if err != nil {
		return "", fmt.Errorf("read private parked successor context: %w", err)
	}
	authority, err := sessionpark.LaunchAuthority(envelope.Request, plan.RequestDigest, continuationPath, continuation)
	if err != nil {
		return "", err
	}
	if plan.SchemaVersion != launchSchemaVersion || plan.HandoffID != authority.AggregateID ||
		plan.SuccessorWBSessionID != authority.SuccessorWBSessionID || plan.PredecessorWBSessionID != authority.PredecessorWBSessionID ||
		plan.Machine != authority.TargetMachine || plan.TmuxName != "wb-session-"+authority.SuccessorWBSessionID ||
		plan.PinnedCommit != authority.PinnedCommit || plan.PinnedBranch != authority.PinnedBranch ||
		plan.HandoverPath != authority.ContinuationPath || plan.ContinuationDigest != sessionmove.Digest(authority.ContinuationDigest) ||
		plan.StoreRoot == "" || filepath.Base(plan.StoreRoot) != sessionpark.TargetDirName {
		return "", fmt.Errorf("immutable launch plan does not match admitted parked-session envelope")
	}
	spec, err := harnessSpecForAuthority(authority, plan.WorktreeDir)
	if err != nil {
		return "", err
	}
	if plan.Runtime != spec.Runtime || plan.Model != spec.Model || filepath.Base(plan.HarnessExecutable) != spec.Executable ||
		!reflect.DeepEqual(plan.HarnessArgs, spec.Args) {
		return "", fmt.Errorf("immutable parked launch plan does not match fixed %s harness argv", spec.Runtime)
	}
	for _, argument := range plan.HarnessArgs {
		if strings.Contains(argument, continuationPath) || strings.Contains(argument, envelope.Request.Continuation) {
			return "", fmt.Errorf("private continuation data must not appear in harness argv")
		}
	}
	if !filepath.IsAbs(plan.WBExecutable) || !filepath.IsAbs(plan.HarnessExecutable) || !filepath.IsAbs(plan.WorktreeDir) ||
		!filepath.IsAbs(plan.StoreRoot) || filepath.Clean(plan.StoreRoot) != plan.StoreRoot {
		return "", fmt.Errorf("immutable parked launch plan contains a non-absolute target path")
	}
	if _, err := cleanAbsoluteExecutable(plan.WBExecutable); err != nil {
		return "", fmt.Errorf("validate fixed WB executable: %w", err)
	}
	if _, err := cleanAbsoluteExecutable(plan.HarnessExecutable); err != nil {
		return "", fmt.Errorf("validate fixed harness executable: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	wantInfo, err := os.Stat(plan.WorktreeDir)
	if err != nil {
		return "", err
	}
	gotInfo, err := os.Stat(cwd)
	if err != nil || !os.SameFile(wantInfo, gotInfo) {
		return "", fmt.Errorf("private launcher is not rooted in the pinned target worktree")
	}
	if !plan.ContinuationDigest.Matches(continuation) || !bytes.HasPrefix(continuation, []byte(envelope.Request.Continuation)) {
		return "", fmt.Errorf("private parked continuation conflicts with admitted envelope")
	}
	return continuationPath, nil
}

func readPrivateArtifactAt(directory *os.File, name string, maximum int) ([]byte, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 ||
		os.FileMode(stat.Mode).Perm() != 0o600 || stat.Size < 0 || stat.Size > int64(maximum) {
		return nil, fmt.Errorf("private session artifact is not one bounded 0600 regular file")
	}
	raw := make([]byte, int(stat.Size))
	read, err := file.Read(raw)
	if err != nil || read != len(raw) {
		return nil, fmt.Errorf("private session artifact changed while being read")
	}
	return bytes.Clone(raw), nil
}

func launcherError(err error) int {
	_, _ = fmt.Fprintln(os.Stderr, "wb private session launcher:", err)
	return 1
}
