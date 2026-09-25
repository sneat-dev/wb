package sessionlaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

func TestSlCovResolveAuthorityRejectsRequestThatFailsAuthorityValidation(t *testing.T) {
	t.Parallel()
	request := completeLaunchTestRequest(t)
	request.HandoverPath = "."
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAuthority(Options{Request: request, RequestDigest: sessionmove.DigestBytes(raw)}); err == nil || !strings.Contains(err.Error(), "continuation path") {
		t.Fatalf("dot handover path = %v", err)
	}
}

func TestSlCovDefaultDependenciesSessionDirFailure(t *testing.T) {
	bin := t.TempDir()
	slCovExecutable(t, bin, "tmux")
	t.Setenv("PATH", bin)
	deps, err := defaultDependencies(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "not-a-directory")
	slCovWrite(t, file, 0o600, "x")
	if _, err := deps.sessionDir(file); err == nil {
		t.Fatal("sessionDir accepted a regular file root")
	}
}

func TestSlCovStartCannotClaimNewAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("after sealed abandonment", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		slCovSealAbandonment(t, fixture, 7301)
		slCovReadOnly(t, filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, attemptsDirectoryName))
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil {
			t.Fatal("start claimed a new attempt in a read-only attempts directory")
		}
	})
	t.Run("after dead pre-release wrapper", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		attempt := fixture.claimed(t)
		fence, err := attempt.acquireExecFence(7302)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		slCovReadOnly(t, filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, attemptsDirectoryName))
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil {
			t.Fatal("start claimed a replacement attempt in a read-only attempts directory")
		}
	})
}

func TestSlCovStartPlanPublicationAndAdoptionFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("start failure with ambiguous probe", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		fixture.tmux.startErr = errors.New("duplicate session")
		calls := 0
		fixture.tmux.panePIDFn = func() (int, bool, error) {
			calls++
			if calls == 1 {
				return 0, false, nil
			}
			return 0, false, errors.New("probe failed")
		}
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil || !strings.Contains(err.Error(), "duplicate session") || !strings.Contains(err.Error(), "probe failed") {
			t.Fatalf("adoption probe failure = %v", err)
		}
	})
	t.Run("tmux pid changed while authorizing", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		pid := os.Getpid()
		attempt := fixture.claimed(t)
		digest := fixture.planDigest(t)
		record := slCovReadyRecord(fixture.plan, pid, fixture.deps.now())
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		if _, err := attempt.saveReady(fixture.plan, digest, record); err != nil {
			t.Fatal(err)
		}
		if _, err := session.Register(fixture.sessions, record); err != nil {
			t.Fatal(err)
		}
		calls := 0
		fixture.tmux.panePIDFn = func() (int, bool, error) {
			calls++
			if calls == 1 {
				return pid + 1, true, nil
			}
			return pid, true, nil
		}
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil || !strings.Contains(err.Error(), "PID changed") {
			t.Fatalf("pid change = %v", err)
		}
	})
}

func TestSlCovInspectStartedRejectsMismatchedReleasedAttempt(t *testing.T) {
	t.Parallel()
	fixture := slCovNewAuthorityFixture(t)
	attempt, _ := fixture.released(t, 7401)
	started := launcherStarted{SchemaVersion: launchSchemaVersion, HandoffID: fixture.handoffID,
		AttemptID: attempt.id, AttemptIndex: attempt.index + 1, RequestDigest: fixture.plan.RequestDigest,
		PlanDigest: fixture.planDigest(t), ReleaseDigest: slCovDigest("release"), PID: 7401, StartedAt: fixture.deps.now()}
	if _, err := inspectStarted(context.Background(), fixture.options(), fixture.deps, fixture.state, fixture.plan, fixture.planDigest(t), started); err == nil || !strings.Contains(err.Error(), "does not match its selected launch attempt") {
		t.Fatalf("mismatched started marker = %v", err)
	}
}

func TestSlCovVerifyPinnedWorktreeWithoutGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	plan := slCovPlan("handoff-123")
	if err := verifyPinnedWorktree(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "git executable is unavailable") {
		t.Fatalf("verify without git = %v", err)
	}
}

func TestSlCovInspectReleasedProbeFailureAfterExec(t *testing.T) {
	t.Parallel()
	fixture := slCovNewAuthorityFixture(t)
	pid := os.Getpid()
	attempt, release := fixture.liveReleased(t, pid)
	calls := 0
	fixture.tmux.panePIDFn = func() (int, bool, error) {
		calls++
		if calls <= 1 {
			return pid, true, nil
		}
		return 0, false, errors.New("second probe failed")
	}
	_ = attempt
	if _, err := inspectReleased(context.Background(), fixture.options(), fixture.deps, attempt, fixture.plan, fixture.planDigest(t), release); err == nil || !strings.Contains(err.Error(), "second probe failed") {
		t.Fatalf("second probe failure = %v", err)
	}
}

func TestSlCovValidatePlanForOptionsSurfacesHarnessSpecFailure(t *testing.T) {
	t.Parallel()
	_, plan, resolved, options, worktree := slCovAuthorityPlan(t)
	resolved.launch.RequestedHarness = "bogus"
	if err := validatePlanForOptions(plan, options, resolved, worktree); err == nil {
		t.Fatal("validatePlanForOptions accepted an unsupported requested harness")
	}
}

// TestSlCovRunPrivateLauncherParkFailsClosed drives the private launcher over a
// parked aggregate whose private successor context has been removed.
func TestSlCovRunPrivateLauncherParkFailsClosed(t *testing.T) {
	state, plan, bundle, _ := slCovParkFixture(t)
	attempt, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	attemptID := attempt.id
	_ = attempt.Close()
	_, planDigest, err := loadPlan(plan.StoreRoot, bundle.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(plan.StoreRoot, plan.HandoffID, sessionpark.SuccessorContextFileName)); err != nil {
		t.Fatal(err)
	}
	deps := privateLauncherDependencies{
		pid: os.Getpid, register: session.Register,
		wbExecutable: func() (string, error) { return plan.WBExecutable, nil },
		verifyPinned: func(context.Context, launchPlan) error { return nil },
		now:          func() time.Time { return slCovParkNow },
		sleep:        func(time.Duration) { t.Fatal("parked launcher waited for an unreachable release") },
		exec:         func(string, []string, []string) error { t.Fatal("parked launcher exec'd"); return nil },
	}
	args := []string{plan.StoreRoot, plan.HandoffID, attemptID, string(planDigest)}
	if err := runPrivateLauncher(args, deps); err == nil || !strings.Contains(err.Error(), "successor context") {
		t.Fatalf("parked launcher = %v", err)
	}
}

func TestSlCovRunPrivateLauncherCustodyConflicts(t *testing.T) {
	t.Run("abandonment does not bind attempt", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		state, err := openLaunchState(fx.store.Root, fx.request.HandoffID, false)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = state.Close() }()
		attempt, err := state.openAttempt(attemptID)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = attempt.Close() }()
		fence, err := attempt.acquireExecFence(7501)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := attempt.saveAbandonment(fx.plan, fx.planDigest, 7501, fx.deps.now()); err != nil {
			t.Fatal(err)
		}
		abandonment, err := attempt.loadAbandonment()
		if err != nil {
			t.Fatal(err)
		}
		abandonment.RequestDigest = slCovDigest("other")
		raw, err := encodeLaunchJSON(abandonment)
		if err != nil {
			t.Fatal(err)
		}
		slCovWrite(t, filepath.Join(slCovAttemptDir(fx.store.Root, attemptID), "abandoned.json"), 0o600, string(raw))
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil || !strings.Contains(err.Error(), "conflicting immutable abandonment evidence") {
			t.Fatalf("unbound abandonment = %v", err)
		}
	})
	t.Run("corrupt abandonment", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		slCovWrite(t, filepath.Join(slCovAttemptDir(fx.store.Root, attemptID), "abandoned.json"), 0o600, "{")
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil {
			t.Fatal("accepted a corrupt abandonment artifact")
		}
	})
	t.Run("corrupt release", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		slCovWrite(t, filepath.Join(slCovAttemptDir(fx.store.Root, attemptID), "release.json"), 0o600, "{")
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil {
			t.Fatal("accepted a corrupt release artifact")
		}
	})
	t.Run("ambiguous process evidence", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		slCovWrite(t, filepath.Join(slCovAttemptDir(fx.store.Root, attemptID), readyDirectoryName, "2.json"), 0o600, "{}")
		slCovWrite(t, filepath.Join(slCovAttemptDir(fx.store.Root, attemptID), readyDirectoryName, "3.json"), 0o600, "{}")
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil {
			t.Fatal("accepted ambiguous process evidence")
		}
	})
	t.Run("nil wb executable falls back to the running binary", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		deps.wbExecutable = nil
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil || !strings.Contains(err.Error(), "does not match immutable launch plan") {
			t.Fatalf("nil wb executable = %v", err)
		}
	})
	t.Run("session listing failure", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		slCovWrite(t, filepath.Join(filepath.Dir(fx.store.Root), session.DirName), 0o600, "not a directory")
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil {
			t.Fatal("accepted an unreadable session directory")
		}
	})
	t.Run("exec fence is a directory", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		lockPath := filepath.Join(slCovAttemptDir(fx.store.Root, attemptID), execDirectoryName, itoaSlCovLauncher(deps.pid())+".lock")
		if err := os.Mkdir(lockPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil {
			t.Fatal("accepted an exec fence that is a directory")
		}
	})
	t.Run("ready directory not writable", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		slCovReadOnly(t, filepath.Join(slCovAttemptDir(fx.store.Root, attemptID), readyDirectoryName))
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil {
			t.Fatal("published readiness into a read-only directory")
		}
	})
}

func TestSlCovValidatePrivatePlanHarnessFailures(t *testing.T) {
	t.Parallel()
	t.Run("unsupported requested harness", func(t *testing.T) {
		t.Parallel()
		request := completeLaunchTestRequest(t)
		request.RequestedHarness = "bogus"
		store := sessionmove.NewStore(filepath.Join(t.TempDir(), sessionmove.DirName))
		raw, err := sessionmove.EncodeRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		digest := sessionmove.DigestBytes(raw)
		if _, err := store.Admit(raw, digest); err != nil {
			t.Fatal(err)
		}
		state, err := store.Load(request.HandoffID)
		if err != nil {
			t.Fatal(err)
		}
		plan := slCovPlan(request.HandoffID)
		plan.StoreRoot = store.Root
		if err := validatePrivatePlan(state, plan); err == nil {
			t.Fatal("validatePrivatePlan accepted an unsupported requested harness")
		}
	})
	t.Run("invalid absolute harness executable", func(t *testing.T) {
		t.Parallel()
		fx := newLauncherRetryFixture(t)
		state, err := fx.store.Load(fx.request.HandoffID)
		if err != nil {
			t.Fatal(err)
		}
		broken := fx.plan
		broken.HarnessExecutable = filepath.Join(t.TempDir(), "codex")
		if err := validatePrivatePlan(state, broken); err == nil || !strings.Contains(err.Error(), "harness executable") {
			t.Fatalf("invalid harness executable = %v", err)
		}
	})
}

func TestSlCovValidatePrivateParkPlanHarnessExecutable(t *testing.T) {
	state, plan, _, _ := slCovParkFixture(t)
	plan.HarnessExecutable = filepath.Join(t.TempDir(), "codex")
	if _, err := validatePrivateParkPlan(state, plan, realRunner()); err == nil || !strings.Contains(err.Error(), "harness executable") {
		t.Fatalf("invalid parked harness executable = %v", err)
	}
}

func TestSlCovVerifyPrivateLocalRootDirectFailures(t *testing.T) {
	state, plan, bundle, _ := slCovParkFixture(t)
	t.Run("mode mismatch", func(t *testing.T) {
		t.Parallel()
		broken := plan
		broken.RootMode = string(sessionauthority.LaunchRootParkedLocal)
		if err := verifyPrivateLocalRoot(state, bundle, broken, realRunner()); err == nil {
			t.Fatal("accepted a mismatched parked root mode")
		}
	})
	t.Run("missing git", func(t *testing.T) {
		local := bundle
		repo := filepath.Join(t.TempDir(), "worktree")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		local.Worktrees = []sessionpark.Worktree{{Repository: "acme/app", WorktreeDir: repo,
			Branch: "wb-session/park-abc123", Head: strings.Repeat("a", 40)}}
		localPlan := plan
		localPlan.RootMode = string(sessionauthority.LaunchRootParkedLocal)
		localPlan.WorktreeDir = repo
		t.Setenv("PATH", t.TempDir())
		if err := verifyPrivateLocalRoot(state, local, localPlan, realRunner()); err == nil || !strings.Contains(err.Error(), "git executable is unavailable") {
			t.Fatalf("missing git = %v", err)
		}
	})
}
