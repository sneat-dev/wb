package sessionlaunch

import (
	"context"
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

func TestSlCovStateArtifactReplayReadFailures(t *testing.T) {
	t.Run("plan replay read", func(t *testing.T) {
		state, root := slCovOpenState(t)
		plan := slCovPlan("handoff-123")
		if err := os.Symlink(filepath.Join(root, "target"), filepath.Join(slCovStateDir(root), "plan.json")); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := state.savePlan(plan); err == nil {
			t.Fatal("savePlan accepted a symlinked plan artifact")
		}
	})
	t.Run("ready replay read", func(t *testing.T) {
		state, root := slCovOpenState(t)
		plan := slCovPlan("handoff-123")
		_, planDigest, _, err := state.savePlan(plan)
		if err != nil {
			t.Fatal(err)
		}
		attempt, err := state.createAttempt()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = attempt.Close() }()
		record := slCovReadyRecord(plan, 8101, time.Now())
		if err := os.Symlink(filepath.Join(t.TempDir(), "target"), filepath.Join(slCovAttemptDir(root, attempt.id), readyDirectoryName, "8101.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := attempt.saveReady(plan, planDigest, record); err == nil {
			t.Fatal("saveReady accepted a symlinked ready artifact")
		}
	})
	t.Run("release replay read", func(t *testing.T) {
		state, root, attempt, plan, planDigest := slCovAttempt(t)
		_ = state
		record := slCovReadyRecord(plan, 8102, time.Now())
		slCovWrite(t, filepath.Join(slCovAttemptDir(root, attempt.id), "release.json"), 0o600, "{}\n")
		if _, err := attempt.saveReady(plan, planDigest, record); err != nil {
			t.Fatal(err)
		}
		fence, err := attempt.acquireExecFence(record.PID)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		if _, _, err := attempt.saveRelease(plan, planDigest, slCovReady(plan, attempt, planDigest, record), "", time.Now()); err == nil {
			t.Fatal("saveRelease accepted a corrupt existing release")
		}
	})
	t.Run("abandonment replay read", func(t *testing.T) {
		_, root, attempt, plan, planDigest := slCovAttempt(t)
		fence, err := attempt.acquireExecFence(8103)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		slCovWrite(t, filepath.Join(slCovAttemptDir(root, attempt.id), "abandoned.json"), 0o600, "{}\n")
		if _, _, err := attempt.saveAbandonment(plan, planDigest, 8103, time.Now()); err == nil {
			t.Fatal("saveAbandonment accepted a corrupt existing artifact")
		}
	})
}

func TestSlCovValidateAbandonmentReadyDigestAbsenceAndFenceError(t *testing.T) {
	const pid = 8201
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
		abandonment := launcherAbandonment{SchemaVersion: launchSchemaVersion, HandoffID: fx.request.HandoffID,
			AttemptID: attempt.id, AttemptIndex: attempt.index, RequestDigest: fx.digest,
			PlanDigest: fx.planDigest, PID: pid, AbandonedAt: fx.deps.now()}
		return fx, attempt, abandonment
	}
	t.Run("unreadable ready without a digest", func(t *testing.T) {
		fx, attempt, abandonment := base(t)
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		if created, err := attempt.publish(readyDirectoryName, itoaSlCovLauncher(pid)+".json", []byte("{}")); err != nil || !created {
			t.Fatalf("inject ready = %t %v", created, err)
		}
		if err := validateAbandonment(context.Background(), fx.deps, attempt.state, attempt, fx.plan, fx.planDigest, abandonment); err == nil {
			t.Fatal("accepted unreadable ready evidence without a digest")
		}
	})
	t.Run("unreadable exec fence", func(t *testing.T) {
		fx, attempt, abandonment := base(t)
		slCovWrite(t, filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), execDirectoryName, itoaSlCovLauncher(pid)+".lock"), 0o644, "")
		if err := validateAbandonment(context.Background(), fx.deps, attempt.state, attempt, fx.plan, fx.planDigest, abandonment); err == nil {
			t.Fatal("accepted an unreadable exec fence")
		}
	})
}

func TestSlCovRunPrivateLauncherRemainingGates(t *testing.T) {
	t.Run("store root conflict", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		modified := fx.plan
		modified.StoreRoot = filepath.Join(filepath.Dir(fx.store.Root), "other-handoffs")
		raw, err := encodeLaunchJSON(modified)
		if err != nil {
			t.Fatal(err)
		}
		planPath := filepath.Join(slCovStateDir(fx.store.Root), "plan.json")
		if err := os.Remove(planPath); err != nil {
			t.Fatal(err)
		}
		slCovWrite(t, planPath, 0o600, string(raw))
		_, digest, err := loadPlan(fx.store.Root, fx.request.HandoffID)
		if err != nil {
			t.Fatal(err)
		}
		if err := runPrivateLauncher([]string{fx.store.Root, fx.request.HandoffID, attemptID, string(digest)}, deps); err == nil || !strings.Contains(err.Error(), "store root does not match") {
			t.Fatalf("store root conflict = %v", err)
		}
	})
	t.Run("corrupt release appears before wait", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		deps.register = func(directory string, record session.Record) (session.Record, error) {
			saved, err := session.Register(directory, record)
			if err != nil {
				return session.Record{}, err
			}
			slCovWrite(t, filepath.Join(slCovAttemptDir(fx.store.Root, attemptID), "release.json"), 0o600, "{")
			return saved, nil
		}
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil {
			t.Fatal("accepted a corrupt release observed before the wait loop")
		}
	})
}

func TestSlCovValidatePrivatePlanHarnessSpecAndExecutable(t *testing.T) {
	t.Run("unsupported requested harness", func(t *testing.T) {
		request := completeLaunchTestRequest(t)
		request.RequestedHarness = "bogus"
		store := sessionmove.NewStore(filepath.Join(t.TempDir(), sessionmove.DirName))
		raw, err := sessionmove.EncodeRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Admit(raw, sessionmove.DigestBytes(raw)); err != nil {
			t.Fatal(err)
		}
		state, err := store.Load(request.HandoffID)
		if err != nil {
			t.Fatal(err)
		}
		worktree := t.TempDir()
		plan := launchPlan{
			SchemaVersion: launchSchemaVersion, HandoffID: request.HandoffID, RequestDigest: state.Digest,
			SuccessorWBSessionID: request.SuccessorWBSessionID, PredecessorWBSessionID: request.PredecessorWBSessionID,
			Machine: request.TargetMachine, TmuxName: "wb-session-" + request.SuccessorWBSessionID,
			StoreRoot: store.Root, WorktreeDir: worktree, PinnedCommit: request.BundleCommit,
			HandoverPath: request.HandoverPath, PinnedBranch: "wb-session/" + request.HandoffID,
			AuthorityFile: "request.json", ContinuationKind: string(sessionauthority.ContinuationTracked),
			ContinuationDigest: request.HandoverDigest,
			WBExecutable:       "/bin/wb", HarnessExecutable: "/bin/codex",
		}
		if err := validatePrivatePlan(state, plan); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("unsupported requested harness = %v", err)
		}
	})
	t.Run("absolute but invalid harness executable", func(t *testing.T) {
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

func TestSlCovVerifyPrivateLocalRootModeMismatch(t *testing.T) {
	state, plan, bundle, _ := slCovParkFixture(t)
	repo := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	local := bundle
	local.Worktrees = []sessionpark.Worktree{{Repository: "acme/app", WorktreeDir: repo,
		Branch: "wb-session/park-abc123", Head: strings.Repeat("a", 40)}}
	broken := plan
	broken.RootMode = string(sessionauthority.LaunchRootPinnedClean)
	broken.WorktreeDir = repo
	if err := verifyPrivateLocalRoot(state, local, broken); err == nil || !strings.Contains(err.Error(), "local root does not match") {
		t.Fatalf("mode mismatch = %v", err)
	}
}
