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
	"github.com/sneat-dev/wb/internal/sessionmove"
)

// slCovPrivateFixture prepares one admitted handoff, a claimed attempt, and a
// permissive private-launcher dependency set rooted at the pinned worktree.
func slCovPrivateFixture(t *testing.T) (*launcherRetryFixture, string, privateLauncherDependencies) {
	t.Helper()
	fx := newLauncherRetryFixture(t)
	state, err := openLaunchState(fx.store.Root, fx.request.HandoffID, true)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	attemptID := attempt.id
	_ = attempt.Close()
	_ = state.Close()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(fx.worktree); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	deps := privateLauncherDependencies{
		pid: os.Getpid, register: session.Register,
		wbExecutable: func() (string, error) { return fx.plan.WBExecutable, nil },
		verifyPinned: func(context.Context, launchPlan) error { return nil },
		now:          fx.deps.now,
		sleep:        func(time.Duration) { t.Fatal("private launcher waited for an unexpected release") },
		exec:         func(string, []string, []string) error { return nil },
	}
	return fx, attemptID, deps
}

func (fixture *launcherRetryFixture) privateArgs(attemptID string) []string {
	return []string{fixture.store.Root, fixture.request.HandoffID, attemptID, string(fixture.planDigest)}
}

func TestSlCovRunPrivateLauncherRejectsInvalidInvocation(t *testing.T) {
	fx, attemptID, deps := slCovPrivateFixture(t)
	args := fx.privateArgs(attemptID)
	if err := runPrivateLauncher(args[:3], deps); err == nil {
		t.Fatal("accepted a short argv")
	}
	unclean := append([]string(nil), args...)
	unclean[0] = fx.store.Root + "/"
	if err := runPrivateLauncher(unclean, deps); err == nil {
		t.Fatal("accepted an unclean store root")
	}
	foreign := append([]string(nil), args...)
	foreign[0] = filepath.Join(filepath.Dir(fx.store.Root), "elsewhere")
	if err := runPrivateLauncher(foreign, deps); err == nil {
		t.Fatal("accepted a foreign store root")
	}
	missing := append([]string(nil), args...)
	missing[0] = filepath.Join(t.TempDir(), sessionmove.DirName)
	if err := runPrivateLauncher(missing, deps); err == nil {
		t.Fatal("accepted a missing store root")
	}
	unknownAttempt := append([]string(nil), args...)
	unknownAttempt[2] = "000009-00000000000000000000000000000009"
	if err := runPrivateLauncher(unknownAttempt, deps); err == nil {
		t.Fatal("accepted an unknown attempt")
	}
	wrongDigest := append([]string(nil), args...)
	wrongDigest[3] = string(slCovDigest("other"))
	if err := runPrivateLauncher(wrongDigest, deps); err == nil || !strings.Contains(err.Error(), "does not match fixed tmux argv") {
		t.Fatalf("digest mismatch = %v", err)
	}
}

func TestSlCovRunPrivateLauncherRejectsChangedPlanAndRequest(t *testing.T) {
	t.Run("missing plan", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		if err := os.Remove(filepath.Join(slCovStateDir(fx.store.Root), "plan.json")); err != nil {
			t.Fatal(err)
		}
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing plan = %v", err)
		}
	})
	t.Run("changed harness authority", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		tampered := fx.plan
		tampered.Model = "claude-opus"
		raw, err := encodeLaunchJSON(tampered)
		if err != nil {
			t.Fatal(err)
		}
		planPath := filepath.Join(slCovStateDir(fx.store.Root), "plan.json")
		if err := os.Remove(planPath); err != nil {
			t.Fatal(err)
		}
		if created, err := os.OpenFile(planPath, os.O_CREATE|os.O_WRONLY, 0o600); err != nil {
			t.Fatal(err)
		} else {
			if _, err := created.Write(raw); err != nil {
				t.Fatal(err)
			}
			_ = created.Close()
		}
		// Recompute the digest the caller would pass for this altered plan.
		_, digest, err := loadPlan(fx.store.Root, fx.request.HandoffID)
		if err != nil {
			t.Fatal(err)
		}
		if err := runPrivateLauncher([]string{fx.store.Root, fx.request.HandoffID, attemptID, string(digest)}, deps); err == nil {
			t.Fatal("accepted a plan that does not match the admitted request")
		}
	})
	t.Run("missing admitted request", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		if err := os.Remove(filepath.Join(fx.store.Root, fx.request.HandoffID, "request.json")); err != nil {
			t.Fatal(err)
		}
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil {
			t.Fatal("accepted a missing admitted request")
		}
	})
	t.Run("wrong worktree root", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		previous, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(t.TempDir()); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chdir(previous) }()
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil || !strings.Contains(err.Error(), "not rooted in the pinned target worktree") {
			t.Fatalf("wrong worktree = %v", err)
		}
	})
}

func TestSlCovRunPrivateLauncherRejectsExistingCustody(t *testing.T) {
	t.Run("abandoned", func(t *testing.T) {
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
		fence, err := attempt.acquireExecFence(7101)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := attempt.saveAbandonment(fx.plan, fx.planDigest, 7101, fx.deps.now()); err != nil {
			t.Fatal(err)
		}
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil || !strings.Contains(err.Error(), "immutably abandoned") {
			t.Fatalf("abandoned attempt = %v", err)
		}
	})
	t.Run("already released", func(t *testing.T) {
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
		record := slCovReadyRecord(fx.plan, 7102, fx.deps.now())
		fence, err := attempt.acquireExecFence(record.PID)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		if _, err := attempt.saveReady(fx.plan, fx.planDigest, record); err != nil {
			t.Fatal(err)
		}
		if _, _, err := attempt.saveRelease(fx.plan, fx.planDigest, slCovReady(fx.plan, attempt, fx.planDigest, record), "", fx.deps.now()); err != nil {
			t.Fatal(err)
		}
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil || !strings.Contains(err.Error(), "already immutably released") {
			t.Fatalf("released attempt = %v", err)
		}
	})
	t.Run("existing process evidence", func(t *testing.T) {
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
		fence, err := attempt.acquireExecFence(7103)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil || !strings.Contains(err.Error(), "already has process evidence") {
			t.Fatalf("existing evidence = %v", err)
		}
	})
}

func TestSlCovRunPrivateLauncherRejectsExecutableAndSessionConflicts(t *testing.T) {
	t.Run("running WB mismatch", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		other := slCovExecutable(t, t.TempDir(), "wb")
		deps.wbExecutable = func() (string, error) { return other, nil }
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil || !strings.Contains(err.Error(), "does not match immutable launch plan") {
			t.Fatalf("wb mismatch = %v", err)
		}
	})
	t.Run("wb lookup error", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		deps.wbExecutable = func() (string, error) { return "", errors.New("no wb") }
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil || !strings.Contains(err.Error(), "no wb") {
			t.Fatalf("wb lookup error = %v", err)
		}
	})
	t.Run("live successor session ID", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		if _, err := session.Register(fx.sessions, session.Record{
			PID: os.Getppid(), WBSessionID: fx.plan.SuccessorWBSessionID, Machine: fx.plan.Machine,
			Runtime: fx.plan.Runtime, Model: fx.plan.Model, TmuxName: "other-tmux",
			PredecessorWBSessionID: fx.plan.PredecessorWBSessionID, HandoffID: "other", StartedAt: fx.deps.now(),
		}); err != nil {
			t.Fatal(err)
		}
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil || !strings.Contains(err.Error(), "is already live at PID") {
			t.Fatalf("live session ID = %v", err)
		}
	})
	t.Run("conflicting exec fence", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		slCovWrite(t, filepath.Join(slCovAttemptDir(fx.store.Root, attemptID), execDirectoryName, itoaSlCovLauncher(deps.pid())+".lock"), 0o644, "")
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil {
			t.Fatal("accepted a non-private exec fence")
		}
	})
	t.Run("registration failure", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		deps.register = func(string, session.Record) (session.Record, error) {
			return session.Record{}, errors.New("register refused")
		}
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil || !strings.Contains(err.Error(), "register refused") {
			t.Fatalf("registration failure = %v", err)
		}
	})
	t.Run("registration mismatch", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		deps.register = func(directory string, record session.Record) (session.Record, error) {
			record.TmuxName = "not-the-planned-tmux"
			return session.Register(directory, record)
		}
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil || !strings.Contains(err.Error(), "not durably readable") {
			t.Fatalf("registration mismatch = %v", err)
		}
	})
}

func TestSlCovRunPrivateLauncherSurfacesFailureAndReleaseConflicts(t *testing.T) {
	t.Run("ready publication failure", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		slCovReadOnly(t, filepath.Join(slCovAttemptDir(fx.store.Root, attemptID), readyDirectoryName))
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil {
			t.Fatal("accepted a read-only ready directory")
		}
	})
	t.Run("conflicting release appears before wait", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		deps.register = func(directory string, record session.Record) (session.Record, error) {
			saved, err := session.Register(directory, record)
			if err != nil {
				return session.Record{}, err
			}
			state, openErr := openLaunchState(fx.store.Root, fx.request.HandoffID, false)
			if openErr != nil {
				return session.Record{}, openErr
			}
			defer func() { _ = state.Close() }()
			attempt, openErr := state.openAttempt(attemptID)
			if openErr != nil {
				return session.Record{}, openErr
			}
			defer func() { _ = attempt.Close() }()
			conflicting := launcherRelease{SchemaVersion: launchSchemaVersion, HandoffID: fx.plan.HandoffID,
				AttemptID: attempt.id, AttemptIndex: attempt.index, RequestDigest: fx.plan.RequestDigest,
				PlanDigest: fx.planDigest, ReadyDigest: slCovDigest("other"), PID: record.PID, ReleasedAt: fx.deps.now()}
			raw, encodeErr := encodeLaunchJSON(conflicting)
			if encodeErr != nil {
				return session.Record{}, encodeErr
			}
			if _, publishErr := attempt.publish("", "release.json", raw); publishErr != nil {
				return session.Record{}, publishErr
			}
			return saved, nil
		}
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err == nil || !strings.Contains(err.Error(), "selects a different launcher") {
			t.Fatalf("conflicting release = %v", err)
		}
	})
	t.Run("verify pinned failure is recorded", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		deps.sleep = func(time.Duration) {
			ready, _, err := loadReady(fx.store.Root, fx.request.HandoffID, deps.pid())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := saveRelease(fx.store.Root, fx.plan, fx.planDigest, ready, "worklog", fx.deps.now()); err != nil {
				t.Fatal(err)
			}
		}
		deps.verifyPinned = func(context.Context, launchPlan) error { return errors.New("pinned changed") }
		err := runPrivateLauncher(fx.privateArgs(attemptID), deps)
		if err == nil || !strings.Contains(err.Error(), "pinned changed") {
			t.Fatalf("verify pinned failure = %v", err)
		}
		state, openErr := openLaunchState(fx.store.Root, fx.request.HandoffID, false)
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer func() { _ = state.Close() }()
		attempt, openErr := state.openAttempt(attemptID)
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer func() { _ = attempt.Close() }()
		if _, found, failureErr := attempt.loadExecFailure(deps.pid()); failureErr != nil || !found {
			t.Fatalf("recorded failure = found %t err %v", found, failureErr)
		}
	})
	t.Run("exec failure is recorded", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		deps.sleep = func(time.Duration) {
			ready, _, err := loadReady(fx.store.Root, fx.request.HandoffID, deps.pid())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := saveRelease(fx.store.Root, fx.plan, fx.planDigest, ready, "worklog", fx.deps.now()); err != nil {
				t.Fatal(err)
			}
		}
		deps.exec = func(string, []string, []string) error { return errors.New("exec refused") }
		err := runPrivateLauncher(fx.privateArgs(attemptID), deps)
		if err == nil || !strings.Contains(err.Error(), "exec refused") {
			t.Fatalf("exec failure = %v", err)
		}
	})
	t.Run("success publishes and execs", func(t *testing.T) {
		fx, attemptID, deps := slCovPrivateFixture(t)
		executed := false
		deps.sleep = func(time.Duration) {
			ready, _, err := loadReady(fx.store.Root, fx.request.HandoffID, deps.pid())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := saveRelease(fx.store.Root, fx.plan, fx.planDigest, ready, "worklog", fx.deps.now()); err != nil {
				t.Fatal(err)
			}
		}
		deps.exec = func(path string, argv, environment []string) error {
			executed = true
			if path != fx.plan.HarnessExecutable || len(environment) == 0 {
				t.Fatalf("exec = %q env=%d", path, len(environment))
			}
			return nil
		}
		if err := runPrivateLauncher(fx.privateArgs(attemptID), deps); err != nil {
			t.Fatal(err)
		}
		if !executed {
			t.Fatal("harness was not exec'd")
		}
	})
}

func itoaSlCovLauncher(value int) string {
	if value < 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	if digits == "" {
		return "0"
	}
	return digits
}

func TestSlCovValidatePrivatePlanRejectsDivergence(t *testing.T) {
	fx := newLauncherRetryFixture(t)
	state, err := fx.store.Load(fx.request.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePrivatePlan(state, fx.plan); err != nil {
		t.Fatalf("consistent plan = %v", err)
	}
	tests := map[string]func(*launchPlan){
		"schema":            func(plan *launchPlan) { plan.SchemaVersion++ },
		"handoff":           func(plan *launchPlan) { plan.HandoffID = "other" },
		"request digest":    func(plan *launchPlan) { plan.RequestDigest = slCovDigest("other") },
		"successor":         func(plan *launchPlan) { plan.SuccessorWBSessionID = "other" },
		"predecessor":       func(plan *launchPlan) { plan.PredecessorWBSessionID = "other" },
		"machine":           func(plan *launchPlan) { plan.Machine = "other" },
		"tmux name":         func(plan *launchPlan) { plan.TmuxName = "other" },
		"pinned commit":     func(plan *launchPlan) { plan.PinnedCommit = strings.Repeat("c", 40) },
		"handover path":     func(plan *launchPlan) { plan.HandoverPath = "other.md" },
		"store root":        func(plan *launchPlan) { plan.StoreRoot = "" },
		"pinned branch":     func(plan *launchPlan) { plan.PinnedBranch = "other" },
		"authority file":    func(plan *launchPlan) { plan.AuthorityFile = "other.json" },
		"continuation kind": func(plan *launchPlan) { plan.ContinuationKind = "other" },
		"continuation dig":  func(plan *launchPlan) { plan.ContinuationDigest = slCovDigest("other") },
		"harness model":     func(plan *launchPlan) { plan.Model = "claude-opus" },
		"harness base":      func(plan *launchPlan) { plan.HarnessExecutable = "/other/other" },
		"harness args":      func(plan *launchPlan) { plan.HarnessArgs = []string{"other"} },
		"relative wb path":  func(plan *launchPlan) { plan.WBExecutable = "relative/wb" },
		"relative harness":  func(plan *launchPlan) { plan.HarnessExecutable = "relative/codex" },
		"relative worktree": func(plan *launchPlan) { plan.WorktreeDir = "relative/worktree" },
		"invalid wb exec":   func(plan *launchPlan) { plan.WBExecutable = filepath.Join(t.TempDir(), "absent") },
		"invalid harness":   func(plan *launchPlan) { plan.HarnessExecutable = filepath.Join(t.TempDir(), "absent") },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			broken := fx.plan
			mutate(&broken)
			if err := validatePrivatePlan(state, broken); err == nil {
				t.Fatalf("divergence %q was accepted", name)
			}
		})
	}
	t.Run("sparse optional fields", func(t *testing.T) {
		sparse := fx.plan
		sparse.PinnedBranch, sparse.AuthorityFile, sparse.ContinuationKind, sparse.ContinuationDigest = "", "", "", ""
		if err := validatePrivatePlan(state, sparse); err != nil {
			t.Fatalf("sparse plan = %v", err)
		}
	})
}

func TestSlCovVerifyLauncherWorktreeReadsAndDigestsTheHandover(t *testing.T) {
	fx := newLauncherRetryFixture(t)
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(fx.worktree); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(previous) }()
	if err := verifyLauncherWorktree(fx.plan, fx.request, fx.store); err != nil {
		t.Fatalf("tracked handover = %v", err)
	}
	t.Run("wrong cwd", func(t *testing.T) {
		if err := os.Chdir(t.TempDir()); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chdir(fx.worktree) }()
		if err := verifyLauncherWorktree(fx.plan, fx.request, fx.store); err == nil {
			t.Fatal("accepted a foreign cwd")
		}
	})
	t.Run("missing handover", func(t *testing.T) {
		path := filepath.Join(fx.worktree, filepath.FromSlash(fx.request.HandoverPath))
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.WriteFile(path, original, 0o644) }()
		if err := verifyLauncherWorktree(fx.plan, fx.request, fx.store); err == nil {
			t.Fatal("accepted a missing handover")
		}
	})
	t.Run("changed handover digest", func(t *testing.T) {
		path := filepath.Join(fx.worktree, filepath.FromSlash(fx.request.HandoverPath))
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("tampered\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.WriteFile(path, original, 0o644) }()
		if err := verifyLauncherWorktree(fx.plan, fx.request, fx.store); err == nil || !strings.Contains(err.Error(), "digest changed") {
			t.Fatalf("tampered handover = %v", err)
		}
	})
	t.Run("missing worktree", func(t *testing.T) {
		broken := fx.plan
		broken.WorktreeDir = filepath.Join(t.TempDir(), "absent")
		if err := verifyLauncherWorktree(broken, fx.request, fx.store); err == nil {
			t.Fatal("accepted a missing worktree")
		}
	})
	t.Run("private handover", func(t *testing.T) {
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
		lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.EnsureHandoverUnderLock(lock, request.HandoffID, digest); err != nil {
			t.Fatal(err)
		}
		_ = lock.Close()
		plan := fx.plan
		plan.WorktreeDir = fx.worktree
		if err := verifyLauncherWorktree(plan, request, store); err != nil {
			t.Fatalf("private handover = %v", err)
		}
		pruned := sessionmove.NewStore(store.Root)
		if err := os.Remove(filepath.Join(store.Root, request.HandoffID, "handover.md")); err == nil {
			if err := verifyLauncherWorktree(plan, request, pruned); err == nil {
				t.Fatal("accepted a missing private handover")
			}
		}
	})
}
