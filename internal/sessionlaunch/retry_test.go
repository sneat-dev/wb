package sessionlaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"golang.org/x/sys/unix"
)

type launcherRetryFixture struct {
	root       string
	store      sessionmove.Store
	request    sessionmove.Request
	digest     sessionmove.Digest
	lock       *sessionmove.ExecutionLock
	worktree   string
	sessions   string
	bin        string
	plan       launchPlan
	planDigest sessionmove.Digest
	tmux       *fakeTmux
	deps       dependencies
}

func newLauncherRetryFixture(t *testing.T) *launcherRetryFixture {
	t.Helper()
	root := t.TempDir()
	store := sessionmove.NewStore(filepath.Join(root, "handoffs"))
	request := completeLaunchTestRequest(t)
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	worktree := filepath.Join(root, "worktree")
	if err := os.MkdirAll(filepath.Join(worktree, ".wb", "handoffs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, filepath.FromSlash(request.HandoverPath)), []byte("handover\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "wb"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	spec, err := harnessSpec(request, worktree)
	if err != nil {
		t.Fatal(err)
	}
	plan := launchPlan{
		SchemaVersion: launchSchemaVersion, HandoffID: request.HandoffID, RequestDigest: digest,
		SuccessorWBSessionID: request.SuccessorWBSessionID, PredecessorWBSessionID: request.PredecessorWBSessionID,
		Machine: request.TargetMachine, TmuxName: "wb-session-" + request.SuccessorWBSessionID,
		Runtime: spec.Runtime, Model: spec.Model, StoreRoot: store.Root, WorktreeDir: worktree,
		PinnedCommit: request.BundleCommit, HandoverPath: request.HandoverPath,
		WBExecutable: filepath.Join(bin, "wb"), HarnessExecutable: filepath.Join(bin, spec.Executable), HarnessArgs: spec.Args,
	}
	state, err := openLaunchState(store.Root, request.HandoffID, true)
	if err != nil {
		t.Fatal(err)
	}
	_, planDigest, _, err := state.savePlan(plan)
	_ = state.Close()
	if err != nil {
		t.Fatal(err)
	}
	tmux := &fakeTmux{}
	fixture := &launcherRetryFixture{
		root: root, store: store, request: request, digest: digest, lock: lock,
		worktree: worktree, sessions: filepath.Join(root, "sessions"), bin: bin,
		plan: plan, planDigest: planDigest, tmux: tmux,
	}
	fixture.deps = dependencies{
		tmux: tmux,
		lookPath: func(name string) (string, error) {
			return filepath.Join(bin, name), nil
		},
		wbExecutable:  func() (string, error) { return filepath.Join(bin, "wb"), nil },
		sessionDir:    func(string) (string, error) { return fixture.sessions, nil },
		now:           func() time.Time { return time.Date(2026, time.August, 25, 18, 0, 0, 0, time.UTC) },
		pollInterval:  time.Millisecond,
		startTimeout:  100 * time.Millisecond,
		verifyPinned:  func(context.Context, launchPlan) error { return nil },
		processStatus: func(int) error { return syscall.ESRCH },
	}
	return fixture
}

func (fixture *launcherRetryFixture) options(before BeforeRelease) Options {
	return Options{
		Store: fixture.store, ProjectsRoot: fixture.root, Request: fixture.request, RequestDigest: fixture.digest,
		WorktreeDir: fixture.worktree, PinnedCommit: fixture.request.BundleCommit, ExecutionLock: fixture.lock,
		BeforeRelease: before,
	}
}

func (fixture *launcherRetryFixture) createReleasedAttempt(t *testing.T, withFailure, keepFenceHeld bool) (*os.File, int) {
	t.Helper()
	state, err := openLaunchState(fixture.store.Root, fixture.request.HandoffID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	attempt, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attempt.Close() }()
	const pid = 919191
	fence, err := attempt.acquireExecFence(pid)
	if err != nil {
		t.Fatal(err)
	}
	record := session.Record{
		PID: pid, WBSessionID: fixture.plan.SuccessorWBSessionID, Machine: fixture.plan.Machine,
		Runtime: fixture.plan.Runtime, Model: fixture.plan.Model, TmuxName: fixture.plan.TmuxName,
		PredecessorWBSessionID: fixture.plan.PredecessorWBSessionID, HandoffID: fixture.plan.HandoffID,
		StartedAt: time.Date(2026, time.August, 25, 17, 0, 0, 0, time.UTC),
	}
	ready, err := attempt.saveReady(fixture.plan, fixture.planDigest, record)
	if err != nil {
		t.Fatal(err)
	}
	release, _, err := attempt.saveRelease(fixture.plan, fixture.planDigest, ready, "worklog-attempt-1", record.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	_, releaseDigest, err := attempt.loadRelease()
	if err != nil {
		t.Fatal(err)
	}
	if withFailure {
		if err := attempt.saveExecFailure(fixture.plan, fixture.planDigest, release.ReadyDigest, releaseDigest, pid,
			errors.New("injected exact Exec failure"), record.StartedAt.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if !keepFenceHeld {
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		return nil, pid
	}
	t.Cleanup(func() { _ = fence.Close() })
	return fence, pid
}

func (fixture *launcherRetryFixture) configureSuccessfulStart(t *testing.T) (BeforeRelease, <-chan error) {
	t.Helper()
	var fence *os.File
	var attemptID string
	pid := os.Getpid()
	fixture.tmux.onStart = func() {
		fixture.tmux.pid = pid
		state, err := openLaunchState(fixture.store.Root, fixture.request.HandoffID, false)
		if err != nil {
			t.Fatal(err)
		}
		attempt, err := latestAttempt(state)
		if err != nil {
			t.Fatal(err)
		}
		attemptID = attempt.id
		record, err := session.Register(fixture.sessions, session.Record{
			PID: pid, WBSessionID: fixture.plan.SuccessorWBSessionID, Machine: fixture.plan.Machine,
			Runtime: fixture.plan.Runtime, Model: fixture.plan.Model, TmuxName: fixture.plan.TmuxName,
			PredecessorWBSessionID: fixture.plan.PredecessorWBSessionID, HandoffID: fixture.plan.HandoffID,
			StartedAt: time.Date(2026, time.August, 25, 18, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatal(err)
		}
		fence, err = attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := attempt.saveReady(fixture.plan, fixture.planDigest, record); err != nil {
			t.Fatal(err)
		}
		_ = attempt.Close()
		_ = state.Close()
	}
	released := make(chan error, 1)
	before := func(context.Context, Prepared) (string, error) {
		go func() {
			for {
				path := filepath.Join(fixture.store.Root, fixture.request.HandoffID, launchDirectoryName,
					attemptsDirectoryName, attemptID, "release.json")
				if _, err := os.Stat(path); err == nil {
					released <- fence.Close()
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
		return "worklog-attempt-2", nil
	}
	return before, released
}

func TestReleasedExactFailureRetriesOneNewAttempt(t *testing.T) {
	fixture := newLauncherRetryFixture(t)
	fixture.createReleasedAttempt(t, true, false)

	_, inspectErr := inspectWithDependencies(context.Background(), fixture.options(nil), fixture.deps, true)
	if !errors.Is(inspectErr, ErrRetryableLaunch) {
		t.Fatalf("Inspect error = %v, want ErrRetryableLaunch", inspectErr)
	}
	var attemptFailure *AttemptFailureError
	if !errors.As(inspectErr, &attemptFailure) {
		t.Fatalf("Inspect error = %v, want typed AttemptFailureError", inspectErr)
	}
	evidence := attemptFailure.Evidence
	if !evidence.Authenticates(fixture.request.HandoffID, fixture.digest, "worklog-attempt-1") ||
		evidence.AttemptIndex != 1 || evidence.PID != 919191 || evidence.StartedAt.IsZero() || evidence.FailedAt.IsZero() {
		t.Fatalf("typed immutable failure evidence = %#v", evidence)
	}
	mutations := map[string]func(*FailureEvidence){
		"handoff": func(value *FailureEvidence) { value.HandoffID = "other" },
		"digest":  func(value *FailureEvidence) { value.RequestDigest = sessionmove.DigestBytes([]byte("other")) },
		"attempt": func(value *FailureEvidence) { value.AttemptID = "000002-" + strings.Repeat("2", 32) },
		"index":   func(value *FailureEvidence) { value.AttemptIndex++ },
		"pid":     func(value *FailureEvidence) { value.PID++ },
		"started": func(value *FailureEvidence) { value.StartedAt = value.StartedAt.Add(time.Second) },
		"failed":  func(value *FailureEvidence) { value.FailedAt = value.FailedAt.Add(time.Second) },
		"worklog": func(value *FailureEvidence) { value.TargetWorkLogReference = "other" },
		"diagnostic": func(value *FailureEvidence) {
			value.Diagnostic = "substituted"
		},
	}
	for name, mutate := range mutations {
		copy := evidence
		mutate(&copy)
		if copy.Authenticates(fixture.request.HandoffID, fixture.digest, "worklog-attempt-1") {
			t.Fatalf("mutated %s failure evidence retained authentication", name)
		}
	}

	if _, err := inspectWithDependencies(context.Background(), fixture.options(nil), fixture.deps, true); !errors.Is(err, ErrRetryableLaunch) {
		t.Fatalf("Inspect error = %v, want ErrRetryableLaunch", err)
	}
	before, released := fixture.configureSuccessfulStart(t)
	result, err := startWithDependencies(context.Background(), fixture.options(before), fixture.deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if fixture.tmux.starts != 1 || result.AttemptIndex != 2 || result.AttemptID == "" || !result.Reused {
		t.Fatalf("result=%#v starts=%d", result, fixture.tmux.starts)
	}
	state, err := openLaunchState(fixture.store.Root, fixture.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	attempts, err := state.listAttempts()
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts=%#v error=%v", attempts, err)
	}
}

func TestClaimedAttemptCrashCompletesSameAttemptWithoutSecondClaim(t *testing.T) {
	fixture := newLauncherRetryFixture(t)
	state, err := openLaunchState(fixture.store.Root, fixture.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	const claimedID = "000001-00000000000000000000000000000001"
	if err := unix.Mkdirat(int(state.attempts.Fd()), claimedID, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := state.attempts.Sync(); err != nil {
		t.Fatal(err)
	}
	_ = state.Close() // crash after final attempt root, before fixed children

	before, released := fixture.configureSuccessfulStart(t)
	result, err := startWithDependencies(context.Background(), fixture.options(before), fixture.deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if result.AttemptID != claimedID || result.AttemptIndex != 1 || fixture.tmux.starts != 1 {
		t.Fatalf("result=%#v starts=%d", result, fixture.tmux.starts)
	}
}

func TestDuplicateTmuxStartAdoptsSameAttemptWithoutReplacement(t *testing.T) {
	fixture := newLauncherRetryFixture(t)
	fixture.tmux.startErr = errors.New("duplicate session")
	before, released := fixture.configureSuccessfulStart(t)
	result, err := startWithDependencies(context.Background(), fixture.options(before), fixture.deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if result.AttemptIndex != 1 || fixture.tmux.starts != 1 {
		t.Fatalf("result=%#v starts=%d", result, fixture.tmux.starts)
	}
	state, err := openLaunchState(fixture.store.Root, fixture.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	refs, err := state.listAttempts()
	if err != nil || len(refs) != 1 {
		t.Fatalf("attempts=%#v error=%v", refs, err)
	}
}

func TestReleasedAttemptReplacementFailsClosedWithoutExactTerminalProof(t *testing.T) {
	tests := []struct {
		name          string
		withFailure   bool
		holdFence     bool
		processAlive  bool
		processError  error
		tmuxStillLive bool
		want          string
	}{
		{name: "missing failure", want: "no exact immutable terminal failure"},
		{name: "held fence", withFailure: true, holdFence: true, want: "still holds its exec fence"},
		{name: "live PID", withFailure: true, processAlive: true, want: "still live or ambiguous"},
		{name: "ambiguous PID probe", withFailure: true, processError: syscall.EINVAL, want: "probe is ambiguous"},
		{name: "existing tmux", withFailure: true, tmuxStillLive: true, want: "still exists"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLauncherRetryFixture(t)
			_, pid := fixture.createReleasedAttempt(t, test.withFailure, test.holdFence)
			fixture.deps.startTimeout = 10 * time.Millisecond
			fixture.deps.processStatus = func(int) error {
				if test.processAlive {
					return nil
				}
				if test.processError != nil {
					return test.processError
				}
				return syscall.ESRCH
			}
			if test.tmuxStillLive {
				fixture.tmux.pid = pid
			}
			_, err := startWithDependencies(context.Background(), fixture.options(nil), fixture.deps)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Start error = %v, want %q", err, test.want)
			}
			if fixture.tmux.starts != 0 {
				t.Fatalf("StartDetached calls = %d, want 0", fixture.tmux.starts)
			}
			state, openErr := openLaunchState(fixture.store.Root, fixture.request.HandoffID, false)
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer func() { _ = state.Close() }()
			attempts, listErr := state.listAttempts()
			if listErr != nil || len(attempts) != 1 {
				t.Fatalf("attempts=%#v error=%v", attempts, listErr)
			}
		})
	}
}

func TestDeadPreReleaseWrapperIsSealedThenRetriesOneNewAttempt(t *testing.T) {
	fixture := newLauncherRetryFixture(t)
	state, err := openLaunchState(fixture.store.Root, fixture.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	const pid = 929292
	fence, err := attempt.acquireExecFence(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	_ = attempt.Close()
	_ = state.Close()

	before, released := fixture.configureSuccessfulStart(t)
	result, err := startWithDependencies(context.Background(), fixture.options(before), fixture.deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if fixture.tmux.starts != 1 || result.AttemptIndex != 2 {
		t.Fatalf("result=%#v StartDetached calls=%d, want attempt 2/one start", result, fixture.tmux.starts)
	}
	state, err = openLaunchState(fixture.store.Root, fixture.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	attempts, err := state.listAttempts()
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts=%#v error=%v", attempts, err)
	}
	first, err := state.openAttempt(attempts[0].id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	abandonment, err := first.loadAbandonment()
	if err != nil || abandonment.PID != pid || abandonment.PlanDigest != fixture.planDigest || abandonment.ReadyDigest != "" {
		t.Fatalf("abandonment=%#v error=%v", abandonment, err)
	}
}

func TestPreReleaseAbandonmentRefusesAmbiguousState(t *testing.T) {
	tests := []struct {
		name         string
		holdFence    bool
		processError error
		liveProcess  bool
		tmuxLive     bool
		unknownEntry bool
	}{
		{name: "held fence", holdFence: true, processError: syscall.ESRCH},
		{name: "live PID"},
		{name: "permission ambiguous PID", processError: syscall.EPERM},
		{name: "unexpected PID probe", processError: syscall.EINVAL},
		{name: "existing exact tmux", processError: syscall.ESRCH, tmuxLive: true},
		{name: "ambiguous process artifacts", processError: syscall.ESRCH, unknownEntry: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLauncherRetryFixture(t)
			state, err := openLaunchState(fixture.store.Root, fixture.request.HandoffID, false)
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := state.createAttempt()
			if err != nil {
				t.Fatal(err)
			}
			const pid = 959595
			fence, err := attempt.acquireExecFence(pid)
			if err != nil {
				t.Fatal(err)
			}
			if test.holdFence {
				t.Cleanup(func() { _ = fence.Close() })
			} else if err := fence.Close(); err != nil {
				t.Fatal(err)
			}
			if test.unknownEntry {
				if created, err := attempt.publish(execDirectoryName, "unexpected", []byte("{}\n")); err != nil || !created {
					t.Fatalf("inject ambiguous artifact created=%t error=%v", created, err)
				}
			}
			_ = attempt.Close()
			_ = state.Close()
			fixture.deps.startTimeout = 10 * time.Millisecond
			fixture.deps.processStatus = func(int) error {
				if test.liveProcess {
					return nil
				}
				return test.processError
			}
			if test.tmuxLive {
				fixture.tmux.pid = pid
			}
			if _, err := startWithDependencies(context.Background(), fixture.options(nil), fixture.deps); err == nil {
				t.Fatal("ambiguous pre-release state authorized a replacement")
			}
			if fixture.tmux.starts != 0 {
				t.Fatalf("StartDetached calls = %d, want 0", fixture.tmux.starts)
			}
			state, err = openLaunchState(fixture.store.Root, fixture.request.HandoffID, false)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = state.Close() }()
			refs, err := state.listAttempts()
			if err != nil || len(refs) != 1 {
				t.Fatalf("attempts=%#v error=%v", refs, err)
			}
			attempt, err = state.openAttempt(refs[0].id)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = attempt.Close() }()
			if _, err := attempt.loadAbandonment(); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("abandonment artifact error = %v, want absent", err)
			}
		})
	}
}

func TestAbandonmentPublicationCrashReplaysExactlyOnce(t *testing.T) {
	fixture := newLauncherRetryFixture(t)
	state, err := openLaunchState(fixture.store.Root, fixture.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	const pid = 939393
	fence, err := first.acquireExecFence(pid)
	if err != nil {
		t.Fatal(err)
	}
	record := session.Record{
		PID: pid, WBSessionID: fixture.plan.SuccessorWBSessionID, Machine: fixture.plan.Machine,
		Runtime: fixture.plan.Runtime, Model: fixture.plan.Model, TmuxName: fixture.plan.TmuxName,
		PredecessorWBSessionID: fixture.plan.PredecessorWBSessionID, HandoffID: fixture.plan.HandoffID,
		StartedAt: fixture.deps.now(),
	}
	if _, err := first.saveReady(fixture.plan, fixture.planDigest, record); err != nil {
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	abandonment, replay, err := first.saveAbandonment(fixture.plan, fixture.planDigest, pid, fixture.deps.now())
	if err != nil || replay || abandonment.ReadyDigest == "" {
		t.Fatalf("save abandonment replay=%t error=%v", replay, err)
	}
	_ = first.Close()
	_ = state.Close() // crash window: marker durable, attempt 2 not claimed

	before, released := fixture.configureSuccessfulStart(t)
	result, err := startWithDependencies(context.Background(), fixture.options(before), fixture.deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if result.AttemptIndex != 2 || fixture.tmux.starts != 1 {
		t.Fatalf("result=%#v starts=%d", result, fixture.tmux.starts)
	}
	result, err = startWithDependencies(context.Background(), fixture.options(before), fixture.deps)
	if err != nil {
		t.Fatal(err)
	}
	if result.AttemptIndex != 2 || fixture.tmux.starts != 1 || !result.Reused {
		t.Fatalf("replay result=%#v starts=%d", result, fixture.tmux.starts)
	}
}

func TestDelayedPrivateLauncherCannotRegisterAfterItsAttemptWasAbandoned(t *testing.T) {
	fixture := newLauncherRetryFixture(t)
	state, err := openLaunchState(fixture.store.Root, fixture.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	firstID := first.id
	const oldPID = 949494
	fence, err := first.acquireExecFence(oldPID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.saveAbandonment(fixture.plan, fixture.planDigest, oldPID, fixture.deps.now()); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	second, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
	_ = state.Close()

	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(fixture.worktree); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(previous) }()
	registered := false
	err = runPrivateLauncher([]string{fixture.store.Root, fixture.request.HandoffID, firstID, string(fixture.planDigest)}, privateLauncherDependencies{
		pid: func() int { return os.Getpid() },
		register: func(string, session.Record) (session.Record, error) {
			registered = true
			return session.Record{}, errors.New("must not register")
		},
		wbExecutable: func() (string, error) { return fixture.plan.WBExecutable, nil },
		verifyPinned: func(context.Context, launchPlan) error { return nil },
		sleep:        func(time.Duration) { t.Fatal("abandoned private launcher waited for release") },
		exec:         func(string, []string, []string) error { return errors.New("must not exec") },
	})
	if err == nil || !strings.Contains(err.Error(), "immutably abandoned") {
		t.Fatalf("private launcher error = %v", err)
	}
	if registered {
		t.Fatal("delayed old private launcher registered after abandonment")
	}
}

func TestReleasedAndStartedAttemptsRejectContradictoryAbandonment(t *testing.T) {
	for _, withStarted := range []bool{false, true} {
		name := "released"
		if withStarted {
			name = "started"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newLauncherRetryFixture(t)
			fixture.createReleasedAttempt(t, false, false)
			state, err := openLaunchState(fixture.store.Root, fixture.request.HandoffID, false)
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := latestAttempt(state)
			if err != nil {
				t.Fatal(err)
			}
			release, releaseDigest, err := attempt.loadRelease()
			if err != nil {
				t.Fatal(err)
			}
			if withStarted {
				if _, _, err := state.saveStarted(attempt, fixture.plan, fixture.planDigest, releaseDigest, release, fixture.deps.now()); err != nil {
					t.Fatal(err)
				}
			}
			abandonment := launcherAbandonment{
				SchemaVersion: launchSchemaVersion, HandoffID: fixture.request.HandoffID,
				AttemptID: attempt.id, AttemptIndex: attempt.index, RequestDigest: fixture.digest,
				PlanDigest: fixture.planDigest, ReadyDigest: release.ReadyDigest, PID: release.PID,
				AbandonedAt: fixture.deps.now(),
			}
			raw, err := encodeLaunchJSON(abandonment)
			if err != nil {
				t.Fatal(err)
			}
			if created, err := attempt.publish("", "abandoned.json", raw); err != nil || !created {
				t.Fatalf("inject abandonment created=%t error=%v", created, err)
			}
			_ = attempt.Close()
			_ = state.Close()

			_, err = inspectWithDependencies(context.Background(), fixture.options(nil), fixture.deps, true)
			if err == nil || !strings.Contains(err.Error(), "abandonment") {
				t.Fatalf("Inspect error = %v, want same-attempt abandonment conflict", err)
			}
		})
	}
}
