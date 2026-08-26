package sessionlaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

type fakeTmux struct {
	starts   int
	name     string
	cwd      string
	cmd      string
	args     []string
	pid      int
	startErr error
	onStart  func()
	onPane   func()
}

func (f *fakeTmux) StartDetached(_ context.Context, name, cwd, command string, args []string) error {
	f.starts++
	f.name, f.cwd, f.cmd, f.args = name, cwd, command, append([]string(nil), args...)
	if f.onStart != nil {
		f.onStart()
	}
	return f.startErr
}

func (f *fakeTmux) PanePID(context.Context, string) (int, bool, error) {
	if f.onPane != nil {
		f.onPane()
	}
	return f.pid, f.pid > 0, nil
}

func TestStartRegistersReadyBeforeReleaseAndReplaysWithoutRelaunch(t *testing.T) {
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
	defer func() { _ = lock.Close() }()

	worktree := filepath.Join(root, "worktree")
	if err := os.MkdirAll(filepath.Join(worktree, ".wb", "handoffs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, filepath.FromSlash(request.HandoverPath)), []byte("handover\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(root, "sessions")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "wb"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tmux := &fakeTmux{}
	var execFence *os.File
	var attemptID string
	launcherPID := os.Getpid()
	now := time.Date(2026, time.August, 25, 15, 0, 0, 0, time.UTC)
	deps := dependencies{
		tmux:         tmux,
		lookPath:     func(name string) (string, error) { return filepath.Join(bin, name), nil },
		wbExecutable: func() (string, error) { return filepath.Join(bin, "wb"), nil },
		sessionDir:   func(string) (string, error) { return sessions, nil },
		now:          func() time.Time { return now }, pollInterval: time.Millisecond, startTimeout: time.Second,
		verifyPinned: func(context.Context, launchPlan) error { return nil },
	}
	tmux.onStart = func() {
		tmux.pid = launcherPID
		plan, planDigest, loadErr := loadPlan(store.Root, request.HandoffID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		record, registerErr := session.Register(sessions, session.Record{
			PID: tmux.pid, WBSessionID: request.SuccessorWBSessionID, Machine: request.TargetMachine,
			Runtime: plan.Runtime, Model: plan.Model, TmuxName: plan.TmuxName,
			PredecessorWBSessionID: request.PredecessorWBSessionID, HandoffID: request.HandoffID, StartedAt: now,
		})
		if registerErr != nil {
			t.Fatal(registerErr)
		}
		state, stateErr := openLaunchState(store.Root, request.HandoffID, true)
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		attempt, attemptErr := latestAttempt(state)
		if attemptErr != nil {
			t.Fatal(attemptErr)
		}
		attemptID = attempt.id
		execFence, stateErr = attempt.acquireExecFence(tmux.pid)
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		if _, readyErr := attempt.saveReady(plan, planDigest, record); readyErr != nil {
			t.Fatal(readyErr)
		}
		_ = attempt.Close()
		_ = state.Close()
	}
	execReleased := make(chan error, 1)
	beforeCalls := 0
	options := Options{
		Store: store, ProjectsRoot: root, Request: request, RequestDigest: digest,
		WorktreeDir: worktree, PinnedCommit: request.BundleCommit, ExecutionLock: lock,
		BeforeRelease: func(_ context.Context, prepared Prepared) (string, error) {
			beforeCalls++
			if _, _, err := loadRelease(store.Root, request.HandoffID); err == nil {
				t.Fatal("release existed before BeforeRelease")
			}
			if prepared.Session.PID != launcherPID || prepared.Session.WBSessionID != request.SuccessorWBSessionID ||
				prepared.AttemptID != attemptID || prepared.AttemptIndex != 1 {
				t.Fatalf("prepared = %#v", prepared)
			}
			go func(fence *os.File) {
				for {
					if _, statErr := os.Stat(filepath.Join(store.Root, request.HandoffID, launchDirectoryName,
						attemptsDirectoryName, attemptID, "release.json")); statErr == nil {
						execReleased <- fence.Close()
						return
					}
					time.Sleep(time.Millisecond)
				}
			}(execFence)
			return "worklog-target", nil
		},
	}
	first, err := startWithDependencies(context.Background(), options, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-execReleased; err != nil {
		t.Fatal(err)
	}
	if first.PID != launcherPID || first.TmuxName != "wb-session-"+request.SuccessorWBSessionID || first.Reused ||
		first.TargetWorkLogRef != "worklog-target" || beforeCalls != 1 {
		t.Fatalf("first = %#v, before=%d", first, beforeCalls)
	}
	_, planDigest, err := loadPlan(store.Root, request.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	wantPrivate := []string{PrivateLauncherArgument, store.Root, request.HandoffID, attemptID, string(planDigest)}
	if tmux.cmd != filepath.Join(bin, "wb") || tmux.cwd != worktree || !reflect.DeepEqual(tmux.args, wantPrivate) {
		t.Fatalf("tmux direct command = %q %#v cwd=%q", tmux.cmd, tmux.args, tmux.cwd)
	}
	if _, err := session.Register(sessions, session.Record{PID: launcherPID, Runtime: "codex", Model: "gpt-5", NativeHarnessID: "native-target"}); err != nil {
		t.Fatal(err)
	}
	deps.lookPath = func(string) (string, error) { return "", errors.New("PATH changed after immutable plan") }
	deps.wbExecutable = func() (string, error) { return "", errors.New("WB binary changed after immutable plan") }
	second, err := startWithDependencies(context.Background(), options, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reused || tmux.starts != 1 || beforeCalls != 1 {
		t.Fatalf("replay = %#v starts=%d before=%d", second, tmux.starts, beforeCalls)
	}
}

func TestRunPrivateLauncherPublishesReadyThenExecsFixedArgvAfterRelease(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("WB_HOME", home)
	request := completeLaunchTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(home, sessionmove.DirName))
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
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
	plan := launchPlan{SchemaVersion: launchSchemaVersion, HandoffID: request.HandoffID, RequestDigest: digest,
		SuccessorWBSessionID: request.SuccessorWBSessionID, PredecessorWBSessionID: request.PredecessorWBSessionID,
		Machine: request.TargetMachine, TmuxName: "wb-session-" + request.SuccessorWBSessionID, Runtime: spec.Runtime, Model: spec.Model,
		StoreRoot:   store.Root,
		WorktreeDir: worktree, PinnedCommit: request.BundleCommit, HandoverPath: request.HandoverPath,
		WBExecutable: filepath.Join(bin, "wb"), HarnessExecutable: filepath.Join(bin, "codex"), HarnessArgs: spec.Args}
	_, planDigest, _, err := savePlan(store.Root, plan)
	if err != nil {
		t.Fatal(err)
	}
	launchState, err := openLaunchState(store.Root, request.HandoffID, true)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := launchState.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	attemptID := attempt.id
	_ = attempt.Close()
	_ = launchState.Close()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(worktree); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(previous) }()
	execCalled, sleepCalled := false, false
	deps := privateLauncherDependencies{pid: os.Getpid, register: session.Register,
		verifyPinned: func(context.Context, launchPlan) error { return nil },
		wbExecutable: func() (string, error) { return plan.WBExecutable, nil },
		sleep: func(time.Duration) {
			if sleepCalled {
				t.Fatal("launcher waited after release")
			}
			sleepCalled = true
			ready, _, loadErr := loadReady(store.Root, request.HandoffID, os.Getpid())
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if _, _, saveErr := saveRelease(store.Root, plan, planDigest, ready, "worklog-target", time.Now().UTC()); saveErr != nil {
				t.Fatal(saveErr)
			}
		},
		exec: func(path string, argv, _ []string) error {
			execCalled = true
			if path != plan.HarnessExecutable || !reflect.DeepEqual(argv, append([]string{plan.HarnessExecutable}, plan.HarnessArgs...)) {
				t.Fatalf("exec = %q %#v", path, argv)
			}
			if _, _, err := loadReady(store.Root, request.HandoffID, os.Getpid()); err != nil {
				t.Fatal("ready was not durable before exec")
			}
			return nil
		},
	}
	if err := runPrivateLauncher([]string{store.Root, request.HandoffID, attemptID, string(planDigest)}, deps); err != nil {
		t.Fatal(err)
	}
	if !sleepCalled || !execCalled {
		t.Fatalf("sleep=%t exec=%t", sleepCalled, execCalled)
	}
}

func TestRunPrivateLauncherRecordsExecFailureBeforeReleasingFence(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	request := completeLaunchTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(home, sessionmove.DirName))
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
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
	plan := launchPlan{SchemaVersion: launchSchemaVersion, HandoffID: request.HandoffID, RequestDigest: digest,
		SuccessorWBSessionID: request.SuccessorWBSessionID, PredecessorWBSessionID: request.PredecessorWBSessionID,
		Machine: request.TargetMachine, TmuxName: "wb-session-" + request.SuccessorWBSessionID,
		Runtime: spec.Runtime, Model: spec.Model, StoreRoot: store.Root, WorktreeDir: worktree,
		PinnedCommit: request.BundleCommit, HandoverPath: request.HandoverPath,
		WBExecutable: filepath.Join(bin, "wb"), HarnessExecutable: filepath.Join(bin, "codex"), HarnessArgs: spec.Args}
	_, planDigest, _, err := savePlan(store.Root, plan)
	if err != nil {
		t.Fatal(err)
	}
	launchState, err := openLaunchState(store.Root, request.HandoffID, true)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := launchState.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	attemptID := attempt.id
	_ = attempt.Close()
	_ = launchState.Close()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(worktree); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(previous) }()
	injected := errors.New("injected Exec failure")
	deps := privateLauncherDependencies{
		pid: os.Getpid, register: session.Register, verifyPinned: func(context.Context, launchPlan) error { return nil },
		wbExecutable: func() (string, error) { return plan.WBExecutable, nil },
		now:          func() time.Time { return time.Date(2026, time.August, 25, 16, 0, 0, 0, time.UTC) },
		sleep: func(time.Duration) {
			ready, _, loadErr := loadReady(store.Root, request.HandoffID, os.Getpid())
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if _, _, saveErr := saveRelease(store.Root, plan, planDigest, ready, "worklog-target", time.Now().UTC()); saveErr != nil {
				t.Fatal(saveErr)
			}
		},
		exec: func(string, []string, []string) error { return injected },
	}
	err = runPrivateLauncher([]string{store.Root, request.HandoffID, attemptID, string(planDigest)}, deps)
	if !errors.Is(err, injected) {
		t.Fatalf("private launcher error = %v, want injected Exec failure", err)
	}
	state, err := openLaunchState(store.Root, request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	latest, err := latestAttempt(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = latest.Close() }()
	failure, found, err := latest.loadExecFailure(os.Getpid())
	if err != nil || !found || !strings.Contains(failure.Diagnostic, injected.Error()) {
		t.Fatalf("failure = %#v found=%t error=%v", failure, found, err)
	}
	held, err := latest.execFenceHeld(os.Getpid())
	if err != nil || held {
		t.Fatalf("exec fence after failure = held %t error %v", held, err)
	}
}

func TestLaunchArtifactsRejectTrailingJSONAndSymlinks(t *testing.T) {
	root := t.TempDir()
	handoff := filepath.Join(root, "handoff-123")
	if err := os.MkdirAll(filepath.Join(handoff, "launch"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handoff, "launch", "plan.json"), []byte("{}\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadPlan(root, "handoff-123"); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
	if err := os.Remove(filepath.Join(handoff, "launch", "plan.json")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(handoff, "launch", "plan.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadPlan(root, "handoff-123"); err == nil {
		t.Fatal("symlinked launch artifact was accepted")
	}
}

func completeLaunchTestRequest(t *testing.T) sessionmove.Request {
	t.Helper()
	request := launchTestRequest()
	request.SchemaVersion = sessionmove.RequestSchemaVersion
	request.RepositoryRemote = "/tmp/acme/app.git"
	request.Branch = "feature/session"
	request.SourceWorkCommit = strings.Repeat("a", 40)
	request.BundleCommit = strings.Repeat("b", 40)
	request.HandoverDigest = sessionmove.DigestBytes([]byte("handover\n"))
	request.CreatedAt = time.Date(2026, time.August, 25, 14, 0, 0, 0, time.UTC)
	return request
}
