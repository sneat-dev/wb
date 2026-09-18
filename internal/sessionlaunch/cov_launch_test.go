package sessionlaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

// slCovFlexTmux is a fully injectable tmux seam for launcher state machines.
type slCovFlexTmux struct {
	pid            int
	exists         bool
	panePIDErr     error
	paneFailureErr error
	failure        tmuxFailure
	failureFound   bool
	startErr       error
	starts         int
	onStart        func(*slCovFlexTmux)
	panePIDFn      func() (int, bool, error)
}

func (tmux *slCovFlexTmux) StartDetached(context.Context, string, string, string, []string) error {
	tmux.starts++
	if tmux.onStart != nil {
		tmux.onStart(tmux)
	}
	return tmux.startErr
}

func (tmux *slCovFlexTmux) PanePID(context.Context, string) (int, bool, error) {
	if tmux.panePIDFn != nil {
		return tmux.panePIDFn()
	}
	if tmux.panePIDErr != nil {
		return 0, false, tmux.panePIDErr
	}
	return tmux.pid, tmux.exists, nil
}

func (tmux *slCovFlexTmux) PaneFailure(context.Context, string) (tmuxFailure, bool, error) {
	return tmux.failure, tmux.failureFound, tmux.paneFailureErr
}

// slCovReleasedAttempt builds a durably released attempt and returns it with
// its exact release artifact and the fence already released.
func slCovReleasedAttempt(t *testing.T, fx *launcherRetryFixture, pid int) (*launchAttempt, launcherRelease, sessionmove.Digest, launcherReady) {
	t.Helper()
	state, err := openLaunchState(fx.store.Root, fx.request.HandoffID, true)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = attempt.Close() })
	record := slCovReadyRecord(fx.plan, pid, fx.deps.now())
	fence, err := attempt.acquireExecFence(pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attempt.saveReady(fx.plan, fx.planDigest, record); err != nil {
		t.Fatal(err)
	}
	ready := slCovReady(fx.plan, attempt, fx.planDigest, record)
	release, _, err := attempt.saveRelease(fx.plan, fx.planDigest, ready, "worklog", fx.deps.now())
	if err != nil {
		t.Fatal(err)
	}
	_, releaseDigest, err := attempt.loadRelease()
	if err != nil {
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	return attempt, release, releaseDigest, ready
}

func TestSlCovWaitReadySurfacesEveryGateFailure(t *testing.T) {
	t.Parallel()
	pid := os.Getpid()
	setup := func(t *testing.T) (*launcherRetryFixture, *launchAttempt, *slCovFlexTmux) {
		t.Helper()
		fx := newLauncherRetryFixture(t)
		fx.deps.startTimeout = 30 * time.Millisecond
		tmux := &slCovFlexTmux{pid: pid, exists: true}
		fx.deps.tmux = tmux
		state, err := openLaunchState(fx.store.Root, fx.request.HandoffID, false)
		if err != nil {
			t.Fatal(err)
		}
		attempt, err := state.createAttempt()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = attempt.Close(); _ = state.Close() })
		return fx, attempt, tmux
	}
	t.Run("pane probe error", func(t *testing.T) {
		t.Parallel()
		fx, attempt, tmux := setup(t)
		tmux.panePIDErr = errors.New("tmux probe failed")
		if _, err := waitReady(context.Background(), fx.options(nil), fx.deps, attempt, fx.plan, fx.planDigest); err == nil || !strings.Contains(err.Error(), "tmux probe failed") {
			t.Fatalf("pane probe error = %v", err)
		}
	})
	t.Run("session directory error", func(t *testing.T) {
		t.Parallel()
		fx, attempt, _ := setup(t)
		if _, err := attempt.saveReady(fx.plan, fx.planDigest, slCovReadyRecord(fx.plan, pid, fx.deps.now())); err != nil {
			t.Fatal(err)
		}
		fx.deps.sessionDir = func(string) (string, error) { return "", errors.New("session directory unavailable") }
		if _, err := waitReady(context.Background(), fx.options(nil), fx.deps, attempt, fx.plan, fx.planDigest); err == nil || !strings.Contains(err.Error(), "session directory unavailable") {
			t.Fatalf("session directory error = %v", err)
		}
	})
	t.Run("no live session", func(t *testing.T) {
		t.Parallel()
		fx, attempt, _ := setup(t)
		if _, err := attempt.saveReady(fx.plan, fx.planDigest, slCovReadyRecord(fx.plan, pid, fx.deps.now())); err != nil {
			t.Fatal(err)
		}
		if _, err := waitReady(context.Background(), fx.options(nil), fx.deps, attempt, fx.plan, fx.planDigest); err == nil || !strings.Contains(err.Error(), "without a live WB session") {
			t.Fatalf("no live session = %v", err)
		}
	})
	t.Run("conflicting registration", func(t *testing.T) {
		t.Parallel()
		fx, attempt, _ := setup(t)
		if _, err := attempt.saveReady(fx.plan, fx.planDigest, slCovReadyRecord(fx.plan, pid, fx.deps.now())); err != nil {
			t.Fatal(err)
		}
		conflict := slCovReadyRecord(fx.plan, pid, fx.deps.now())
		conflict.Model = "other"
		if _, err := session.Register(fx.sessions, conflict); err != nil {
			t.Fatal(err)
		}
		if _, err := waitReady(context.Background(), fx.options(nil), fx.deps, attempt, fx.plan, fx.planDigest); err == nil || !strings.Contains(err.Error(), "conflicts with its WB session registration") {
			t.Fatalf("conflicting registration = %v", err)
		}
	})
	t.Run("missing exec fence file", func(t *testing.T) {
		t.Parallel()
		fx, attempt, _ := setup(t)
		record := slCovReadyRecord(fx.plan, pid, fx.deps.now())
		if _, err := attempt.saveReady(fx.plan, fx.planDigest, record); err != nil {
			t.Fatal(err)
		}
		if _, err := session.Register(fx.sessions, record); err != nil {
			t.Fatal(err)
		}
		if _, err := waitReady(context.Background(), fx.options(nil), fx.deps, attempt, fx.plan, fx.planDigest); err == nil {
			t.Fatal("waitReady accepted a missing exec fence")
		}
	})
	t.Run("unheld exec fence", func(t *testing.T) {
		t.Parallel()
		fx, attempt, _ := setup(t)
		record := slCovReadyRecord(fx.plan, pid, fx.deps.now())
		if _, err := attempt.saveReady(fx.plan, fx.planDigest, record); err != nil {
			t.Fatal(err)
		}
		if _, err := session.Register(fx.sessions, record); err != nil {
			t.Fatal(err)
		}
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := waitReady(context.Background(), fx.options(nil), fx.deps, attempt, fx.plan, fx.planDigest); err == nil || !strings.Contains(err.Error(), "without holding its exec-success fence") {
			t.Fatalf("unheld fence = %v", err)
		}
	})
	t.Run("unreadable ready artifact", func(t *testing.T) {
		t.Parallel()
		fx, attempt, _ := setup(t)
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		record := slCovReadyRecord(fx.plan, pid, fx.deps.now())
		if _, err := session.Register(fx.sessions, record); err != nil {
			t.Fatal(err)
		}
		if created, err := attempt.publish(readyDirectoryName, strconv.Itoa(pid)+".json", []byte("{")); err != nil || !created {
			t.Fatalf("inject ready = %t %v", created, err)
		}
		if _, err := waitReady(context.Background(), fx.options(nil), fx.deps, attempt, fx.plan, fx.planDigest); err == nil {
			t.Fatal("waitReady accepted an unreadable ready artifact")
		}
	})
	t.Run("success", func(t *testing.T) {
		t.Parallel()
		fx, attempt, _ := setup(t)
		record := slCovReadyRecord(fx.plan, pid, fx.deps.now())
		if _, err := attempt.saveReady(fx.plan, fx.planDigest, record); err != nil {
			t.Fatal(err)
		}
		if _, err := session.Register(fx.sessions, record); err != nil {
			t.Fatal(err)
		}
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		ready, err := waitReady(context.Background(), fx.options(nil), fx.deps, attempt, fx.plan, fx.planDigest)
		if err != nil || ready.PID != pid {
			t.Fatalf("waitReady = %#v %v", ready, err)
		}
	})
}

func TestSlCovWaitExecSuccessSurfacesEvidenceFailures(t *testing.T) {
	t.Parallel()
	const pid = 5250
	setup := func(t *testing.T) (*launcherRetryFixture, *launchAttempt, launcherRelease, sessionmove.Digest, launcherReady, *slCovFlexTmux) {
		t.Helper()
		fx := newLauncherRetryFixture(t)
		fx.deps.startTimeout = 30 * time.Millisecond
		attempt, release, releaseDigest, ready := slCovReleasedAttempt(t, fx, pid)
		tmux := &slCovFlexTmux{pid: pid, exists: true}
		fx.deps.tmux = tmux
		return fx, attempt, release, releaseDigest, ready, tmux
	}
	t.Run("fence probe error", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, _, ready, _ := setup(t)
		if err := os.Remove(filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), execDirectoryName, "5250.lock")); err != nil {
			t.Fatal(err)
		}
		if err := waitExecSuccess(context.Background(), fx.deps, attempt, fx.plan, fx.planDigest, ready, release); err == nil {
			t.Fatal("waitExecSuccess accepted a missing fence file")
		}
	})
	t.Run("unreadable failure evidence", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, _, ready, _ := setup(t)
		if created, err := attempt.publish(execDirectoryName, "5250.failure.json", []byte("{")); err != nil || !created {
			t.Fatalf("inject failure = %t %v", created, err)
		}
		if err := waitExecSuccess(context.Background(), fx.deps, attempt, fx.plan, fx.planDigest, ready, release); err == nil {
			t.Fatal("waitExecSuccess accepted unreadable failure evidence")
		}
	})
	t.Run("conflicting failure evidence", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, releaseDigest, ready, _ := setup(t)
		if err := attempt.saveExecFailure(fx.plan, fx.planDigest, slCovDigest("other"), releaseDigest, pid, errors.New("boom"), fx.deps.now()); err != nil {
			t.Fatal(err)
		}
		if err := waitExecSuccess(context.Background(), fx.deps, attempt, fx.plan, fx.planDigest, ready, release); err == nil || !strings.Contains(err.Error(), "conflicts with immutable release") {
			t.Fatalf("conflicting failure = %v", err)
		}
	})
	t.Run("terminal pane failure recorded", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, _, ready, tmux := setup(t)
		tmux.failure = tmuxFailure{ExitStatus: 17, Diagnostic: "fatal"}
		tmux.failureFound = true
		err := waitExecSuccess(context.Background(), fx.deps, attempt, fx.plan, fx.planDigest, ready, release)
		var failure *AttemptFailureError
		if !errors.As(err, &failure) || !strings.Contains(failure.Evidence.Diagnostic, "status 17") {
			t.Fatalf("terminal failure = %v", err)
		}
	})
	t.Run("terminal failure cannot be recorded", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, _, ready, tmux := setup(t)
		tmux.failure = tmuxFailure{ExitStatus: 3}
		tmux.failureFound = true
		slCovReadOnly(t, filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), execDirectoryName))
		if err := waitExecSuccess(context.Background(), fx.deps, attempt, fx.plan, fx.planDigest, ready, release); err == nil {
			t.Fatal("waitExecSuccess recorded failure through a read-only exec directory")
		}
	})
	t.Run("terminal failure probe error", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, _, ready, tmux := setup(t)
		tmux.paneFailureErr = errors.New("capture failed")
		if err := waitExecSuccess(context.Background(), fx.deps, attempt, fx.plan, fx.planDigest, ready, release); err == nil || !strings.Contains(err.Error(), "capture failed") {
			t.Fatalf("terminal probe error = %v", err)
		}
	})
	t.Run("liveness probe error", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, _, ready, tmux := setup(t)
		tmux.panePIDErr = errors.New("list-panes failed")
		if err := waitExecSuccess(context.Background(), fx.deps, attempt, fx.plan, fx.planDigest, ready, release); err == nil || !strings.Contains(err.Error(), "list-panes failed") {
			t.Fatalf("liveness probe error = %v", err)
		}
	})
	t.Run("success after exec", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, _, ready, _ := setup(t)
		if err := waitExecSuccess(context.Background(), fx.deps, attempt, fx.plan, fx.planDigest, ready, release); err != nil {
			t.Fatalf("waitExecSuccess = %v", err)
		}
	})
}

func TestSlCovInspectReleasedSurfacesStateFailures(t *testing.T) {
	t.Parallel()
	const pid = 5350
	t.Run("corrupt abandonment", func(t *testing.T) {
		t.Parallel()
		fx := newLauncherRetryFixture(t)
		attempt, release, _, _ := slCovReleasedAttempt(t, fx, pid)
		slCovWrite(t, filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), "abandoned.json"), 0o600, "{}\n")
		if _, err := inspectReleased(context.Background(), fx.options(nil), fx.deps, attempt, fx.plan, fx.planDigest, release); err == nil {
			t.Fatal("inspectReleased accepted a corrupt abandonment artifact")
		}
	})
	t.Run("missing ready", func(t *testing.T) {
		t.Parallel()
		fx := newLauncherRetryFixture(t)
		attempt, release, _, _ := slCovReleasedAttempt(t, fx, pid)
		if err := os.Remove(filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), readyDirectoryName, "5350.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectReleased(context.Background(), fx.options(nil), fx.deps, attempt, fx.plan, fx.planDigest, release); err == nil {
			t.Fatal("inspectReleased accepted a missing ready artifact")
		}
	})
	t.Run("release does not bind plan", func(t *testing.T) {
		t.Parallel()
		fx := newLauncherRetryFixture(t)
		attempt, release, _, _ := slCovReleasedAttempt(t, fx, pid)
		tampered := release
		tampered.PlanDigest = slCovDigest("other")
		if _, err := inspectReleased(context.Background(), fx.options(nil), fx.deps, attempt, fx.plan, fx.planDigest, tampered); err == nil || !strings.Contains(err.Error(), "does not match its plan and ready artifact") {
			t.Fatalf("unbound release = %v", err)
		}
	})
	t.Run("tmux probe error", func(t *testing.T) {
		t.Parallel()
		fx := newLauncherRetryFixture(t)
		attempt, release, _, _ := slCovReleasedAttempt(t, fx, pid)
		fx.deps.tmux = &slCovFlexTmux{panePIDErr: errors.New("probe failed")}
		if _, err := inspectReleased(context.Background(), fx.options(nil), fx.deps, attempt, fx.plan, fx.planDigest, release); err == nil {
			t.Fatal("inspectReleased accepted a probe failure")
		}
	})
	t.Run("tmux pid mismatch", func(t *testing.T) {
		t.Parallel()
		fx := newLauncherRetryFixture(t)
		attempt, release, _, _ := slCovReleasedAttempt(t, fx, pid)
		calls := 0
		fx.deps.tmux = &slCovFlexTmux{panePIDFn: func() (int, bool, error) {
			calls++
			if calls == 1 {
				return pid, true, nil
			}
			return pid + 1, true, nil
		}}
		if _, err := inspectReleased(context.Background(), fx.options(nil), fx.deps, attempt, fx.plan, fx.planDigest, release); err == nil || !strings.Contains(err.Error(), "is not live at PID") {
			t.Fatalf("pid mismatch = %v", err)
		}
	})
	t.Run("session directory error", func(t *testing.T) {
		t.Parallel()
		fx := newLauncherRetryFixture(t)
		attempt, release, _, _ := slCovReleasedAttempt(t, fx, pid)
		fx.deps.tmux = &slCovFlexTmux{pid: pid, exists: true}
		fx.deps.sessionDir = func(string) (string, error) { return "", errors.New("no sessions") }
		if _, err := inspectReleased(context.Background(), fx.options(nil), fx.deps, attempt, fx.plan, fx.planDigest, release); err == nil {
			t.Fatal("inspectReleased accepted a session directory error")
		}
	})
	t.Run("no matching live registration", func(t *testing.T) {
		t.Parallel()
		fx := newLauncherRetryFixture(t)
		attempt, release, _, _ := slCovReleasedAttempt(t, fx, pid)
		fx.deps.tmux = &slCovFlexTmux{pid: pid, exists: true}
		if _, err := inspectReleased(context.Background(), fx.options(nil), fx.deps, attempt, fx.plan, fx.planDigest, release); err == nil || !strings.Contains(err.Error(), "no matching live WB session registration") {
			t.Fatalf("missing registration = %v", err)
		}
	})
}

func TestSlCovFinalizeStartedAndInspectStartedRejectMissingArtifacts(t *testing.T) {
	t.Parallel()
	fx := newLauncherRetryFixture(t)
	state, err := openLaunchState(fx.store.Root, fx.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	attempt, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attempt.Close() }()
	if _, _, err := finalizeStarted(state, attempt, fx.plan, fx.planDigest, launcherRelease{}, fx.deps.now()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finalizeStarted without release = %v", err)
	}
	missing := launcherStarted{AttemptID: "000009-00000000000000000000000000000009", AttemptIndex: 9, PID: 1, StartedAt: fx.deps.now()}
	if _, err := inspectStarted(context.Background(), fx.options(nil), fx.deps, state, fx.plan, fx.planDigest, missing); err == nil {
		t.Fatal("inspectStarted accepted a missing attempt")
	}
	noRelease := launcherStarted{AttemptID: attempt.id, AttemptIndex: attempt.index, PID: 1, StartedAt: fx.deps.now()}
	if _, err := inspectStarted(context.Background(), fx.options(nil), fx.deps, state, fx.plan, fx.planDigest, noRelease); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspectStarted without release = %v", err)
	}
	mismatched := launcherStarted{AttemptID: attempt.id, AttemptIndex: attempt.index + 1, PID: 1, StartedAt: fx.deps.now()}
	if _, err := inspectStarted(context.Background(), fx.options(nil), fx.deps, state, fx.plan, fx.planDigest, mismatched); err == nil {
		t.Fatal("inspectStarted accepted a mismatched marker")
	}
}

func TestSlCovResolveAuthorityPrivateHandoverRequiresLock(t *testing.T) {
	t.Parallel()
	request := completeLaunchTestRequest(t)
	request.HandoverPath = ""
	request.HandoverContent = "private handover\n"
	request.HandoverDigest = sessionmove.DigestBytes([]byte(request.HandoverContent))
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), sessionmove.DirName))
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAuthority(Options{Store: store, Request: request, RequestDigest: digest}); err == nil || !strings.Contains(err.Error(), "requires the exact held handoff execution lock") {
		t.Fatalf("private handover without lock = %v", err)
	}
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	resolved, err := resolveAuthority(Options{Store: store, Request: request, RequestDigest: digest, ExecutionLock: lock})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.launch.ContinuationKind != sessionauthority.ContinuationPrivate ||
		resolved.launch.ContinuationPath != sessionmove.PrivateHandoverPath(store.Root, request.HandoffID) {
		t.Fatalf("private authority = %#v", resolved.launch)
	}
	if _, err := resolveAuthority(Options{Store: store, Request: request, RequestDigest: slCovDigest("wrong"), ExecutionLock: lock}); err == nil || !strings.Contains(err.Error(), "materialize private handover") {
		t.Fatalf("wrong digest materialization = %v", err)
	}
}

func TestSlCovResolveAuthorityRejectsInvalidAuthority(t *testing.T) {
	t.Parallel()
	if _, err := resolveAuthority(Options{}); err == nil {
		t.Fatal("resolveAuthority accepted an empty request")
	}
	invalid := slCovValidAuthority()
	invalid.AggregateID = "not a safe id!"
	if _, err := resolveAuthority(Options{Authority: &invalid}); err == nil {
		t.Fatal("resolveAuthority accepted an invalid authority")
	}
	valid := slCovValidAuthority()
	resolved, err := resolveAuthority(Options{Authority: &valid, StoreRoot: "/tmp/store"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.storeRoot != "/tmp/store" || resolved.launch.AggregateID != valid.AggregateID {
		t.Fatalf("resolved authority = %#v", resolved)
	}
}

func TestSlCovDefaultDependenciesEntryPointsAndPreflight(t *testing.T) {
	t.Parallel()
	t.Run("tmux unavailable", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if _, err := defaultDependencies(""); err == nil || !strings.Contains(err.Error(), "fixed tmux executable is unavailable") {
			t.Fatalf("missing tmux = %v", err)
		}
		if err := PreflightLocal(RuntimeCodex); err == nil || !strings.Contains(err.Error(), "fixed tmux executable is unavailable") {
			t.Fatalf("preflight without tmux = %v", err)
		}
	})
	t.Run("tmux available", func(t *testing.T) {
		bin := t.TempDir()
		slCovExecutable(t, bin, "tmux")
		slCovExecutable(t, bin, "codex")
		t.Setenv("PATH", bin)
		deps, err := defaultDependencies("")
		if err != nil || deps.tmux == nil || deps.pollInterval == 0 || deps.startTimeout == 0 {
			t.Fatalf("defaultDependencies = %#v %v", deps, err)
		}
		if _, err := Start(context.Background(), Options{ProjectsRoot: t.TempDir()}); err == nil {
			t.Fatal("Start accepted an empty request")
		}
		if _, err := Inspect(context.Background(), Options{ProjectsRoot: t.TempDir()}); err == nil {
			t.Fatal("Inspect accepted an empty request")
		}
		if err := PreflightLocal(RuntimeCodex); err != nil {
			t.Fatalf("PreflightLocal = %v", err)
		}
		if err := PreflightLocal("bogus"); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("PreflightLocal(unsupported) = %v", err)
		}
	})
}

func TestSlCovSelectAttemptForStartRejectsMalformedHistory(t *testing.T) {
	t.Parallel()
	fx := newLauncherRetryFixture(t)
	state, err := openLaunchState(fx.store.Root, fx.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()

	t.Run("corrupt started marker", func(t *testing.T) {
		t.Parallel()
		slCovWrite(t, filepath.Join(slCovStateDir(fx.store.Root), "started.json"), 0o600, "{}\n")
		defer func() { _ = os.Remove(filepath.Join(slCovStateDir(fx.store.Root), "started.json")) }()
		if _, _, _, _, err := selectAttemptForStart(context.Background(), fx.options(nil), fx.deps, state, fx.plan, fx.planDigest); err == nil {
			t.Fatal("selectAttemptForStart accepted a corrupt started marker")
		}
	})

	t.Run("malformed attempt history", func(t *testing.T) {
		t.Parallel()
		if err := os.Mkdir(filepath.Join(slCovStateDir(fx.store.Root), attemptsDirectoryName, "garbage"), 0o700); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Remove(filepath.Join(slCovStateDir(fx.store.Root), attemptsDirectoryName, "garbage")) }()
		if _, _, _, _, err := selectAttemptForStart(context.Background(), fx.options(nil), fx.deps, state, fx.plan, fx.planDigest); err == nil {
			t.Fatal("selectAttemptForStart accepted a malformed history")
		}
	})

	t.Run("unopenable latest attempt", func(t *testing.T) {
		t.Parallel()
		if err := os.Mkdir(filepath.Join(slCovStateDir(fx.store.Root), attemptsDirectoryName, "garbage"), 0o700); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Remove(filepath.Join(slCovStateDir(fx.store.Root), attemptsDirectoryName, "garbage")) }()
		// Replace the malformed entry with one that parses but is a file.
		if err := os.Remove(filepath.Join(slCovStateDir(fx.store.Root), attemptsDirectoryName, "garbage")); err != nil {
			t.Fatal(err)
		}
		claimed := filepath.Join(slCovStateDir(fx.store.Root), attemptsDirectoryName, "000001-00000000000000000000000000000001")
		slCovWrite(t, claimed, 0o600, "not a directory")
		defer func() { _ = os.Remove(claimed) }()
		if _, _, _, _, err := selectAttemptForStart(context.Background(), fx.options(nil), fx.deps, state, fx.plan, fx.planDigest); err == nil {
			t.Fatal("selectAttemptForStart accepted an unopenable attempt")
		}
	})

	t.Run("corrupt release", func(t *testing.T) {
		t.Parallel()
		fx := newLauncherRetryFixture(t)
		attempt, _, _, _ := slCovReleasedAttempt(t, fx, 5450)
		slCovWrite(t, filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), "release.json"), 0o600, "{}\n")
		state, err := openLaunchState(fx.store.Root, fx.request.HandoffID, false)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = state.Close() }()
		if _, _, _, _, err := selectAttemptForStart(context.Background(), fx.options(nil), fx.deps, state, fx.plan, fx.planDigest); err == nil {
			t.Fatal("selectAttemptForStart accepted a corrupt release")
		}
	})
}

func TestSlCovFailedAttemptRetryableRejectsEveryAmbiguity(t *testing.T) {
	t.Parallel()
	const pid = 5550
	base := func(t *testing.T) (*launcherRetryFixture, *launchAttempt, launcherRelease, sessionmove.Digest) {
		t.Helper()
		fx := newLauncherRetryFixture(t)
		attempt, release, releaseDigest, _ := slCovReleasedAttempt(t, fx, pid)
		if err := attempt.saveExecFailure(fx.plan, fx.planDigest, release.ReadyDigest, releaseDigest, pid, errors.New("boom"), fx.deps.now()); err != nil {
			t.Fatal(err)
		}
		return fx, attempt, release, releaseDigest
	}
	call := func(t *testing.T, fx *launcherRetryFixture, attempt *launchAttempt, release launcherRelease, releaseDigest sessionmove.Digest) error {
		t.Helper()
		state, err := openLaunchState(fx.store.Root, fx.request.HandoffID, false)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = state.Close() }()
		return failedAttemptRetryable(context.Background(), fx.options(nil), fx.deps, state, attempt, fx.plan, fx.planDigest, release, releaseDigest)
	}
	t.Run("baseline retryable", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, releaseDigest := base(t)
		if err := call(t, fx, attempt, release, releaseDigest); err != nil {
			t.Fatalf("retryable = %v", err)
		}
	})
	t.Run("corrupt abandonment", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, releaseDigest := base(t)
		slCovWrite(t, filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), "abandoned.json"), 0o600, "{}\n")
		if err := call(t, fx, attempt, release, releaseDigest); err == nil {
			t.Fatal("accepted a corrupt abandonment artifact")
		}
	})
	t.Run("started marker exists", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, releaseDigest := base(t)
		slCovWrite(t, filepath.Join(slCovStateDir(fx.store.Root), "started.json"), 0o600, "{}\n")
		if err := call(t, fx, attempt, release, releaseDigest); err == nil {
			t.Fatal("accepted an existing started marker")
		}
	})
	t.Run("unreadable ready", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, releaseDigest := base(t)
		if err := os.Remove(filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), readyDirectoryName, "5550.json")); err != nil {
			t.Fatal(err)
		}
		if err := call(t, fx, attempt, release, releaseDigest); err == nil {
			t.Fatal("accepted a missing ready artifact")
		}
	})
	t.Run("release does not bind attempt", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, releaseDigest := base(t)
		tampered := release
		tampered.PlanDigest = slCovDigest("other")
		if err := call(t, fx, attempt, tampered, releaseDigest); err == nil {
			t.Fatal("accepted an unbound release")
		}
	})
	t.Run("live tmux session", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, releaseDigest := base(t)
		fx.deps.tmux = &slCovFlexTmux{pid: pid, exists: true}
		if err := call(t, fx, attempt, release, releaseDigest); err == nil || !strings.Contains(err.Error(), "still exists") {
			t.Fatalf("live tmux = %v", err)
		}
	})
	t.Run("live pid", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, releaseDigest := base(t)
		fx.deps.processStatus = func(int) error { return nil }
		if err := call(t, fx, attempt, release, releaseDigest); err == nil || !strings.Contains(err.Error(), "still live or ambiguous") {
			t.Fatalf("live pid = %v", err)
		}
	})
	t.Run("held fence", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, releaseDigest := base(t)
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		if err := call(t, fx, attempt, release, releaseDigest); err == nil || !strings.Contains(err.Error(), "still holds its exec fence") {
			t.Fatalf("held fence = %v", err)
		}
	})
}

func TestSlCovValidateAbandonmentRejectsEveryAmbiguity(t *testing.T) {
	t.Parallel()
	const pid = 5650
	base := func(t *testing.T) (*launcherRetryFixture, *launchAttempt, launcherAbandonment) {
		t.Helper()
		fx := newLauncherRetryFixture(t)
		state, err := openLaunchState(fx.store.Root, fx.request.HandoffID, false)
		if err != nil {
			t.Fatal(err)
		}
		attempt, err := state.createAttempt()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = attempt.Close() })
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		abandonment := launcherAbandonment{SchemaVersion: launchSchemaVersion, HandoffID: fx.request.HandoffID,
			AttemptID: attempt.id, AttemptIndex: attempt.index, RequestDigest: fx.digest,
			PlanDigest: fx.planDigest, PID: pid, AbandonedAt: fx.deps.now()}
		return fx, attempt, abandonment
	}
	call := func(t *testing.T, fx *launcherRetryFixture, attempt *launchAttempt, abandonment launcherAbandonment) error {
		t.Helper()
		state, err := openLaunchState(fx.store.Root, fx.request.HandoffID, false)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = state.Close() }()
		return validateAbandonment(context.Background(), fx.deps, state, attempt, fx.plan, fx.planDigest, abandonment)
	}
	t.Run("baseline valid", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		if err := call(t, fx, attempt, abandonment); err != nil {
			t.Fatalf("valid abandonment = %v", err)
		}
	})
	t.Run("does not bind attempt", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		abandonment.AttemptIndex++
		if err := call(t, fx, attempt, abandonment); err == nil {
			t.Fatal("accepted an unbound abandonment")
		}
	})
	t.Run("conflicting release", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		slCovWrite(t, filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), "release.json"), 0o600, "{}\n")
		if err := call(t, fx, attempt, abandonment); err == nil {
			t.Fatal("accepted a conflicting release")
		}
	})
	t.Run("conflicting started", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		slCovWrite(t, filepath.Join(slCovStateDir(fx.store.Root), "started.json"), 0o600, "{}\n")
		if err := call(t, fx, attempt, abandonment); err == nil {
			t.Fatal("accepted a conflicting started marker")
		}
	})
	t.Run("process evidence mismatch", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		abandonment.PID = pid + 1
		if err := call(t, fx, attempt, abandonment); err == nil {
			t.Fatal("accepted mismatched process evidence")
		}
	})
	t.Run("missing process evidence", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		if err := os.Remove(filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), execDirectoryName, "5650.lock")); err != nil {
			t.Fatal(err)
		}
		if err := call(t, fx, attempt, abandonment); err == nil {
			t.Fatal("accepted missing process evidence")
		}
	})
	t.Run("ready appeared after abandonment", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		if _, err := attempt.saveReady(fx.plan, fx.planDigest, slCovReadyRecord(fx.plan, pid, fx.deps.now())); err != nil {
			t.Fatal(err)
		}
		if err := call(t, fx, attempt, abandonment); err == nil {
			t.Fatal("accepted ready evidence after abandonment")
		}
	})
	t.Run("ready digest mismatch", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		if _, err := attempt.saveReady(fx.plan, fx.planDigest, slCovReadyRecord(fx.plan, pid, fx.deps.now())); err != nil {
			t.Fatal(err)
		}
		abandonment.ReadyDigest = slCovDigest("other")
		if err := call(t, fx, attempt, abandonment); err == nil {
			t.Fatal("accepted a conflicting ready digest")
		}
	})
	t.Run("held fence", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		if err := call(t, fx, attempt, abandonment); err == nil {
			t.Fatal("accepted a held fence")
		}
	})
	t.Run("tmux still live", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		fx.deps.tmux = &slCovFlexTmux{pid: pid, exists: true}
		if err := call(t, fx, attempt, abandonment); err == nil {
			t.Fatal("accepted a live tmux session")
		}
	})
	t.Run("tmux probe error", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		fx.deps.tmux = &slCovFlexTmux{panePIDErr: errors.New("probe failed")}
		if err := call(t, fx, attempt, abandonment); err == nil {
			t.Fatal("accepted a tmux probe error")
		}
	})
	t.Run("live pid", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		fx.deps.processStatus = func(int) error { return nil }
		if err := call(t, fx, attempt, abandonment); err == nil {
			t.Fatal("accepted a live PID")
		}
	})
	t.Run("ambiguous pid probe", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		fx.deps.processStatus = func(int) error { return syscall.EPERM }
		if err := call(t, fx, attempt, abandonment); err == nil {
			t.Fatal("accepted an ambiguous PID probe")
		}
	})
}
