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
)

func TestLaunchAdmissionRetainsWorkingDirectoryResolutionFailure(t *testing.T) {
	t.Parallel()
	fx := newLauncherRetryFixture(t)
	abs := func(string) (string, error) { return "", os.ErrNotExist }
	if _, err := startWithAbsolutePath(context.Background(), fx.options(nil), fx.deps, abs); err == nil || !strings.Contains(err.Error(), "clean absolute path") {
		t.Fatalf("start resolution = %v", err)
	}
	if _, err := inspectPreparedWithAbsolutePath(context.Background(), fx.options(nil), abs); err == nil || !strings.Contains(err.Error(), "clean absolute path") {
		t.Fatalf("prepared resolution = %v", err)
	}
	if _, err := inspectWithAbsolutePath(context.Background(), fx.options(nil), fx.deps, false, abs); err == nil || !strings.Contains(err.Error(), "clean absolute path") {
		t.Fatalf("inspection resolution = %v", err)
	}
}

func TestInitialPlanPublicationRefusesOccupiedArtifact(t *testing.T) {
	t.Parallel()
	fx := slCovNewAuthorityFixture(t)
	path := filepath.Join(slCovStateDir(fx.root), "plan.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	fx.deps.wbExecutable = func() (string, error) {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		return fx.plan.WBExecutable, nil
	}
	if _, err := startWithDependencies(context.Background(), fx.options(), fx.deps); err == nil {
		t.Fatal("occupied immutable plan accepted")
	}
}

func TestAbandonmentRechecksDurableStateBeforeCreatingNextAttempt(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"encoding", "recheck", "pinned", "next", "replay-next"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			fx := slCovNewAuthorityFixture(t)
			attempt, err := fx.state.createAttempt()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = attempt.Close() })
			const pid = 99111
			fence, err := attempt.acquireExecFence(pid)
			if err != nil {
				t.Fatal(err)
			}
			if err := fence.Close(); err != nil {
				t.Fatal(err)
			}
			calls := 0
			fx.deps.processStatus = func(int) error {
				calls++
				if scope == "recheck" && calls == 2 {
					return nil
				}
				return syscall.ESRCH
			}
			sentinel := errors.New("corrected pinned root refused")
			if scope == "encoding" {
				fx.deps.now = func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
			}
			if scope == "pinned" {
				fx.deps.verifyPinned = func(context.Context, launchPlan) error { return sentinel }
			}
			if scope == "next" || scope == "replay-next" {
				fx.deps.verifyPinned = func(context.Context, launchPlan) error {
					if err := os.WriteFile(filepath.Join(slCovStateDir(fx.root), attemptsDirectoryName, "unexpected"), []byte("occupied"), 0600); err != nil {
						t.Fatal(err)
					}
					return nil
				}
			}
			if scope == "replay-next" {
				if _, _, err := attempt.saveAbandonment(fx.plan, mustPlanDigest(t, fx), pid, fx.deps.now()); err != nil {
					t.Fatal(err)
				}
			}
			_, err = startWithDependencies(context.Background(), fx.options(), fx.deps)
			if err == nil {
				t.Fatal("terminal abandonment failure accepted")
			}
			if scope == "pinned" && !errors.Is(err, sentinel) {
				t.Fatalf("pinned refusal = %v", err)
			}
		})
	}
}
