package sessionlaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestPrivateLauncherRetainsDurablePublicationFailures(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"session-list", "ready", "release", "release-reread", "failure", "fence", "ready-reread"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			fx := newLauncherRetryFixture(t)
			state, err := openLaunchState(fx.store.Root, fx.request.HandoffID, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = state.Close() })
			attempt, err := state.createAttempt()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = attempt.Close() })
			sentinel := errors.New("harness execution refused")
			deps := privateLauncherDependencies{
				pid: func() int { return os.Getpid() }, register: session.Register,
				wbExecutable: func() (string, error) { return fx.plan.WBExecutable, nil },
				verifyPinned: func(context.Context, launchPlan) error { return nil },
				exec:         func(string, []string, []string) error { return nil }, now: fx.deps.now,
			}
			if scope == "fence" {
				deps.pid = func() int {
					pid := os.Getpid()
					if err := os.WriteFile(filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), execDirectoryName, strconv.Itoa(pid)+".lock"), nil, 0644); err != nil {
						t.Fatal(err)
					}
					return pid
				}
			}
			if scope == "session-list" {
				if err := os.WriteFile(filepath.Join(fx.root, session.DirName), []byte("occupied"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scope == "ready" {
				deps.register = func(directory string, record session.Record) (session.Record, error) {
					registered, err := session.Register(directory, record)
					if err != nil {
						return registered, err
					}
					if err := os.WriteFile(filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), "ready", strconv.Itoa(record.PID)+".json"), []byte("invalid"), 0600); err != nil {
						t.Fatal(err)
					}
					return registered, nil
				}
			}
			slept := false
			deps.sleep = func(time.Duration) {
				if slept {
					t.Fatal("launcher continued after refusal")
				}
				slept = true
				ready, _, err := attempt.loadReady(os.Getpid())
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := attempt.saveRelease(fx.plan, fx.planDigest, ready, "", fx.deps.now()); err != nil {
					t.Fatal(err)
				}
			}
			if scope == "release" {
				deps.sleep = func(time.Duration) {
					if slept {
						t.Fatal("launcher continued after refusal")
					}
					slept = true
					if err := os.WriteFile(filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), "release.json"), []byte("invalid"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if scope == "release-reread" {
				successfulReads := 0
				deps.readRelease = func(current *launchAttempt) (launcherRelease, sessionmove.Digest, error) {
					if successfulReads == 1 {
						if err := os.Remove(filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), "release.json")); err != nil {
							t.Fatal(err)
						}
					}
					release, digest, err := current.loadRelease()
					if err == nil {
						successfulReads++
					}
					return release, digest, err
				}
			}
			if scope == "ready-reread" {
				deps.readReady = func(current *launchAttempt, pid int) (launcherReady, sessionmove.Digest, error) {
					if err := os.Remove(filepath.Join(slCovAttemptDir(fx.store.Root, attempt.id), readyDirectoryName, strconv.Itoa(pid)+".json")); err != nil {
						t.Fatal(err)
					}
					return current.loadReady(pid)
				}
			}
			if scope == "failure" {
				deps.exec = func(string, []string, []string) error { return sentinel }
				deps.now = func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
			}
			args := []string{fx.store.Root, fx.request.HandoffID, attempt.id, string(fx.planDigest)}
			err = runPrivateLauncherWithDirectory(args, deps, func() (string, error) { return fx.worktree, nil }, os.Stat)
			if err == nil {
				t.Fatal("private launcher accepted failed native observation/publication")
			}
			if scope == "ready-reread" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("second readiness observation = %v", err)
			}
			if scope == "release-reread" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("second release observation = %v", err)
			}
			if scope == "failure" && (!errors.Is(err, sentinel) || !strings.Contains(err.Error(), "persist immutable launcher failure")) {
				t.Fatalf("failure = %v", err)
			}
		})
	}
}
