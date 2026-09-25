package sessionlaunch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

// slCovReadOnly removes write permission from an already-open private
// directory so publication through its retained descriptor fails.
func slCovReadOnly(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
}

func TestSlCovPublishFailuresPropagateFromEveryWriter(t *testing.T) {
	t.Parallel()
	t.Run("plan", func(t *testing.T) {
		t.Parallel()
		state, root := slCovOpenState(t)
		slCovReadOnly(t, slCovStateDir(root))
		if _, _, _, err := state.savePlan(slCovPlan("handoff-123")); err == nil || !strings.Contains(err.Error(), "persist immutable launch plan") {
			t.Fatalf("read-only plan publish = %v", err)
		}
	})
	t.Run("ready", func(t *testing.T) {
		t.Parallel()
		_, root, attempt, plan, planDigest := slCovAttempt(t)
		slCovReadOnly(t, filepath.Join(slCovAttemptDir(root, attempt.id), readyDirectoryName))
		record := slCovReadyRecord(plan, 111, time.Now())
		if _, err := attempt.saveReady(plan, planDigest, record); err == nil {
			t.Fatal("read-only ready publish succeeded")
		}
	})
	t.Run("release", func(t *testing.T) {
		t.Parallel()
		_, root, attempt, plan, planDigest := slCovAttempt(t)
		record := slCovReadyRecord(plan, 112, time.Now())
		if _, err := attempt.saveReady(plan, planDigest, record); err != nil {
			t.Fatal(err)
		}
		fence, err := attempt.acquireExecFence(record.PID)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		slCovReadOnly(t, slCovAttemptDir(root, attempt.id))
		if _, _, err := attempt.saveRelease(plan, planDigest, slCovReady(plan, attempt, planDigest, record), "", time.Now()); err == nil {
			t.Fatal("read-only release publish succeeded")
		}
	})
	t.Run("exec failure", func(t *testing.T) {
		t.Parallel()
		_, root, attempt, plan, planDigest := slCovAttempt(t)
		slCovReadOnly(t, filepath.Join(slCovAttemptDir(root, attempt.id), execDirectoryName))
		if err := attempt.saveExecFailure(plan, planDigest, slCovDigest("r"), slCovDigest("rel"), 113, errors.New("boom"), time.Now()); err == nil {
			t.Fatal("read-only exec-failure publish succeeded")
		}
	})
	t.Run("abandonment", func(t *testing.T) {
		t.Parallel()
		_, root, attempt, plan, planDigest := slCovAttempt(t)
		fence, err := attempt.acquireExecFence(114)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		slCovReadOnly(t, slCovAttemptDir(root, attempt.id))
		if _, _, err := attempt.saveAbandonment(plan, planDigest, 114, time.Now()); err == nil {
			t.Fatal("read-only abandonment publish succeeded")
		}
	})
	t.Run("started", func(t *testing.T) {
		t.Parallel()
		state, root, attempt, plan, planDigest := slCovAttempt(t)
		record := slCovReadyRecord(plan, 115, time.Now())
		fence, err := attempt.acquireExecFence(record.PID)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fence.Close() }()
		if _, err := attempt.saveReady(plan, planDigest, record); err != nil {
			t.Fatal(err)
		}
		release, _, err := attempt.saveRelease(plan, planDigest, slCovReady(plan, attempt, planDigest, record), "", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_, releaseDigest, err := attempt.loadRelease()
		if err != nil {
			t.Fatal(err)
		}
		slCovReadOnly(t, slCovStateDir(root))
		if _, _, err := state.saveStarted(attempt, plan, planDigest, releaseDigest, release, time.Now()); err == nil {
			t.Fatal("read-only started publish succeeded")
		}
	})
}

func TestSlCovSaveReadyWithoutPlanFailsClosed(t *testing.T) {
	t.Parallel()
	state, _ := slCovOpenState(t)
	attempt, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = attempt.Close() })
	if _, err := attempt.saveReady(slCovPlan("handoff-123"), slCovDigest("plan"), slCovReadyRecord(slCovPlan("handoff-123"), 1, time.Now())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("saveReady without a plan = %v", err)
	}
	if _, _, err := attempt.saveAbandonment(slCovPlan("handoff-123"), slCovDigest("plan"), 1, time.Now()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("saveAbandonment without a plan = %v", err)
	}
}

func TestSlCovSaveReleaseSurfacesMissingReadyAndFence(t *testing.T) {
	t.Parallel()
	t.Run("missing ready", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		record := slCovReadyRecord(plan, 121, time.Now())
		ready := slCovReady(plan, attempt, planDigest, record)
		if _, _, err := attempt.saveRelease(plan, planDigest, ready, "", time.Now()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("release without ready = %v", err)
		}
	})
	t.Run("missing exec fence file", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		record := slCovReadyRecord(plan, 122, time.Now())
		if _, err := attempt.saveReady(plan, planDigest, record); err != nil {
			t.Fatal(err)
		}
		ready := slCovReady(plan, attempt, planDigest, record)
		if _, _, err := attempt.saveRelease(plan, planDigest, ready, "", time.Now()); err == nil {
			t.Fatal("release without an exec fence file succeeded")
		}
	})
	t.Run("corrupt abandonment", func(t *testing.T) {
		t.Parallel()
		_, root, attempt, plan, planDigest := slCovAttempt(t)
		slCovWrite(t, filepath.Join(slCovAttemptDir(root, attempt.id), "abandoned.json"), 0o600, "{}\n")
		if _, _, err := attempt.saveRelease(plan, planDigest, launcherReady{}, "", time.Now()); err == nil {
			t.Fatal("release with a corrupt abandonment artifact succeeded")
		}
	})
	t.Run("missing plan", func(t *testing.T) {
		t.Parallel()
		state, _ := slCovOpenState(t)
		attempt, err := state.createAttempt()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = attempt.Close() }()
		if _, _, err := attempt.saveRelease(slCovPlan("handoff-123"), slCovDigest("plan"), launcherReady{}, "", time.Now()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("release without a plan = %v", err)
		}
	})
}

func TestSlCovSaveExecFailureHandlesCorruptExistingEvidence(t *testing.T) {
	t.Parallel()
	t.Run("corrupt existing", func(t *testing.T) {
		t.Parallel()
		state, root, attempt, plan, planDigest := slCovAttempt(t)
		_ = state
		slCovWrite(t, filepath.Join(slCovAttemptDir(root, attempt.id), execDirectoryName, "131.failure.json"), 0o600, "{}\n")
		if err := attempt.saveExecFailure(plan, planDigest, slCovDigest("r"), slCovDigest("rel"), 131, errors.New("boom"), time.Now()); err == nil {
			t.Fatal("saveExecFailure accepted corrupt existing evidence")
		}
	})
	t.Run("matching replay", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		readyDigest, releaseDigest := slCovDigest("r"), slCovDigest("rel")
		if err := attempt.saveExecFailure(plan, planDigest, readyDigest, releaseDigest, 132, errors.New("first"), time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := attempt.saveExecFailure(plan, planDigest, readyDigest, releaseDigest, 132, errors.New("second, different text"), time.Now()); err != nil {
			t.Fatalf("matching replay = %v", err)
		}
	})
	t.Run("unreadable existing", func(t *testing.T) {
		t.Parallel()
		_, root, attempt, _, _ := slCovAttempt(t)
		slCovWrite(t, filepath.Join(slCovAttemptDir(root, attempt.id), execDirectoryName, "133.failure.json"), 0o644, "{}\n")
		if _, _, err := attempt.loadExecFailure(133); err == nil {
			t.Fatal("loadExecFailure accepted a non-private artifact")
		}
	})
}

//nolint:paralleltest // kept serial: this test's exec-fence acquire/Close/held sequence races any sibling parallel test's fork() (which duplicates this fd into the forked child until its own exec), making the fence appear falsely held after Close (task-21, #739; proven test-only -- see acquireExecFence's doc comment in state.go for why production cannot hit this); serial removes every such sibling from the race window
func TestSlCovSaveAbandonmentSurfacesCorruptNeighbourArtifacts(t *testing.T) {
	t.Run("corrupt release", func(t *testing.T) {
		t.Parallel()
		_, root, attempt, plan, planDigest := slCovAttempt(t)
		slCovWrite(t, filepath.Join(slCovAttemptDir(root, attempt.id), "release.json"), 0o600, "{}\n")
		if _, _, err := attempt.saveAbandonment(plan, planDigest, 141, time.Now()); err == nil {
			t.Fatal("saveAbandonment accepted a corrupt release artifact")
		}
	})
	t.Run("corrupt started", func(t *testing.T) {
		t.Parallel()
		_, root, attempt, plan, planDigest := slCovAttempt(t)
		slCovWrite(t, filepath.Join(slCovStateDir(root), "started.json"), 0o600, "{}\n")
		if _, _, err := attempt.saveAbandonment(plan, planDigest, 142, time.Now()); err == nil {
			t.Fatal("saveAbandonment accepted a corrupt started marker")
		}
	})
	t.Run("ambiguous evidence", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		for _, name := range []string{"151.json", "152.json"} {
			if created, err := attempt.publish(readyDirectoryName, name, []byte("{}")); err != nil || !created {
				t.Fatalf("inject %s = %t %v", name, created, err)
			}
		}
		if _, _, err := attempt.saveAbandonment(plan, planDigest, 151, time.Now()); err == nil {
			t.Fatal("saveAbandonment accepted ambiguous process evidence")
		}
	})
	t.Run("unreadable lock", func(t *testing.T) {
		t.Parallel()
		_, root, attempt, plan, planDigest := slCovAttempt(t)
		slCovWrite(t, filepath.Join(slCovAttemptDir(root, attempt.id), execDirectoryName, "153.lock"), 0o644, "")
		if _, _, err := attempt.saveAbandonment(plan, planDigest, 153, time.Now()); err == nil {
			t.Fatal("saveAbandonment accepted a non-private lock")
		}
	})
	t.Run("corrupt ready", func(t *testing.T) {
		t.Parallel()
		_, root, attempt, plan, planDigest := slCovAttempt(t)
		fence, err := attempt.acquireExecFence(154)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		slCovWrite(t, filepath.Join(slCovAttemptDir(root, attempt.id), readyDirectoryName, "154.json"), 0o600, "{}\n")
		if _, _, err := attempt.saveAbandonment(plan, planDigest, 154, time.Now()); err == nil {
			t.Fatal("saveAbandonment accepted a corrupt ready artifact")
		}
	})
	t.Run("ready digest conflict on replay", func(t *testing.T) {
		t.Parallel()
		_, _, attempt, plan, planDigest := slCovAttempt(t)
		fence, err := attempt.acquireExecFence(155)
		if err != nil {
			t.Fatal(err)
		}
		if err := fence.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := attempt.saveAbandonment(plan, planDigest, 155, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := attempt.saveReady(plan, planDigest, slCovReadyRecord(plan, 155, time.Now())); err != nil {
			t.Fatal(err)
		}
		if _, _, err := attempt.saveAbandonment(plan, planDigest, 155, time.Now()); err == nil || !strings.Contains(err.Error(), "conflicting immutable abandonment evidence") {
			t.Fatalf("ready digest conflict = %v", err)
		}
	})
}

func TestSlCovSaveStartedSurfacesCorruptStartedMarker(t *testing.T) {
	t.Parallel()
	state, root, attempt, plan, planDigest := slCovAttempt(t)
	record := slCovReadyRecord(plan, 161, time.Now())
	fence, err := attempt.acquireExecFence(record.PID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fence.Close() })
	if _, err := attempt.saveReady(plan, planDigest, record); err != nil {
		t.Fatal(err)
	}
	release, _, err := attempt.saveRelease(plan, planDigest, slCovReady(plan, attempt, planDigest, record), "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, releaseDigest, err := attempt.loadRelease()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.saveStarted(attempt, plan, planDigest, releaseDigest, release, time.Now()); err != nil {
		t.Fatal(err)
	}
	slCovWrite(t, filepath.Join(slCovStateDir(root), "started.json"), 0o600, "{}\n")
	if _, _, err := state.saveStarted(attempt, plan, planDigest, releaseDigest, release, time.Now()); err == nil {
		t.Fatal("saveStarted accepted a corrupt existing marker")
	}
}

func TestSlCovDecodeLaunchJSONAcceptsCanonicalEncoding(t *testing.T) {
	t.Parallel()
	plan := slCovPlan("handoff-123")
	raw, err := encodeLaunchJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	var decoded launchPlan
	if err := decodeLaunchJSON(raw, &decoded); err != nil || !equalLaunchPlan(decoded, plan) {
		t.Fatalf("decodeLaunchJSON = %#v %v", decoded, err)
	}
}

func TestSlCovOpenLaunchStateRejectsLaunchPathCollision(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), sessionmove.DirName)
	if err := os.MkdirAll(filepath.Join(root, "handoff-collide"), 0o700); err != nil {
		t.Fatal(err)
	}
	slCovWrite(t, filepath.Join(root, "handoff-collide", launchDirectoryName), 0o600, "not a directory")
	if _, err := openLaunchState(root, "handoff-collide", true); err == nil || !strings.Contains(err.Error(), "open private launch directory") {
		t.Fatalf("launch path collision = %v", err)
	}
}

func TestSlCovOpenLaunchStateWithoutAttemptsDirectory(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), sessionmove.DirName)
	if err := os.MkdirAll(filepath.Join(root, "handoff-nodir", launchDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := openLaunchState(root, "handoff-nodir", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	if state.attempts != nil {
		t.Fatalf("attempts directory unexpectedly present: %v", state.attempts)
	}
	if refs, err := state.listAttempts(); err != nil || refs != nil {
		t.Fatalf("listAttempts without attempts = %#v %v", refs, err)
	}
}

func TestSlCovOpenAttemptRequiresFixedChildren(t *testing.T) {
	t.Parallel()
	state, root := slCovOpenState(t)
	const claimedID = "000001-00000000000000000000000000000001"
	if err := os.MkdirAll(filepath.Join(slCovAttemptDir(root, claimedID), readyDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := state.openAttempt(claimedID); err == nil || !strings.Contains(err.Error(), "open private launch directory "+execDirectoryName) {
		t.Fatalf("attempt without exec directory = %v", err)
	}
}

func TestSlCovOpenOrRecoverRejectsNonDirectoryChild(t *testing.T) {
	t.Parallel()
	state, root := slCovOpenState(t)
	const claimedID = "000001-00000000000000000000000000000001"
	if err := os.MkdirAll(slCovAttemptDir(root, claimedID), 0o700); err != nil {
		t.Fatal(err)
	}
	slCovWrite(t, filepath.Join(slCovAttemptDir(root, claimedID), readyDirectoryName), 0o600, "not a directory")
	if _, err := state.openOrRecoverClaimedAttempt(claimedID); err == nil {
		t.Fatal("openOrRecoverClaimedAttempt accepted a non-directory child")
	}
}

func TestSlCovCreateAttemptRequiresContiguousHistory(t *testing.T) {
	t.Parallel()
	state, root := slCovOpenState(t)
	if err := os.Mkdir(filepath.Join(slCovStateDir(root), attemptsDirectoryName, "garbage"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := state.createAttempt(); err == nil {
		t.Fatal("createAttempt accepted a malformed attempt history")
	}
}

func TestSlCovListAttemptsSurfacesClosedDescriptor(t *testing.T) {
	t.Parallel()
	state, _ := slCovOpenState(t)
	if err := state.attempts.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := state.listAttempts(); err == nil {
		t.Fatal("listAttempts on a closed descriptor succeeded")
	}
	if _, err := latestAttempt(state); err == nil {
		t.Fatal("latestAttempt on a closed descriptor succeeded")
	}
}

func TestSlCovNilAttemptCloseIsSafe(t *testing.T) {
	t.Parallel()
	if err := (*launchAttempt)(nil).Close(); err != nil {
		t.Fatalf("nil attempt Close = %v", err)
	}
}

func TestSlCovExecFenceRejectsNonPrivateLockFile(t *testing.T) {
	t.Parallel()
	state, root := slCovOpenState(t)
	attempt, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = attempt.Close() })
	lockPath := filepath.Join(slCovAttemptDir(root, attempt.id), execDirectoryName, "171.lock")
	slCovWrite(t, lockPath, 0o644, "")
	if _, err := attempt.acquireExecFence(171); err == nil {
		t.Fatal("acquireExecFence accepted a non-private lock file")
	}
	if _, err := attempt.execFenceHeld(171); err == nil {
		t.Fatal("execFenceHeld accepted a non-private lock file")
	}
}

func TestSlCovSaveReleaseWrapperRejectsMalformedAttemptIdentity(t *testing.T) {
	t.Parallel()
	state, root := slCovOpenState(t)
	plan := slCovPlan("handoff-123")
	_, planDigest, _, err := state.savePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := saveRelease(root, plan, planDigest, launcherReady{AttemptID: "malformed"}, "", time.Now()); err == nil {
		t.Fatal("saveRelease wrapper accepted a malformed attempt identity")
	}
}

func TestSlCovLatestAttemptSurfacesMalformedHistory(t *testing.T) {
	t.Parallel()
	state, root := slCovOpenState(t)
	if err := os.Mkdir(filepath.Join(slCovStateDir(root), attemptsDirectoryName, "garbage"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := latestAttempt(state); err == nil {
		t.Fatal("latestAttempt accepted a malformed attempt history")
	}
}

func TestSlCovPublishThroughClosedLaunchDirectoryFails(t *testing.T) {
	t.Parallel()
	state, root := slCovOpenState(t)
	slCovReadOnly(t, slCovStateDir(root))
	if _, err := state.publish("", "plan.json", []byte("{}\n")); err == nil {
		t.Fatal("publish through a read-only launch directory succeeded")
	}
}
