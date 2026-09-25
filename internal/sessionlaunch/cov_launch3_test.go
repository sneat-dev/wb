package sessionlaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func (fixture *slCovAuthorityFixture) planDigest(t *testing.T) sessionmove.Digest {
	t.Helper()
	return mustPlanDigest(t, fixture)
}

// claimed claims one empty attempt in the fixture's aggregate directory.
func (fixture *slCovAuthorityFixture) claimed(t *testing.T) *launchAttempt {
	t.Helper()
	attempt, err := fixture.state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = attempt.Close() })
	return attempt
}

// released claims one attempt and durably releases it with its fence closed.
func (fixture *slCovAuthorityFixture) released(t *testing.T, pid int) (*launchAttempt, launcherRelease) {
	t.Helper()
	attempt := fixture.claimed(t)
	digest := fixture.planDigest(t)
	record := slCovReadyRecord(fixture.plan, pid, fixture.deps.now())
	fence, err := attempt.acquireExecFence(pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attempt.saveReady(fixture.plan, digest, record); err != nil {
		t.Fatal(err)
	}
	release, _, err := attempt.saveRelease(fixture.plan, digest, slCovReady(fixture.plan, attempt, digest, record), "worklog", fixture.deps.now())
	if err != nil {
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	return attempt, release
}

// liveReleased additionally registers the live successor session and makes the
// fake tmux report the exact released PID.
func (fixture *slCovAuthorityFixture) liveReleased(t *testing.T, pid int) (*launchAttempt, launcherRelease) {
	t.Helper()
	attempt, release := fixture.released(t, pid)
	if _, err := session.Register(fixture.sessions, slCovReadyRecord(fixture.plan, pid, fixture.deps.now())); err != nil {
		t.Fatal(err)
	}
	fixture.tmux.pid, fixture.tmux.exists = pid, true
	return attempt, release
}

func TestSlCovInspectPreparedRejectsAmbiguousCustody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("resolve authority error", func(t *testing.T) {
		t.Parallel()
		if _, err := InspectPrepared(ctx, Options{}); err == nil {
			t.Fatal("InspectPrepared accepted an empty request")
		}
	})
	t.Run("unheld fence", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		fixture.fence.held = false
		if _, err := InspectPrepared(ctx, fixture.options()); err == nil {
			t.Fatal("InspectPrepared accepted an unheld fence")
		}
	})
	t.Run("retain error", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		fixture.fence.retainErr = errors.New("retain failed")
		if _, err := InspectPrepared(ctx, fixture.options()); err == nil {
			t.Fatal("InspectPrepared accepted a retain failure")
		}
	})
	t.Run("missing aggregate", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		empty, err := os.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = empty.Close() }()
		fixture.fence.handoff = empty
		if _, err := InspectPrepared(ctx, fixture.options()); !errors.Is(err, ErrNotReleased) {
			t.Fatalf("missing aggregate = %v", err)
		}
	})
	t.Run("unopenable aggregate", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		dir := filepath.Join(t.TempDir(), "handoff-123")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		slCovWrite(t, filepath.Join(dir, launchDirectoryName), 0o600, "not a directory")
		handoff, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = handoff.Close() }()
		fixture.fence.handoff = handoff
		if _, err := InspectPrepared(ctx, fixture.options()); err == nil {
			t.Fatal("InspectPrepared accepted an unopenable aggregate")
		}
	})
	t.Run("missing plan", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		if err := os.Remove(filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, "plan.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := InspectPrepared(ctx, fixture.options()); !errors.Is(err, ErrNotReleased) {
			t.Fatalf("missing plan = %v", err)
		}
	})
	t.Run("relative worktree", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		options := fixture.options()
		options.WorktreeDir = "relative"
		if _, err := InspectPrepared(ctx, options); err == nil {
			t.Fatal("InspectPrepared accepted a relative worktree")
		}
	})
	t.Run("plan conflict", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		options := fixture.options()
		options.WorktreeDir = t.TempDir()
		if _, err := InspectPrepared(ctx, options); err == nil {
			t.Fatal("InspectPrepared accepted a conflicting plan")
		}
	})
	t.Run("started marker exists", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		started := launcherStarted{SchemaVersion: launchSchemaVersion, HandoffID: fixture.handoffID,
			AttemptID: "000001-00000000000000000000000000000001", AttemptIndex: 1,
			RequestDigest: fixture.plan.RequestDigest, PlanDigest: fixture.planDigest(t),
			ReleaseDigest: slCovDigest("release"), PID: 61, StartedAt: fixture.deps.now()}
		raw, err := encodeLaunchJSON(started)
		if err != nil {
			t.Fatal(err)
		}
		if created, err := fixture.state.publish("", "started.json", raw); err != nil || !created {
			t.Fatalf("inject started = %t %v", created, err)
		}
		if _, err := InspectPrepared(ctx, fixture.options()); err == nil || !strings.Contains(err.Error(), "immutable started marker") {
			t.Fatalf("started marker = %v", err)
		}
	})
	t.Run("corrupt started marker", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		if created, err := fixture.state.publish("", "started.json", []byte("{}")); err != nil || !created {
			t.Fatalf("inject started = %t %v", created, err)
		}
		if _, err := InspectPrepared(ctx, fixture.options()); err == nil {
			t.Fatal("InspectPrepared accepted a corrupt started marker")
		}
	})
	t.Run("malformed history", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		if err := os.Mkdir(filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, attemptsDirectoryName, "garbage"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := InspectPrepared(ctx, fixture.options()); err == nil {
			t.Fatal("InspectPrepared accepted a malformed history")
		}
	})
	t.Run("unopenable attempt", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		slCovWrite(t, filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, attemptsDirectoryName,
			"000001-00000000000000000000000000000001"), 0o600, "not a directory")
		if _, err := InspectPrepared(ctx, fixture.options()); err == nil {
			t.Fatal("InspectPrepared accepted an unopenable attempt")
		}
	})
	t.Run("latest already released", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		fixture.released(t, 62)
		if _, err := InspectPrepared(ctx, fixture.options()); err == nil || !strings.Contains(err.Error(), "already released latest attempt") {
			t.Fatalf("latest released = %v", err)
		}
	})
	t.Run("prior released without ready", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		attempt, _ := fixture.released(t, 63)
		fixture.claimed(t)
		if err := os.Remove(filepath.Join(slCovAttemptDir(fixture.root, attempt.id), readyDirectoryName, "63.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := InspectPrepared(ctx, fixture.options()); err == nil {
			t.Fatal("InspectPrepared accepted a prior release without ready evidence")
		}
	})
	t.Run("prior released with corrupt failure", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		attempt, _ := fixture.released(t, 64)
		fixture.claimed(t)
		slCovWrite(t, filepath.Join(slCovAttemptDir(fixture.root, attempt.id), execDirectoryName, "64.failure.json"), 0o600, "{}")
		if _, err := InspectPrepared(ctx, fixture.options()); err == nil {
			t.Fatal("InspectPrepared accepted corrupt prior failure evidence")
		}
	})
	t.Run("prior released lacks terminal evidence", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		fixture.released(t, 65)
		fixture.claimed(t)
		if _, err := InspectPrepared(ctx, fixture.options()); err == nil || !strings.Contains(err.Error(), "lacks exact terminal evidence") {
			t.Fatalf("prior released without failure = %v", err)
		}
	})
	t.Run("corrupt latest release", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		attempt := fixture.claimed(t)
		slCovWrite(t, filepath.Join(slCovAttemptDir(fixture.root, attempt.id), "release.json"), 0o600, "{}")
		if _, err := InspectPrepared(ctx, fixture.options()); err == nil {
			t.Fatal("InspectPrepared accepted a corrupt release")
		}
	})
	t.Run("ambiguous ready evidence", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		attempt := fixture.claimed(t)
		for _, name := range []string{"66.json", "67.json"} {
			if created, err := attempt.publish(readyDirectoryName, name, []byte("{}")); err != nil || !created {
				t.Fatalf("inject %s = %t %v", name, created, err)
			}
		}
		if _, err := InspectPrepared(ctx, fixture.options()); err == nil {
			t.Fatal("InspectPrepared accepted ambiguous ready evidence")
		}
	})
	t.Run("no evidence", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		fixture.claimed(t)
		if _, err := InspectPrepared(ctx, fixture.options()); !errors.Is(err, ErrNotReleased) {
			t.Fatalf("no evidence = %v", err)
		}
	})
	t.Run("lock without ready", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		attempt := fixture.claimed(t)
		fence, err := attempt.acquireExecFence(68)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := InspectPrepared(ctx, fixture.options()); !errors.Is(err, ErrNotReleased) {
			t.Fatalf("lock without ready = %v", err)
		}
	})
	t.Run("corrupt ready", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		attempt := fixture.claimed(t)
		fence, err := attempt.acquireExecFence(69)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		if created, err := attempt.publish(readyDirectoryName, "69.json", []byte("{}")); err != nil || !created {
			t.Fatalf("inject ready = %t %v", created, err)
		}
		if _, err := InspectPrepared(ctx, fixture.options()); err == nil {
			t.Fatal("InspectPrepared accepted a corrupt ready artifact")
		}
	})
	t.Run("ready conflicts with plan", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		attempt := fixture.claimed(t)
		fence, err := attempt.acquireExecFence(70)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		ready := slCovReady(fixture.plan, attempt, fixture.planDigest(t), slCovReadyRecord(fixture.plan, 70, fixture.deps.now()))
		ready.PlanDigest = slCovDigest("other")
		raw, err := encodeLaunchJSON(ready)
		if err != nil {
			t.Fatal(err)
		}
		if created, err := attempt.publish(readyDirectoryName, "70.json", raw); err != nil || !created {
			t.Fatalf("inject ready = %t %v", created, err)
		}
		if _, err := InspectPrepared(ctx, fixture.options()); err == nil {
			t.Fatal("InspectPrepared accepted ready evidence conflicting with its plan")
		}
	})
}

func TestSlCovInspectWithDependenciesRejectsAmbiguousState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inspect := func(t *testing.T, fixture *slCovAuthorityFixture) error {
		t.Helper()
		_, err := inspectWithDependencies(ctx, fixture.options(), fixture.deps, true)
		return err
	}
	t.Run("resolve authority error", func(t *testing.T) {
		t.Parallel()
		if _, err := inspectWithDependencies(ctx, Options{}, slCovFakeDeps(t, &fakeTmux{}), true); err == nil {
			t.Fatal("accepted an empty request")
		}
	})
	t.Run("unheld fence", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		fixture.fence.held = false
		if err := inspect(t, fixture); err == nil {
			t.Fatal("accepted an unheld fence")
		}
	})
	t.Run("retain error", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		fixture.fence.retainErr = errors.New("retain failed")
		if err := inspect(t, fixture); err == nil {
			t.Fatal("accepted a retain failure")
		}
	})
	t.Run("missing aggregate", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		empty, err := os.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = empty.Close() }()
		fixture.fence.handoff = empty
		if err := inspect(t, fixture); !errors.Is(err, ErrNotReleased) {
			t.Fatalf("missing aggregate = %v", err)
		}
	})
	t.Run("missing plan", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		if err := os.Remove(filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, "plan.json")); err != nil {
			t.Fatal(err)
		}
		if err := inspect(t, fixture); !errors.Is(err, ErrNotReleased) {
			t.Fatalf("missing plan = %v", err)
		}
	})
	t.Run("corrupt plan", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		slCovWrite(t, filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, "plan.json"), 0o600, "{}\n")
		if err := inspect(t, fixture); err == nil {
			t.Fatal("accepted a corrupt plan")
		}
	})
	t.Run("relative worktree", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		options := fixture.options()
		options.WorktreeDir = "relative"
		if _, err := inspectWithDependencies(ctx, options, fixture.deps, true); err == nil {
			t.Fatal("accepted a relative worktree")
		}
	})
	t.Run("corrupt started marker", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		if created, err := fixture.state.publish("", "started.json", []byte("{}")); err != nil || !created {
			t.Fatalf("inject started = %t %v", created, err)
		}
		if err := inspect(t, fixture); err == nil {
			t.Fatal("accepted a corrupt started marker")
		}
	})
	t.Run("missing history", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		if err := inspect(t, fixture); !errors.Is(err, ErrNotReleased) {
			t.Fatalf("missing history = %v", err)
		}
	})
	t.Run("malformed history", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		if err := os.Mkdir(filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, attemptsDirectoryName, "garbage"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := inspect(t, fixture); err == nil {
			t.Fatal("accepted a malformed history")
		}
	})
	t.Run("corrupt release", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		attempt := fixture.claimed(t)
		slCovWrite(t, filepath.Join(slCovAttemptDir(fixture.root, attempt.id), "release.json"), 0o600, "{}")
		if err := inspect(t, fixture); err == nil {
			t.Fatal("accepted a corrupt release")
		}
	})
	t.Run("released live successor", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		pid := os.Getpid()
		fixture.liveReleased(t, pid)
		result, err := inspectWithDependencies(ctx, fixture.options(), fixture.deps, true)
		if err != nil || result.PID != pid || !result.Reused {
			t.Fatalf("live released = %#v %v", result, err)
		}
	})
	t.Run("finalize started failure", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		pid := os.Getpid()
		fixture.liveReleased(t, pid)
		slCovReadOnly(t, filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName))
		if err := inspect(t, fixture); err == nil {
			t.Fatal("accepted a started-marker publication failure")
		}
	})
}

func TestSlCovStartWithDependenciesSurfacesLiveStateConflicts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("finalize started failure", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		pid := os.Getpid()
		fixture.liveReleased(t, pid)
		slCovReadOnly(t, filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName))
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil {
			t.Fatal("start accepted a started-marker publication failure")
		}
	})
	t.Run("verify pinned failure after readiness", func(t *testing.T) {
		t.Parallel()
		fx := newLauncherRetryFixture(t)
		pid := os.Getpid()
		var fence *execFence
		fx.tmux.onStart = func() {
			fx.tmux.pid = pid
			state, err := openLaunchState(fx.store.Root, fx.request.HandoffID, false)
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := latestAttempt(state)
			if err != nil {
				t.Fatal(err)
			}
			record := slCovReadyRecord(fx.plan, pid, fx.deps.now())
			if _, err := session.Register(fx.sessions, record); err != nil {
				t.Fatal(err)
			}
			fence, err = attempt.acquireExecFence(pid)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := attempt.saveReady(fx.plan, fx.planDigest, record); err != nil {
				t.Fatal(err)
			}
			_ = attempt.Close()
			_ = state.Close()
		}
		t.Cleanup(func() {
			if fence != nil {
				_ = fence.Close()
			}
		})
		fx.deps.verifyPinned = func(context.Context, launchPlan) error { return errors.New("pinned changed") }
		if _, err := startWithDependencies(ctx, fx.options(nil), fx.deps); err == nil || !strings.Contains(err.Error(), "pinned changed") {
			t.Fatalf("verify pinned failure = %v", err)
		}
	})
	t.Run("released successor cannot be retried without terminal evidence", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		fixture.released(t, 71)
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil {
			t.Fatal("start accepted an unreleased-ambiguous terminal attempt")
		}
	})
}
