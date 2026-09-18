package sessionlaunch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// slCovAttempt creates a fresh launch state with one plan and one claimed attempt.
func slCovAttempt(t *testing.T) (*launchState, string, *launchAttempt, launchPlan, sessionmove.Digest) {
	t.Helper()
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
	t.Cleanup(func() { _ = attempt.Close() })
	return state, root, attempt, plan, planDigest
}

func TestSlCovSavePlanReportsReplayAndRejectsConflict(t *testing.T) {
	t.Parallel()
	state, _, _, plan, planDigest := slCovAttempt(t)
	replayed, replayDigest, replay, err := state.savePlan(plan)
	if err != nil || !replay || replayed.Model != plan.Model || replayDigest != planDigest {
		t.Fatalf("replay = %#v replay=%t digest=%q err=%v", replayed, replay, replayDigest, err)
	}
	conflict := plan
	conflict.Model = "claude-opus"
	if _, _, _, err := state.savePlan(conflict); err == nil || !strings.Contains(err.Error(), "conflicting immutable launch plan") {
		t.Fatalf("conflicting plan = %v", err)
	}
	loaded, loadedDigest, err := state.loadPlan()
	if err != nil || loadedDigest != planDigest || !equalLaunchPlan(loaded, plan) {
		t.Fatalf("loadPlan = %#v %q %v", loaded, loadedDigest, err)
	}
}

func TestSlCovLoadPlanRejectsMalformedAndForeignArtifacts(t *testing.T) {
	t.Parallel()
	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		state, _, _, _, _ := slCovAttempt(t)
		fresh, _ := slCovOpenState(t)
		_ = state
		if _, _, err := fresh.loadPlan(); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing plan = %v", err)
		}
	})
	t.Run("malformed", func(t *testing.T) {
		t.Parallel()
		state, _ := slCovOpenState(t)
		if created, err := state.publish("", "plan.json", []byte("{")); err != nil || !created {
			t.Fatalf("publish malformed = %t %v", created, err)
		}
		if _, _, err := state.loadPlan(); err == nil {
			t.Fatal("loadPlan accepted malformed JSON")
		}
	})
	t.Run("foreign handoff", func(t *testing.T) {
		t.Parallel()
		state, _ := slCovOpenState(t)
		foreign := slCovPlan("handoff-999")
		raw, err := encodeLaunchJSON(foreign)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := state.publish("", "plan.json", raw); err != nil {
			t.Fatal(err)
		}
		if _, _, err := state.loadPlan(); err == nil || !strings.Contains(err.Error(), "invalid immutable launch plan") {
			t.Fatalf("foreign plan = %v", err)
		}
	})
	t.Run("stale schema", func(t *testing.T) {
		t.Parallel()
		state, _ := slCovOpenState(t)
		stale := slCovPlan("handoff-123")
		stale.SchemaVersion = launchSchemaVersion + 1
		raw, err := encodeLaunchJSON(stale)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := state.publish("", "plan.json", raw); err != nil {
			t.Fatal(err)
		}
		if _, _, err := state.loadPlan(); err == nil {
			t.Fatal("loadPlan accepted a stale schema version")
		}
	})
}

func TestSlCovSaveReadyRequiresExactPlanAndConflictsOnDivergentBytes(t *testing.T) {
	t.Parallel()
	_, _, attempt, plan, planDigest := slCovAttempt(t)
	record := slCovReadyRecord(plan, 5150, time.Date(2026, time.August, 25, 18, 0, 0, 0, time.UTC))
	if _, err := attempt.saveReady(plan, slCovDigest("other"), record); err == nil || !strings.Contains(err.Error(), "plan changed") {
		t.Fatalf("divergent plan digest = %v", err)
	}
	divergent := plan
	divergent.Model = "other"
	if _, err := attempt.saveReady(divergent, planDigest, record); err == nil {
		t.Fatal("saveReady accepted a divergent plan")
	}
	ready, err := attempt.saveReady(plan, planDigest, record)
	if err != nil || ready.PID != record.PID {
		t.Fatalf("saveReady = %#v %v", ready, err)
	}
	if replay, err := attempt.saveReady(plan, planDigest, record); err != nil || replay.PID != record.PID {
		t.Fatalf("saveReady replay = %#v %v", replay, err)
	}
	// A pre-existing ready artifact for the same PID with different content is
	// an immutable-state conflict.
	conflicting := slCovReadyRecord(plan, 5151, record.StartedAt)
	conflicting.Model = "different"
	raw, err := encodeLaunchJSON(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := attempt.publish(readyDirectoryName, "5151.json", raw); err != nil || !created {
		t.Fatalf("inject ready = %t %v", created, err)
	}
	if _, err := attempt.saveReady(plan, planDigest, conflicting); err == nil || !strings.Contains(err.Error(), "conflicts with immutable state") {
		t.Fatalf("conflicting ready = %v", err)
	}
}

func TestSlCovLoadReadyValidation(t *testing.T) {
	t.Parallel()
	_, _, attempt, plan, _ := slCovAttempt(t)
	if _, _, err := attempt.loadReady(9); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing ready = %v", err)
	}
	if created, err := attempt.publish(readyDirectoryName, "9.json", []byte("{")); err != nil || !created {
		t.Fatalf("inject malformed = %t %v", created, err)
	}
	if _, _, err := attempt.loadReady(9); err == nil {
		t.Fatal("loadReady accepted malformed JSON")
	}
	foreign := launcherReady{SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID,
		AttemptID: "000002-00000000000000000000000000000002", AttemptIndex: 2, PID: 10}
	raw, err := encodeLaunchJSON(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := attempt.publish(readyDirectoryName, "10.json", raw); err != nil || !created {
		t.Fatalf("inject foreign = %t %v", created, err)
	}
	if _, _, err := attempt.loadReady(10); err == nil || !strings.Contains(err.Error(), "invalid launcher ready artifact") {
		t.Fatalf("foreign ready = %v", err)
	}
}

func TestSlCovSaveReleaseGatesEveryPrecondition(t *testing.T) {
	t.Parallel()
	const pid = 6161
	started := time.Date(2026, time.August, 25, 18, 0, 0, 0, time.UTC)

	t.Run("abandoned attempt cannot be released", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		injectSlCovAbandonment(t, attempt, plan, planDigest, pid)
		if _, _, err := attempt.saveRelease(plan, planDigest, launcherReady{PID: pid}, "", started); err == nil || !strings.Contains(err.Error(), "cannot be released") {
			t.Fatalf("abandoned release = %v", err)
		}
	})
	t.Run("divergent plan", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		divergent := plan
		divergent.Model = "other"
		if _, _, err := attempt.saveRelease(divergent, planDigest, launcherReady{PID: pid}, "", started); err == nil || !strings.Contains(err.Error(), "plan changed") {
			t.Fatalf("divergent plan release = %v", err)
		}
	})
	t.Run("ready artifact digest mismatch", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		record := slCovReadyRecord(plan, pid, started)
		if _, err := attempt.saveReady(plan, planDigest, record); err != nil {
			t.Fatal(err)
		}
		ready := launcherReady{SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID, AttemptID: attempt.id,
			AttemptIndex: attempt.index, RequestDigest: plan.RequestDigest, PlanDigest: slCovDigest("other"),
			PID: pid, Session: record}
		if _, _, err := attempt.saveRelease(plan, planDigest, ready, "", started); err == nil || !strings.Contains(err.Error(), "plan changed") {
			t.Fatalf("ready digest mismatch = %v", err)
		}
	})
	t.Run("missing ready", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		if _, _, err := attempt.saveRelease(plan, planDigest, launcherReady{PID: pid}, "", started); err == nil {
			t.Fatal("saveRelease accepted a missing ready artifact")
		}
	})
	t.Run("changed ready content", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		record := slCovReadyRecord(plan, pid, started)
		if _, err := attempt.saveReady(plan, planDigest, record); err != nil {
			t.Fatal(err)
		}
		tampered := record
		tampered.Model = "tampered"
		ready := launcherReady{SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID, AttemptID: attempt.id,
			AttemptIndex: attempt.index, RequestDigest: plan.RequestDigest, PlanDigest: planDigest,
			PID: pid, Session: tampered}
		if _, _, err := attempt.saveRelease(plan, planDigest, ready, "", started); err == nil || !strings.Contains(err.Error(), "ready artifact changed") {
			t.Fatalf("tampered ready = %v", err)
		}
	})
	t.Run("fence not held", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		record := slCovReadyRecord(plan, pid, started)
		if _, err := attempt.saveReady(plan, planDigest, record); err != nil {
			t.Fatal(err)
		}
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := attempt.saveRelease(plan, planDigest, slCovReady(plan, attempt, planDigest, record), "", started); err == nil || !strings.Contains(err.Error(), "did not hold its exec-success fence") {
			t.Fatalf("unheld fence release = %v", err)
		}
	})
	t.Run("replay and conflict", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		record := slCovReadyRecord(plan, pid, started)
		if _, err := attempt.saveReady(plan, planDigest, record); err != nil {
			t.Fatal(err)
		}
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		ready := slCovReady(plan, attempt, planDigest, record)
		release, replay, err := attempt.saveRelease(plan, planDigest, ready, "worklog-1", started)
		if err != nil || replay || release.TargetWorkLogRef != "worklog-1" {
			t.Fatalf("saveRelease = %#v replay=%t err=%v", release, replay, err)
		}
		replayed, replay, err := attempt.saveRelease(plan, planDigest, ready, "worklog-1", started)
		if err != nil || !replay || replayed.PID != release.PID {
			t.Fatalf("replay = %#v replay=%t err=%v", replayed, replay, err)
		}
		if _, _, err := attempt.saveRelease(plan, planDigest, ready, "worklog-2", started); err == nil || !strings.Contains(err.Error(), "conflicting immutable launcher release") {
			t.Fatalf("conflicting release = %v", err)
		}
	})
}

func TestSlCovLoadReleaseValidation(t *testing.T) {
	t.Parallel()
	_, root, attempt, plan, _ := slCovAttempt(t)
	if _, _, err := attempt.loadRelease(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing release = %v", err)
	}
	if created, err := attempt.publish("", "release.json", []byte("{")); err != nil || !created {
		t.Fatalf("inject malformed = %t %v", created, err)
	}
	if _, _, err := attempt.loadRelease(); err == nil {
		t.Fatal("loadRelease accepted malformed JSON")
	}
	stale := launcherRelease{SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID, AttemptID: attempt.id,
		AttemptIndex: attempt.index, PID: 0, ReleasedAt: time.Now()}
	raw, err := encodeLaunchJSON(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(slCovAttemptDir(root, attempt.id), "release.json")); err != nil {
		t.Fatal(err)
	}
	if created, err := attempt.publish("", "release.json", raw); err != nil || !created {
		t.Fatalf("inject stale = %t %v", created, err)
	}
	if _, _, err := attempt.loadRelease(); err == nil || !strings.Contains(err.Error(), "invalid immutable launcher release") {
		t.Fatalf("stale release = %v", err)
	}
}

func TestSlCovSaveExecFailureFlattensAndBoundsDiagnostic(t *testing.T) {
	t.Parallel()
	_, _, attempt, plan, planDigest := slCovAttempt(t)
	readyDigest := slCovDigest("ready")
	releaseDigest := slCovDigest("release")
	diagnostic := "first\r\nsecond\n" + strings.Repeat("d", 2048)
	if err := attempt.saveExecFailure(plan, planDigest, readyDigest, releaseDigest, 77, errors.New(diagnostic), time.Now()); err != nil {
		t.Fatal(err)
	}
	failure, found, err := attempt.loadExecFailure(77)
	if err != nil || !found {
		t.Fatalf("loadExecFailure = %#v found=%t err=%v", failure, found, err)
	}
	if len(failure.Diagnostic) != 1024 || strings.ContainsAny(failure.Diagnostic, "\r\n") {
		t.Fatalf("diagnostic = %q (len %d)", failure.Diagnostic, len(failure.Diagnostic))
	}
	if err := attempt.saveExecFailure(plan, slCovDigest("divergent"), readyDigest, releaseDigest, 77, errors.New("different"), time.Now()); err == nil || !strings.Contains(err.Error(), "conflicting immutable exec-failure evidence") {
		t.Fatalf("conflicting failure = %v", err)
	}
}

func TestSlCovLoadExecFailureValidation(t *testing.T) {
	t.Parallel()
	_, _, attempt, plan, _ := slCovAttempt(t)
	if _, found, err := attempt.loadExecFailure(88); err != nil || found {
		t.Fatalf("missing failure = found %t err %v", found, err)
	}
	if created, err := attempt.publish(execDirectoryName, "88.failure.json", []byte("{")); err != nil || !created {
		t.Fatalf("inject malformed = %t %v", created, err)
	}
	if _, _, err := attempt.loadExecFailure(88); err == nil {
		t.Fatal("loadExecFailure accepted malformed JSON")
	}
	invalid := launcherFailure{SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID, AttemptID: attempt.id,
		AttemptIndex: attempt.index, PID: 89, FailedAt: time.Now()}
	raw, err := encodeLaunchJSON(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := attempt.publish(execDirectoryName, "89.failure.json", raw); err != nil || !created {
		t.Fatalf("inject invalid = %t %v", created, err)
	}
	if _, _, err := attempt.loadExecFailure(89); err == nil || !strings.Contains(err.Error(), "invalid immutable launcher failure") {
		t.Fatalf("invalid failure = %v", err)
	}
}

// injectSlCovAbandonment writes one structurally valid abandonment artifact
// directly so release/abandonment conflict branches can be reached.
func injectSlCovAbandonment(t *testing.T, attempt *launchAttempt, plan launchPlan, planDigest sessionmove.Digest, pid int) {
	t.Helper()
	abandonment := launcherAbandonment{SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID,
		AttemptID: attempt.id, AttemptIndex: attempt.index, RequestDigest: plan.RequestDigest,
		PlanDigest: planDigest, PID: pid, AbandonedAt: time.Now().UTC()}
	raw, err := encodeLaunchJSON(abandonment)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := attempt.publish("", "abandoned.json", raw); err != nil || !created {
		t.Fatalf("inject abandonment = %t %v", created, err)
	}
}

func TestSlCovSaveAbandonmentRequiresExactTerminalEvidence(t *testing.T) {
	t.Parallel()
	const pid = 7171
	now := time.Date(2026, time.August, 25, 18, 0, 0, 0, time.UTC)

	t.Run("divergent plan", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		divergent := plan
		divergent.Model = "other"
		if _, _, err := attempt.saveAbandonment(divergent, planDigest, pid, now); err == nil || !strings.Contains(err.Error(), "plan changed") {
			t.Fatalf("divergent plan abandonment = %v", err)
		}
	})
	t.Run("released attempt cannot be abandoned", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		record := slCovReadyRecord(plan, pid, now)
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := attempt.saveReady(plan, planDigest, record); err != nil {
			t.Fatal(err)
		}
		if _, _, err := attempt.saveRelease(plan, planDigest, slCovReady(plan, attempt, planDigest, record), "", now); err != nil {
			t.Fatal(err)
		}
		_ = fence.Close()
		if _, _, err := attempt.saveAbandonment(plan, planDigest, pid, now); err == nil || !strings.Contains(err.Error(), "released launcher attempt cannot be abandoned") {
			t.Fatalf("released abandonment = %v", err)
		}
	})
	t.Run("started attempt cannot be abandoned", func(t *testing.T) {
		t.Parallel()
		state, root, attempt, plan, planDigest := slCovAttempt(t)
		record := slCovReadyRecord(plan, pid, now)
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := attempt.saveReady(plan, planDigest, record); err != nil {
			t.Fatal(err)
		}
		release, _, err := attempt.saveRelease(plan, planDigest, slCovReady(plan, attempt, planDigest, record), "", now)
		if err != nil {
			t.Fatal(err)
		}
		_ = fence.Close()
		_, releaseDigest, err := attempt.loadRelease()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := state.saveStarted(attempt, plan, planDigest, releaseDigest, release, now); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(slCovAttemptDir(root, attempt.id), "release.json")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := attempt.saveAbandonment(plan, planDigest, pid, now); err == nil || !strings.Contains(err.Error(), "started launcher attempt cannot be abandoned") {
			t.Fatalf("started abandonment = %v", err)
		}
	})
	t.Run("missing process evidence", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		if _, _, err := attempt.saveAbandonment(plan, planDigest, pid, now); err == nil || !strings.Contains(err.Error(), "does not bind one exact process-evidence PID") {
			t.Fatalf("missing evidence abandonment = %v", err)
		}
	})
	t.Run("held fence", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		if _, _, err := attempt.saveAbandonment(plan, planDigest, pid, now); err == nil || !strings.Contains(err.Error(), "live launcher fence cannot be abandoned") {
			t.Fatalf("held fence abandonment = %v", err)
		}
	})
	t.Run("ready conflicts with plan", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		ready := launcherReady{SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID, AttemptID: attempt.id,
			AttemptIndex: attempt.index, RequestDigest: plan.RequestDigest, PlanDigest: slCovDigest("other"), PID: pid}
		raw, err := encodeLaunchJSON(ready)
		if err != nil {
			t.Fatal(err)
		}
		if created, err := attempt.publish(readyDirectoryName, fmt.Sprintf("%d.json", pid), raw); err != nil || !created {
			t.Fatalf("inject ready = %t %v", created, err)
		}
		if _, _, err := attempt.saveAbandonment(plan, planDigest, pid, now); err == nil || !strings.Contains(err.Error(), "ready artifact conflicts") {
			t.Fatalf("conflicting ready abandonment = %v", err)
		}
	})
	t.Run("create replay and conflict", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		abandonment, replay, err := attempt.saveAbandonment(plan, planDigest, pid, now)
		if err != nil || replay || abandonment.PID != pid {
			t.Fatalf("saveAbandonment = %#v replay=%t err=%v", abandonment, replay, err)
		}
		replayed, replay, err := attempt.saveAbandonment(plan, planDigest, pid, now)
		if err != nil || !replay || replayed.PID != pid {
			t.Fatalf("replay = %#v replay=%t err=%v", replayed, replay, err)
		}
		if _, _, err := attempt.saveAbandonment(plan, planDigest, pid+1, now); err == nil {
			t.Fatal("saveAbandonment accepted a divergent PID replay")
		}
	})
}

func TestSlCovLoadAbandonmentValidation(t *testing.T) {
	t.Parallel()
	_, root, attempt, plan, _ := slCovAttempt(t)
	if _, err := attempt.loadAbandonment(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing abandonment = %v", err)
	}
	if created, err := attempt.publish("", "abandoned.json", []byte("{")); err != nil || !created {
		t.Fatalf("inject malformed = %t %v", created, err)
	}
	if _, err := attempt.loadAbandonment(); err == nil {
		t.Fatal("loadAbandonment accepted malformed JSON")
	}
	if err := os.Remove(filepath.Join(slCovAttemptDir(root, attempt.id), "abandoned.json")); err != nil {
		t.Fatal(err)
	}
	invalid := launcherAbandonment{SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID,
		AttemptID: attempt.id, AttemptIndex: attempt.index}
	raw, err := encodeLaunchJSON(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := attempt.publish("", "abandoned.json", raw); err != nil || !created {
		t.Fatalf("inject invalid = %t %v", created, err)
	}
	if _, err := attempt.loadAbandonment(); err == nil || !strings.Contains(err.Error(), "invalid immutable launcher abandonment") {
		t.Fatalf("invalid abandonment = %v", err)
	}
}

func TestSlCovSaveStartedReplaysAndRejectsDivergence(t *testing.T) {
	t.Parallel()
	state, _, attempt, plan, planDigest := slCovAttempt(t)
	const pid = 8181
	now := time.Date(2026, time.August, 25, 18, 0, 0, 0, time.UTC)
	record := slCovReadyRecord(plan, pid, now)
	fence, err := attempt.acquireExecFence(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fence.Close() }()
	if _, err := attempt.saveReady(plan, planDigest, record); err != nil {
		t.Fatal(err)
	}
	release, _, err := attempt.saveRelease(plan, planDigest, slCovReady(plan, attempt, planDigest, record), "", now)
	if err != nil {
		t.Fatal(err)
	}
	_, releaseDigest, err := attempt.loadRelease()
	if err != nil {
		t.Fatal(err)
	}
	started, replay, err := state.saveStarted(attempt, plan, planDigest, releaseDigest, release, now)
	if err != nil || replay || started.PID != pid {
		t.Fatalf("saveStarted = %#v replay=%t err=%v", started, replay, err)
	}
	replayed, replay, err := state.saveStarted(attempt, plan, planDigest, releaseDigest, release, now)
	if err != nil || !replay || replayed.PID != pid {
		t.Fatalf("started replay = %#v replay=%t err=%v", replayed, replay, err)
	}
	divergentPlan := plan
	divergentPlan.Model = "other"
	if _, _, err := state.saveStarted(attempt, divergentPlan, slCovDigest("other"), releaseDigest, release, now); err == nil || !strings.Contains(err.Error(), "different immutable started attempt") {
		t.Fatalf("divergent started = %v", err)
	}
}

func TestSlCovLoadStartedValidation(t *testing.T) {
	t.Parallel()
	state, root, _, plan, _ := slCovAttempt(t)
	if _, err := state.loadStarted(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing started = %v", err)
	}
	if created, err := state.publish("", "started.json", []byte("{")); err != nil || !created {
		t.Fatalf("inject malformed = %t %v", created, err)
	}
	if _, err := state.loadStarted(); err == nil {
		t.Fatal("loadStarted accepted malformed JSON")
	}
	if err := os.Remove(filepath.Join(slCovStateDir(root), "started.json")); err != nil {
		t.Fatal(err)
	}
	invalid := launcherStarted{SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID,
		AttemptID: "malformed", AttemptIndex: 1, PID: 5, StartedAt: time.Now()}
	raw, err := encodeLaunchJSON(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := state.publish("", "started.json", raw); err != nil || !created {
		t.Fatalf("inject invalid = %t %v", created, err)
	}
	if _, err := state.loadStarted(); err == nil || !strings.Contains(err.Error(), "invalid immutable started marker") {
		t.Fatalf("invalid started = %v", err)
	}
}

func TestSlCovOpenLaunchStateRejectsUnsafeRootsAndIDs(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), sessionmove.DirName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := openLaunchState("relative", "handoff-123", false); err == nil {
		t.Fatal("openLaunchState accepted a relative root")
	}
	if _, err := openLaunchState(root+"/", "handoff-123", false); err == nil {
		t.Fatal("openLaunchState accepted an unclean root")
	}
	for _, handoffID := range []string{"", ".", "..", "nested/id"} {
		if _, err := openLaunchState(root, handoffID, false); err == nil {
			t.Fatalf("openLaunchState accepted handoff ID %q", handoffID)
		}
	}
	if _, err := openLaunchState(root, "absent", false); err == nil {
		t.Fatal("openLaunchState accepted a missing handoff directory")
	}
	if _, err := openLaunchStateFromHandoff("handoff-123", nil, false); err == nil {
		t.Fatal("openLaunchStateFromHandoff accepted a nil directory")
	}
}

func TestSlCovLaunchStateDirectoryUnavailability(t *testing.T) {
	t.Parallel()
	if err := (*launchState)(nil).Close(); err != nil {
		t.Fatalf("nil state Close = %v", err)
	}
	empty := &launchState{}
	if _, err := empty.directory(""); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing launch directory = %v", err)
	}
	if _, err := empty.directory(attemptsDirectoryName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing attempts directory = %v", err)
	}
	if _, err := empty.listAttempts(); err != nil {
		t.Fatalf("listAttempts without attempts = %v", err)
	}
	if _, err := empty.createAttempt(); err == nil {
		t.Fatal("createAttempt without an attempts directory succeeded")
	}
	if _, err := empty.openAttempt("000001-00000000000000000000000000000001"); err == nil || !strings.Contains(err.Error(), "attempts directory is unavailable") {
		t.Fatalf("openAttempt without attempts = %v", err)
	}
	state, _ := slCovOpenState(t)
	if err := state.Close(); err != nil {
		t.Fatalf("first Close = %v", err)
	}
	if err := state.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if _, err := state.directory(attemptsDirectoryName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed attempts directory = %v", err)
	}
}

func TestSlCovOpenAttemptRejectsMalformedAndIncompleteClaims(t *testing.T) {
	t.Parallel()
	state, _ := slCovOpenState(t)
	if _, err := state.openAttempt("nope"); err == nil {
		t.Fatal("openAttempt accepted a malformed ID")
	}
	if _, err := state.openAttempt("000003-00000000000000000000000000000003"); err == nil {
		t.Fatal("openAttempt accepted a missing attempt")
	}
	const claimedID = "000001-00000000000000000000000000000001"
	if err := unix.Mkdirat(int(state.attempts.Fd()), claimedID, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := state.openAttempt(claimedID); err == nil {
		t.Fatal("openAttempt accepted an attempt without fixed children")
	}
	recovered, err := state.openOrRecoverClaimedAttempt(claimedID)
	if err != nil {
		t.Fatal(err)
	}
	_ = recovered.Close()
}

func TestSlCovOpenOrRecoverClaimedAttemptRejectsAmbiguity(t *testing.T) {
	t.Parallel()
	t.Run("malformed ID", func(t *testing.T) {
		t.Parallel()
		state, _ := slCovOpenState(t)
		if _, err := state.openOrRecoverClaimedAttempt("nope"); err == nil {
			t.Fatal("accepted a malformed attempt ID")
		}
	})
	t.Run("missing attempt", func(t *testing.T) {
		t.Parallel()
		state, _ := slCovOpenState(t)
		if _, err := state.openOrRecoverClaimedAttempt("000004-00000000000000000000000000000004"); err == nil {
			t.Fatal("accepted a missing attempt")
		}
	})
	t.Run("unknown artifact", func(t *testing.T) {
		t.Parallel()
		state, root := slCovOpenState(t)
		const claimedID = "000001-00000000000000000000000000000001"
		if err := unix.Mkdirat(int(state.attempts.Fd()), claimedID, 0o700); err != nil {
			t.Fatal(err)
		}
		slCovWrite(t, filepath.Join(slCovAttemptDir(root, claimedID), "mystery"), 0o600, "x")
		if _, err := state.openOrRecoverClaimedAttempt(claimedID); err == nil || !strings.Contains(err.Error(), "ambiguous artifact") {
			t.Fatalf("unknown artifact = %v", err)
		}
	})
	t.Run("non-empty child", func(t *testing.T) {
		t.Parallel()
		state, root := slCovOpenState(t)
		const claimedID = "000001-00000000000000000000000000000001"
		if err := os.MkdirAll(filepath.Join(slCovAttemptDir(root, claimedID), readyDirectoryName), 0o700); err != nil {
			t.Fatal(err)
		}
		slCovWrite(t, filepath.Join(slCovAttemptDir(root, claimedID), readyDirectoryName, "leftover.json"), 0o600, "x")
		if _, err := state.openOrRecoverClaimedAttempt(claimedID); err == nil || !strings.Contains(err.Error(), "ambiguous state in") {
			t.Fatalf("non-empty child = %v", err)
		}
	})
}

func TestSlCovListAttemptsRejectsUncontiguousHistory(t *testing.T) {
	t.Parallel()
	t.Run("unexpected entry", func(t *testing.T) {
		t.Parallel()
		state, root := slCovOpenState(t)
		if err := os.Mkdir(filepath.Join(slCovStateDir(root), attemptsDirectoryName, "not-an-attempt"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := state.listAttempts(); err == nil || !strings.Contains(err.Error(), "unexpected launch attempt entry") {
			t.Fatalf("unexpected entry = %v", err)
		}
	})
	t.Run("non contiguous", func(t *testing.T) {
		t.Parallel()
		state, _ := slCovOpenState(t)
		const second = "000002-00000000000000000000000000000002"
		if err := unix.Mkdirat(int(state.attempts.Fd()), second, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := state.listAttempts(); err == nil || !strings.Contains(err.Error(), "not contiguous") {
			t.Fatalf("non contiguous history = %v", err)
		}
	})
}

func TestSlCovOpenPrivateDirectoryAtEnforcesPrivateShape(t *testing.T) {
	t.Parallel()
	parent, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	if _, err := openPrivateDirectoryAt(int(parent.Fd()), "absent", false); err == nil {
		t.Fatal("accepted a missing directory")
	}
	if err := os.Mkdir(filepath.Join(parent.Name(), "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := openPrivateDirectoryAt(int(parent.Fd()), "public", false); err == nil || !strings.Contains(err.Error(), "private 0700") {
		t.Fatalf("public directory = %v", err)
	}
	slCovWrite(t, filepath.Join(parent.Name(), "file"), 0o600, "x")
	if _, err := openPrivateDirectoryAt(int(parent.Fd()), "file", false); err == nil {
		t.Fatal("accepted a regular file")
	}
	fd, err := openPrivateDirectoryAt(int(parent.Fd()), "private", true)
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	if fd, err := openPrivateDirectoryAt(int(parent.Fd()), "private", true); err != nil {
		t.Fatalf("recreate existing private directory = %v", err)
	} else {
		_ = unix.Close(fd)
	}
}

func TestSlCovPreReleaseProcessEvidenceBindsOneExactPID(t *testing.T) {
	t.Parallel()
	const pid = 6262
	t.Run("none", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, _, _ := slCovAttempt(t)
		foundPID, found, err := attempt.preReleaseProcessEvidence()
		if err != nil || found || foundPID != 0 {
			t.Fatalf("no evidence = %d %t %v", foundPID, found, err)
		}
	})
	t.Run("ready only is ambiguous", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		if _, err := attempt.saveReady(plan, planDigest, slCovReadyRecord(plan, pid, time.Now())); err != nil {
			t.Fatal(err)
		}
		if _, _, err := attempt.preReleaseProcessEvidence(); err == nil || !strings.Contains(err.Error(), "does not bind one exact PID") {
			t.Fatalf("ready only = %v", err)
		}
	})
	t.Run("lock only is ambiguous", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, _, _ := slCovAttempt(t)
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		foundPID, found, err := attempt.preReleaseProcessEvidence()
		if err != nil || !found || foundPID != pid {
			t.Fatalf("lock-only evidence = %d %t %v", foundPID, found, err)
		}
	})
	t.Run("matching pair", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		if _, err := attempt.saveReady(plan, planDigest, slCovReadyRecord(plan, pid, time.Now())); err != nil {
			t.Fatal(err)
		}
		foundPID, found, err := attempt.preReleaseProcessEvidence()
		if err != nil || !found || foundPID != pid {
			t.Fatalf("matching pair = %d %t %v", foundPID, found, err)
		}
	})
	t.Run("divergent pair", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		fence, err := attempt.acquireExecFence(pid)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		if _, err := attempt.saveReady(plan, planDigest, slCovReadyRecord(plan, pid+1, time.Now())); err != nil {
			t.Fatal(err)
		}
		if _, _, err := attempt.preReleaseProcessEvidence(); err == nil || !strings.Contains(err.Error(), "does not bind one exact PID") {
			t.Fatalf("divergent pair = %v", err)
		}
	})
	t.Run("ambiguous ready entries", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, _, _ := slCovAttempt(t)
		if created, err := attempt.publish(readyDirectoryName, "1.json", []byte("{}")); err != nil || !created {
			t.Fatalf("inject ready one = %t %v", created, err)
		}
		if created, err := attempt.publish(readyDirectoryName, "2.json", []byte("{}")); err != nil || !created {
			t.Fatalf("inject ready two = %t %v", created, err)
		}
		if _, _, err := attempt.preReleaseProcessEvidence(); err == nil || !strings.Contains(err.Error(), "ambiguous pre-release ready evidence") {
			t.Fatalf("ambiguous ready = %v", err)
		}
	})
	t.Run("malformed ready entry", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, _, _ := slCovAttempt(t)
		if created, err := attempt.publish(readyDirectoryName, "garbage", []byte("{}")); err != nil || !created {
			t.Fatalf("inject ready = %t %v", created, err)
		}
		if _, _, err := attempt.preReleaseProcessEvidence(); err == nil {
			t.Fatal("malformed ready entry was accepted")
		}
	})
	t.Run("ambiguous exec entries", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, _, _ := slCovAttempt(t)
		if created, err := attempt.publish(execDirectoryName, "1.lock", []byte("")); err != nil || !created {
			t.Fatalf("inject lock one = %t %v", created, err)
		}
		if created, err := attempt.publish(execDirectoryName, "2.lock", []byte("")); err != nil || !created {
			t.Fatalf("inject lock two = %t %v", created, err)
		}
		if _, _, err := attempt.preReleaseProcessEvidence(); err == nil || !strings.Contains(err.Error(), "ambiguous pre-release exec evidence") {
			t.Fatalf("ambiguous exec = %v", err)
		}
	})
}

func TestSlCovExecFenceSemantics(t *testing.T) {
	t.Parallel()
	state, _ := slCovOpenState(t)
	attempt, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attempt.Close() }()
	bad := &launchAttempt{state: state}
	if _, err := bad.acquireExecFence(1); err == nil {
		t.Fatal("acquireExecFence accepted a nil exec directory")
	}
	if _, err := bad.execFenceHeld(1); err == nil {
		t.Fatal("execFenceHeld accepted a nil exec directory")
	}
	fence, err := attempt.acquireExecFence(31)
	if err != nil {
		t.Fatal(err)
	}
	held, err := attempt.execFenceHeld(31)
	if err != nil || !held {
		t.Fatalf("held fence = %t %v", held, err)
	}
	if _, err := attempt.acquireExecFence(31); err == nil || !strings.Contains(err.Error(), "acquire launcher exec-success fence") {
		t.Fatalf("double acquire = %v", err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	held, err = attempt.execFenceHeld(31)
	if err != nil || held {
		t.Fatalf("released fence = %t %v", held, err)
	}
	if _, err := attempt.execFenceHeld(32); err == nil {
		t.Fatal("execFenceHeld accepted a missing lock file")
	}
}

func TestSlCovReadLaunchArtifactRejectsUnsafeShapes(t *testing.T) {
	t.Parallel()
	state, root := slCovOpenState(t)
	if err := os.Symlink(filepath.Join(slCovStateDir(root), "plan.json"), filepath.Join(slCovStateDir(root), "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := state.read("", "link"); err == nil {
		t.Fatal("readLaunchArtifact followed a symlink")
	}
	if err := os.Mkdir(filepath.Join(slCovStateDir(root), "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := state.read("", "subdir"); err == nil {
		t.Fatal("readLaunchArtifact accepted a directory")
	}
	slCovWrite(t, filepath.Join(slCovStateDir(root), "wide.json"), 0o644, "{}")
	if _, err := state.read("", "wide.json"); err == nil {
		t.Fatal("readLaunchArtifact accepted mode 0644")
	}
	slCovWrite(t, filepath.Join(slCovStateDir(root), "oversized.json"), 0o600, strings.Repeat("x", maxLaunchArtifactBytes+1))
	if _, err := state.read("", "oversized.json"); err == nil {
		t.Fatal("readLaunchArtifact accepted an oversized artifact")
	}
	if _, err := state.read("", "absent.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing artifact = %v", err)
	}
}

func TestSlCovPathWrappersSurfaceMissingHistory(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), sessionmove.DirName)
	plan := slCovPlan("handoff-123")
	if _, _, _, err := savePlan(root, plan); err == nil {
		t.Fatal("savePlan accepted a missing store root")
	}
	if _, _, err := loadPlan(root, "handoff-123"); err == nil {
		t.Fatal("loadPlan accepted a missing store root")
	}
	if _, _, err := loadReady(root, "handoff-123", 1); err == nil {
		t.Fatal("loadReady accepted a missing store root")
	}
	if _, _, err := saveRelease(root, plan, slCovDigest("plan"), launcherReady{}, "", time.Now()); err == nil {
		t.Fatal("saveRelease accepted a missing store root")
	}
	if _, _, err := loadRelease(root, "handoff-123"); err == nil {
		t.Fatal("loadRelease accepted a missing store root")
	}

	state, root := slCovOpenState(t)
	_, planDigest, _, err := state.savePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadReady(root, "handoff-123", 1); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loadReady without attempts = %v", err)
	}
	if _, _, err := loadRelease(root, "handoff-123"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loadRelease without attempts = %v", err)
	}
	attempt, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	record := slCovReadyRecord(plan, 1234, time.Now())
	if _, err := attempt.saveReady(plan, planDigest, record); err != nil {
		t.Fatal(err)
	}
	fence, err := attempt.acquireExecFence(record.PID)
	if err != nil {
		t.Fatal(err)
	}
	release, replay, err := saveRelease(root, plan, planDigest, slCovReady(plan, attempt, planDigest, record), "worklog", time.Now())
	if err != nil || replay || release.PID != record.PID {
		t.Fatalf("saveRelease wrapper = %#v replay=%t err=%v", release, replay, err)
	}
	replayed, replay, err := saveRelease(root, plan, planDigest, slCovReady(plan, attempt, planDigest, record), "worklog", time.Now())
	if err != nil || !replay || replayed.PID != release.PID {
		t.Fatalf("saveRelease replay wrapper = %#v replay=%t err=%v", replayed, replay, err)
	}
	_ = fence.Close()
	loaded, _, err := loadReady(root, "handoff-123", record.PID)
	if err != nil || loaded.PID != record.PID {
		t.Fatalf("loadReady wrapper = %#v %v", loaded, err)
	}
	_ = attempt.Close()
}

func slCovStateDir(root string) string {
	return filepath.Join(root, "handoff-123", launchDirectoryName)
}

func slCovAttemptDir(root, attemptID string) string {
	return filepath.Join(slCovStateDir(root), attemptsDirectoryName, attemptID)
}
