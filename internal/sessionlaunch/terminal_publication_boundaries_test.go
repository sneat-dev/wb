package sessionlaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/unixcompat"
)

func TestReleasedAttemptFinalizesOnlyAfterHealthyLiveRegistration(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		name := "healthy"
		if fail {
			name = "started publication refused"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fx := newLauncherRetryFixture(t)
			record, err := session.Register(fx.sessions, slCovReadyRecord(fx.plan, os.Getpid(), fx.deps.now()))
			if err != nil {
				t.Fatal(err)
			}
			attempt, release, _, _ := slCovReleasedAttempt(t, fx, record.PID)
			t.Cleanup(func() { _ = attempt.state.Close() })
			fence, err := attempt.acquireExecFence(record.PID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = fence.Close() })
			// Registration is authoritative; replace readiness with the exact saved record.
			readyPath := filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), readyDirectoryName, itoaSlCovLauncher(record.PID)+".json")
			if err := os.Remove(readyPath); err != nil {
				t.Fatal(err)
			}
			ready, err := attempt.saveReady(fx.plan, fx.planDigest, record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), "release.json")); err != nil {
				t.Fatal(err)
			}
			release, _, err = attempt.saveRelease(fx.plan, fx.planDigest, ready, "worklog", fx.deps.now())
			if err != nil {
				t.Fatal(err)
			}
			if err := fence.Close(); err != nil {
				t.Fatal(err)
			}
			fx.tmux.pid = record.PID
			if fail {
				fx.deps.now = func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
			}
			selected, reused, result, done, err := selectAttemptForStart(context.Background(), fx.options(nil), fx.deps, attempt.state, fx.plan, fx.planDigest)
			if selected != nil || !reused {
				t.Fatalf("selected=%v reused=%t", selected, reused)
			}
			if fail {
				if err == nil || done {
					t.Fatalf("publication refusal = %v, done=%t", err, done)
				}
				if _, err := attempt.state.loadStarted(); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("started marker = %v", err)
				}
			} else {
				if err != nil || !done || result.PID != release.PID {
					t.Fatalf("completion = %#v, %t, %v", result, done, err)
				}
			}
		})
	}
}

func TestExecFailureObservationRequiresReadableImmutableRelease(t *testing.T) {
	t.Parallel()
	fx := newLauncherRetryFixture(t)
	attempt, release, digest, ready := slCovReleasedAttempt(t, fx, 8104)
	t.Cleanup(func() { _ = attempt.state.Close() })
	if err := attempt.saveExecFailure(fx.plan, fx.planDigest, release.ReadyDigest, digest, release.PID, errors.New("native exec refused"), fx.deps.now()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), "release.json")); err != nil {
		t.Fatal(err)
	}
	if err := waitExecSuccess(context.Background(), fx.deps, attempt, fx.plan, fx.planDigest, ready, release); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("release read = %v", err)
	}
}

func TestTerminalPaneObservationRequiresReadableImmutableRelease(t *testing.T) {
	t.Parallel()
	fx := newLauncherRetryFixture(t)
	attempt, release, _, ready := slCovReleasedAttempt(t, fx, 8105)
	t.Cleanup(func() { _ = attempt.state.Close() })
	fx.tmux.failure = &tmuxFailure{ExitStatus: 1}
	if err := os.Remove(filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), "release.json")); err != nil {
		t.Fatal(err)
	}
	if err := waitExecSuccess(context.Background(), fx.deps, attempt, fx.plan, fx.planDigest, ready, release); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("release read = %v", err)
	}
}

func TestLaunchPublicationRetainsDirectorySyncFailureAfterLink(t *testing.T) {
	t.Parallel()
	state, _ := slCovOpenState(t)
	raw := []byte("immutable artifact\n")
	inj := &filewrite.Injector{Step: filewrite.StepDirSync, Err: os.ErrPermission}
	created, err := publishLaunchArtifactInjected(state.launch, "observed.json", raw, inj)
	if created || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("publication = %t,%v", created, err)
	}
	observed, err := readLaunchArtifact(state.launch, "observed.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(observed) != string(raw) {
		t.Fatalf("linked bytes = %q", observed)
	}
}

func TestAttemptListingRefusesNativeNonDirectoryRead(t *testing.T) {
	t.Parallel()
	state, _ := slCovOpenState(t)
	file, err := os.CreateTemp(t.TempDir(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if err := state.attempts.Close(); err != nil {
		t.Fatal(err)
	}
	state.attempts = file
	if _, err := state.listAttempts(); err == nil {
		t.Fatal("regular file admitted as attempts directory")
	}
}

func TestAbandonmentValidationRefusesUnreadableStartedArtifact(t *testing.T) {
	t.Parallel()
	state, root, attempt, plan, digest := slCovAttempt(t)
	marker := launcherAbandonment{HandoffID: plan.HandoffID, AttemptID: attempt.id, AttemptIndex: attempt.index, RequestDigest: plan.RequestDigest, PlanDigest: digest}
	if err := os.WriteFile(filepath.Join(slCovStateDir(root), "started.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateAbandonment(context.Background(), dependencies{}, state, attempt, plan, digest, marker); err == nil {
		t.Fatal("unreadable terminal marker accepted")
	}
}

func TestAbandonmentValidationPreservesNativeEvidenceReadFailure(t *testing.T) {
	t.Parallel()
	state, _, attempt, plan, digest := slCovAttempt(t)
	marker := launcherAbandonment{HandoffID: plan.HandoffID, AttemptID: attempt.id, AttemptIndex: attempt.index, RequestDigest: plan.RequestDigest, PlanDigest: digest}
	if err := attempt.ready.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateAbandonment(context.Background(), dependencies{}, state, attempt, plan, digest, marker); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("evidence seek = %v", err)
	}
}

func TestLaunchPublicationPreservesNativePendingRemovalRace(t *testing.T) {
	t.Parallel()
	state, _ := slCovOpenState(t)
	raw := []byte("immutable artifact\n")
	called := false
	unlink := func(fd int, name string, flags int) error {
		if called {
			t.Fatal("publication repeated pending removal")
		}
		called = true
		if !strings.HasPrefix(name, ".pending-") {
			t.Fatalf("pending name = %q", name)
		}
		if err := unix.Unlinkat(fd, name, flags); err != nil {
			t.Fatal(err)
		}
		return unix.Unlinkat(fd, name, flags)
	}
	created, err := publishLaunchArtifactWithUnlink(state.launch, "linked.json", raw, nil, unlink)
	if created || !called || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("publication = %t, %v", created, err)
	}
	observed, err := readLaunchArtifact(state.launch, "linked.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(observed) != string(raw) {
		t.Fatalf("linked bytes changed = %q", observed)
	}
}
