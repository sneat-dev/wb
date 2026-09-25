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
)

func TestSlCovTmuxFailureNoServerAndMultiPane(t *testing.T) {
	t.Parallel()
	t.Run("no server", func(t *testing.T) {
		t.Parallel()
		path := slCovScript(t, `printf 'no server running on /private/tmp/tmux-501/default\n'; exit 1`)
		failure, found, err := (osTmux{executable: path}).PaneFailure(context.Background(), "wb-session-x")
		if err != nil || found || failure != (tmuxFailure{}) {
			t.Fatalf("no server = %#v found %t err %v", failure, found, err)
		}
	})
	t.Run("multiple panes", func(t *testing.T) {
		t.Parallel()
		if _, _, err := parseTmuxPaneFailureOutput([]byte("0\t\n0\t\n")); err == nil || !strings.Contains(err.Error(), "want one pane") {
			t.Fatalf("multi-pane output = %v", err)
		}
	})
}

func TestSlCovDecodeLaunchJSONSurfacesTrailingGarbage(t *testing.T) {
	t.Parallel()
	var plan launchPlan
	if err := decodeLaunchJSON([]byte("{\"schema_version\":1}\nnot-json"), &plan); err == nil {
		t.Fatal("decodeLaunchJSON accepted trailing garbage")
	}
}

func TestSlCovOpenLaunchStateFromHandoffRejectsNonDirectoryAttempts(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), sessionmove.DirName)
	handoff := filepath.Join(root, "handoff-broken")
	if err := os.MkdirAll(filepath.Join(handoff, launchDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	slCovWrite(t, filepath.Join(handoff, launchDirectoryName, attemptsDirectoryName), 0o600, "not a directory")
	if _, err := openLaunchState(root, "handoff-broken", false); err == nil {
		t.Fatal("openLaunchState accepted a non-directory attempts path")
	}
}

func TestSlCovOpenOrRecoverClaimedAttemptSurfacesCreationFailure(t *testing.T) {
	t.Parallel()
	state, root := slCovOpenState(t)
	const claimedID = "000001-00000000000000000000000000000001"
	attemptDir := slCovAttemptDir(root, claimedID)
	if err := os.MkdirAll(attemptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(attemptDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(attemptDir, 0o700) })
	if _, err := state.openOrRecoverClaimedAttempt(claimedID); err == nil {
		t.Fatal("openOrRecoverClaimedAttempt created children in a read-only attempt")
	}
}

func TestSlCovPreReleaseProcessEvidenceSurfacesClosedDescriptors(t *testing.T) {
	t.Parallel()
	t.Run("closed ready", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, _, _ := slCovAttempt(t)
		if err := attempt.ready.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := attempt.preReleaseProcessEvidence(); err == nil {
			t.Fatal("accepted a closed ready descriptor")
		}
	})
	t.Run("closed exec", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, _, _ := slCovAttempt(t)
		if err := attempt.exec.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := attempt.preReleaseProcessEvidence(); err == nil {
			t.Fatal("accepted a closed exec descriptor")
		}
	})
}

func TestSlCovOpenPrivateDirectoryAtSurfacesCreationFailure(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	parentFile, err := os.Open(parent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parentFile.Close() })
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	if _, err := openPrivateDirectoryAt(int(parentFile.Fd()), "new-child", true); err == nil {
		t.Fatal("openPrivateDirectoryAt created a directory under a read-only parent")
	}
}

func TestSlCovDefaultDependenciesClosuresAreWired(t *testing.T) {
	bin := t.TempDir()
	slCovExecutable(t, bin, "tmux")
	t.Setenv("PATH", bin)
	deps, err := defaultDependencies(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if deps.now().IsZero() {
		t.Fatal("default now closure returned the zero time")
	}
	directory, err := deps.sessionDir(t.TempDir())
	if err != nil || directory == "" {
		t.Fatalf("default sessionDir = %q %v", directory, err)
	}
	if err := deps.processStatus(os.Getpid()); err != nil {
		t.Fatalf("default processStatus(self) = %v", err)
	}
}

func TestSlCovStartAndInspectSurfaceMissingTmux(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Start(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), "fixed tmux executable is unavailable") {
		t.Fatalf("Start without tmux = %v", err)
	}
	if _, err := Inspect(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), "fixed tmux executable is unavailable") {
		t.Fatalf("Inspect without tmux = %v", err)
	}
}

func TestSlCovInspectPreparedRejectsCorruptPlan(t *testing.T) {
	t.Parallel()
	fixture := slCovNewAuthorityFixture(t)
	slCovWrite(t, filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, "plan.json"), 0o600, "{}\n")
	if _, err := InspectPrepared(context.Background(), fixture.options()); err == nil {
		t.Fatal("InspectPrepared accepted a corrupt plan")
	}
}

//nolint:paralleltest // kept serial: this test's subtests each acquire/Close an exec fence and then rely on startWithDependencies observing accurate fence liveness; running them (or a sibling top-level test) concurrently races any subtest's fork() (which duplicates the fd into the forked child until its own exec), making a fence appear falsely held after Close (task-21, #739; proven test-only -- see acquireExecFence's doc comment in state.go for why production cannot hit this); serial removes every such sibling from the race window
func TestSlCovStartSurfacesUnboundAbandonmentAndPreReleaseEvidence(t *testing.T) {
	ctx := context.Background()
	//nolint:paralleltest // kept serial: see the parent test's reason
	t.Run("unbound abandonment", func(t *testing.T) {
		fixture := slCovNewAuthorityFixture(t)
		slCovSealAbandonment(t, fixture, 6302)
		attempt, err := latestAttempt(fixture.state)
		if err != nil {
			t.Fatal(err)
		}
		abandonment, err := attempt.loadAbandonment()
		if err != nil {
			t.Fatal(err)
		}
		_ = attempt.Close()
		abandonment.RequestDigest = slCovDigest("other")
		raw, err := encodeLaunchJSON(abandonment)
		if err != nil {
			t.Fatal(err)
		}
		slCovWrite(t, filepath.Join(slCovAttemptDir(fixture.root, abandonment.AttemptID), "abandoned.json"), 0o600, string(raw))
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil {
			t.Fatal("start accepted an abandonment that does not bind its plan")
		}
	})
	//nolint:paralleltest // kept serial: see the parent test's reason
	t.Run("unreadable exec fence on pre-release attempt", func(t *testing.T) {
		fixture := slCovNewAuthorityFixture(t)
		attempt := fixture.claimed(t)
		if _, err := attempt.saveReady(fixture.plan, fixture.planDigest(t), slCovReadyRecord(fixture.plan, 6304, fixture.deps.now())); err != nil {
			t.Fatal(err)
		}
		slCovWrite(t, filepath.Join(slCovAttemptDir(fixture.root, attempt.id), execDirectoryName, "6304.lock"), 0o644, "")
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil {
			t.Fatal("start accepted a non-private pre-release exec fence")
		}
	})
	//nolint:paralleltest // kept serial: see the parent test's reason
	t.Run("sealed abandonment verify pinned failure", func(t *testing.T) {
		fixture := slCovNewAuthorityFixture(t)
		attempt := fixture.claimed(t)
		fence, err := attempt.acquireExecFence(6305)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		fixture.deps.verifyPinned = func(context.Context, launchPlan) error { return errors.New("pinned changed") }
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil || !strings.Contains(err.Error(), "after launcher abandonment") {
			t.Fatalf("sealed abandonment verify pinned failure = %v", err)
		}
	})
	//nolint:paralleltest // kept serial: see the parent test's reason
	t.Run("sealed abandonment publication failure", func(t *testing.T) {
		fixture := slCovNewAuthorityFixture(t)
		attempt := fixture.claimed(t)
		fence, err := attempt.acquireExecFence(6306)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		slCovReadOnly(t, slCovAttemptDir(fixture.root, attempt.id))
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil {
			t.Fatal("start persisted an abandonment into a read-only attempt directory")
		}
	})
}

//nolint:paralleltest // kept serial: this test's exec-fence acquire/Close/held sequence races any sibling parallel test's fork() (which duplicates this fd into the forked child until its own exec), making the fence appear falsely held after Close (task-21, #739; proven test-only -- see acquireExecFence's doc comment in state.go for why production cannot hit this); serial removes every such sibling from the race window
func TestSlCovStartFreshLaunchFailurePaths(t *testing.T) {
	ctx := context.Background()
	type handles struct{ fence *execFence }
	setup := func(t *testing.T) (*launcherRetryFixture, *handles) {
		t.Helper()
		fx := newLauncherRetryFixture(t)
		pid := os.Getpid()
		state := &handles{}
		fx.tmux.onStart = func() {
			fx.tmux.pid = pid
			launchState, err := openLaunchState(fx.store.Root, fx.request.HandoffID, false)
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := latestAttempt(launchState)
			if err != nil {
				t.Fatal(err)
			}
			record := slCovReadyRecord(fx.plan, pid, fx.deps.now())
			if _, err := session.Register(fx.sessions, record); err != nil {
				t.Fatal(err)
			}
			state.fence, err = attempt.acquireExecFence(pid)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := attempt.saveReady(fx.plan, fx.planDigest, record); err != nil {
				t.Fatal(err)
			}
			_ = attempt.Close()
			_ = launchState.Close()
		}
		t.Cleanup(func() {
			if state.fence != nil {
				_ = state.fence.Close()
			}
		})
		return fx, state
	}
	t.Run("before release failure", func(t *testing.T) {
		t.Parallel()
		fx, _ := setup(t)
		before := func(context.Context, Prepared) (string, error) { return "", errors.New("custody refused") }
		if _, err := startWithDependencies(ctx, fx.options(before), fx.deps); err == nil || !strings.Contains(err.Error(), "custody refused") {
			t.Fatalf("before release failure = %v", err)
		}
	})
	t.Run("release publication without fence", func(t *testing.T) {
		t.Parallel()
		fx, handles := setup(t)
		before := func(context.Context, Prepared) (string, error) {
			return "worklog", handles.fence.Close()
		}
		if _, err := startWithDependencies(ctx, fx.options(before), fx.deps); err == nil || !strings.Contains(err.Error(), "did not hold its exec-success fence") {
			t.Fatalf("start released without a held fence: %v", err)
		}
	})
	t.Run("exec never observed", func(t *testing.T) {
		t.Parallel()
		fx, _ := setup(t)
		fx.deps.startTimeout = 20 * time.Millisecond
		before := func(context.Context, Prepared) (string, error) { return "worklog", nil }
		if _, err := startWithDependencies(ctx, fx.options(before), fx.deps); err == nil || !strings.Contains(err.Error(), "wait for successor harness Exec") {
			t.Fatalf("exec never observed = %v", err)
		}
	})
	t.Run("finalize started failure", func(t *testing.T) {
		t.Parallel()
		fx, handles := setup(t)
		before := func(context.Context, Prepared) (string, error) {
			state, err := openLaunchState(fx.store.Root, fx.request.HandoffID, false)
			if err != nil {
				return "", err
			}
			attempt, err := latestAttempt(state)
			if err != nil {
				return "", err
			}
			attemptID := attempt.id
			_ = attempt.Close()
			_ = state.Close()
			go func(attemptID string) {
				releasePath := filepath.Join(slCovAttemptDir(fx.store.Root, attemptID), "release.json")
				for {
					if _, err := os.Stat(releasePath); err == nil {
						_ = handles.fence.Close()
						slCovReadOnly(t, slCovStateDir(fx.store.Root))
						return
					}
					time.Sleep(time.Millisecond)
				}
			}(attemptID)
			return "worklog", nil
		}
		if _, err := startWithDependencies(ctx, fx.options(before), fx.deps); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("start finalized into a read-only launch directory: %v", err)
		}
	})
}

func TestSlCovSelectAttemptForStartVerifyPinnedRetryFailure(t *testing.T) {
	t.Parallel()
	fx := newLauncherRetryFixture(t)
	attempt, release, releaseDigest, _ := slCovReleasedAttempt(t, fx, 919191)
	if err := attempt.saveExecFailure(fx.plan, fx.planDigest, release.ReadyDigest, releaseDigest, 919191, errors.New("boom"), fx.deps.now()); err != nil {
		t.Fatal(err)
	}
	fx.deps.tmux = &slCovFlexTmux{}
	fx.deps.verifyPinned = func(context.Context, launchPlan) error { return errors.New("pinned changed") }
	state, err := openLaunchState(fx.store.Root, fx.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	if _, _, _, _, err := selectAttemptForStart(context.Background(), fx.options(nil), fx.deps, state, fx.plan, fx.planDigest); err == nil || !strings.Contains(err.Error(), "verify corrected pinned worktree") {
		t.Fatalf("verify pinned retry failure = %v", err)
	}
}

func TestSlCovFailedAttemptRetryableRemainingGates(t *testing.T) {
	t.Parallel()
	const pid = 919191
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
	t.Run("valid started marker", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, releaseDigest := base(t)
		started := launcherStarted{SchemaVersion: launchSchemaVersion, HandoffID: fx.request.HandoffID,
			AttemptID: attempt.id, AttemptIndex: attempt.index, RequestDigest: fx.plan.RequestDigest,
			PlanDigest: fx.planDigest, ReleaseDigest: releaseDigest, PID: pid, StartedAt: fx.deps.now()}
		raw, err := encodeLaunchJSON(started)
		if err != nil {
			t.Fatal(err)
		}
		if created, err := fx.planStore(t).publish("", "started.json", raw); err != nil || !created {
			t.Fatalf("inject started = %t %v", created, err)
		}
		if err := call(t, fx, attempt, release, releaseDigest); err == nil || !strings.Contains(err.Error(), "started attempt already exists") {
			t.Fatalf("started marker = %v", err)
		}
	})
	t.Run("missing fence file", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, releaseDigest := base(t)
		if err := os.Remove(filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), execDirectoryName, "919191.lock")); err != nil {
			t.Fatal(err)
		}
		if err := call(t, fx, attempt, release, releaseDigest); err == nil {
			t.Fatal("accepted a missing exec fence")
		}
	})
	t.Run("corrupt failure evidence", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, releaseDigest := base(t)
		slCovWrite(t, filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), execDirectoryName, "919191.failure.json"), 0o600, "{}")
		if err := call(t, fx, attempt, release, releaseDigest); err == nil {
			t.Fatal("accepted corrupt failure evidence")
		}
	})
	t.Run("tmux probe error", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, releaseDigest := base(t)
		fx.deps.tmux = &slCovFlexTmux{panePIDErr: errors.New("probe failed")}
		if err := call(t, fx, attempt, release, releaseDigest); err == nil {
			t.Fatal("accepted a tmux probe error")
		}
	})
	t.Run("ambiguous pid probe", func(t *testing.T) {
		t.Parallel()
		fx, attempt, release, releaseDigest := base(t)
		fx.deps.processStatus = func(int) error { return syscall.EPERM }
		if err := call(t, fx, attempt, release, releaseDigest); err == nil {
			t.Fatal("accepted an ambiguous PID probe")
		}
	})
}

// planStore opens the fixture's launch state for direct artifact injection.
func (fixture *launcherRetryFixture) planStore(t *testing.T) *launchState {
	t.Helper()
	state, err := openLaunchState(fixture.store.Root, fixture.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	return state
}

func TestSlCovValidateAbandonmentRemainingGates(t *testing.T) {
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
	call := func(t *testing.T, fx *launcherRetryFixture, state *launchState, attempt *launchAttempt, abandonment launcherAbandonment) error {
		t.Helper()
		return validateAbandonment(context.Background(), fx.deps, state, attempt, fx.plan, fx.planDigest, abandonment)
	}
	t.Run("valid release conflicts", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		state := attempt.state
		record := slCovReadyRecord(fx.plan, pid, fx.deps.now())
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := attempt.saveReady(fx.plan, fx.planDigest, record); err != nil {
			t.Fatal(err)
		}
		if _, _, err := attempt.saveRelease(fx.plan, fx.planDigest, slCovReady(fx.plan, attempt, fx.planDigest, record), "", fx.deps.now()); err != nil {
			t.Fatal(err)
		}
		_ = fence.Close()
		if err := call(t, fx, state, attempt, abandonment); err == nil || !strings.Contains(err.Error(), "conflicting release") {
			t.Fatalf("conflicting release = %v", err)
		}
	})
	t.Run("valid started conflicts", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		state := attempt.state
		started := launcherStarted{SchemaVersion: launchSchemaVersion, HandoffID: fx.request.HandoffID,
			AttemptID: attempt.id, AttemptIndex: attempt.index, RequestDigest: fx.plan.RequestDigest,
			PlanDigest: fx.planDigest, ReleaseDigest: slCovDigest("release"), PID: pid, StartedAt: fx.deps.now()}
		raw, err := encodeLaunchJSON(started)
		if err != nil {
			t.Fatal(err)
		}
		if created, err := state.publish("", "started.json", raw); err != nil || !created {
			t.Fatalf("inject started = %t %v", created, err)
		}
		if err := call(t, fx, state, attempt, abandonment); err == nil || !strings.Contains(err.Error(), "immutable started marker") {
			t.Fatalf("conflicting started = %v", err)
		}
	})
	t.Run("ready digest mismatch refused", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		state := attempt.state
		if _, err := attempt.saveReady(fx.plan, fx.planDigest, slCovReadyRecord(fx.plan, pid, fx.deps.now())); err != nil {
			t.Fatal(err)
		}
		abandonment.ReadyDigest = slCovDigest("other")
		if err := call(t, fx, state, attempt, abandonment); err == nil {
			t.Fatal("accepted a divergent ready digest")
		}
	})
	t.Run("corrupt ready artifact", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		state := attempt.state
		abandonment.ReadyDigest = slCovDigest("expected")
		slCovWrite(t, filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), readyDirectoryName, "5650.json"), 0o600, "{}")
		if err := call(t, fx, state, attempt, abandonment); err == nil {
			t.Fatal("accepted a corrupt ready artifact")
		}
	})
	t.Run("held fence refused", func(t *testing.T) {
		t.Parallel()
		fx, attempt, abandonment := base(t)
		state := attempt.state
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		if err := call(t, fx, state, attempt, abandonment); err == nil {
			t.Fatal("accepted a held fence")
		}
	})
}
