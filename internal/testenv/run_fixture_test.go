package testenv

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/runqueue"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCloneWithOriginPreservesSeedBranchIdentityAndBareMaintenancePolicy(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	clone := filepath.Join(root, "nested", "clone")
	remote := CloneWithOrigin(t, root, "seed", clone)
	if got := Git(t, root, "--git-dir", remote, "show", "main:README.md"); got != "seed\n" {
		t.Fatalf("seed history=%q", got)
	}
	if got := strings.TrimSpace(Git(t, clone, "config", "user.email")); got != "wb@example.test" {
		t.Fatal(got)
	}
	if got := strings.TrimSpace(Git(t, clone, "config", "user.name")); got != "WB Test" {
		t.Fatal(got)
	}
	for _, key := range gitAutoMaintenanceKeys {
		value := strings.TrimSpace(Git(t, root, "--git-dir", remote, "config", key))
		if value != "0" && value != "false" {
			t.Fatalf("%s=%q", key, value)
		}
	}
}
func TestGitAndCloneFixtureReportActualCommandAndFilesystemFailures(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"git", "seed-parent", "readme", "clone-parent"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			seedRoot := root
			clone := filepath.Join(root, "clone")
			recorder := &recordingTB{}
			if path == "seed-parent" {
				seedRoot = filepath.Join(root, "file")
				if err := os.WriteFile(seedRoot, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if path == "readme" {
				if err := os.MkdirAll(filepath.Join(root, "seed-seed", "README.md"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if path == "clone-parent" {
				blocker := filepath.Join(root, "file")
				if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				clone = filepath.Join(blocker, "clone")
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				if path == "git" {
					Git(recorder, root, "this-command-does-not-exist")
				} else {
					CloneWithOrigin(recorder, seedRoot, "seed", clone)
				}
			}()
			<-done
			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			if !recorder.fataled || recorder.fatalMsg == "" {
				t.Fatalf("missing fatal diagnostic: %+v", recorder)
			}
			if path == "git" && !strings.Contains(recorder.fatalMsg, "git [this-command-does-not-exist]") {
				t.Fatal(recorder.fatalMsg)
			}
			if path != "git" && !strings.Contains(recorder.fatalMsg, "file") && path != "readme" {
				t.Fatal(recorder.fatalMsg)
			}
			if path == "readme" && !strings.Contains(recorder.fatalMsg, "README.md") {
				t.Fatal(recorder.fatalMsg)
			}
		})
	}
}

type queueErrorTB struct {
	recordingTB
	message string
}

func (r *queueErrorTB) Errorf(format string, args ...any) { r.message = fmt.Sprintf(format, args...) }
func TestWaitForQueuedObservesActualTicketAndRetainsDeadlineDiagnostic(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ticket := runqueue.Register(root, runqueue.Participant{PID: os.Getpid(), Summary: "waiter"})
	t.Cleanup(ticket.Forget)
	WaitForQueued(t, root, 1)
	empty := t.TempDir()
	recorder := &queueErrorTB{}
	clock := time.Unix(1, 0)
	calls, sleeps := 0, 0
	waitForQueued(recorder, empty, 1, func() time.Time {
		calls++
		if calls < 3 {
			return clock
		}
		return clock.Add(31 * time.Second)
	}, func(duration time.Duration) {
		if duration != time.Millisecond {
			t.Fatal(duration)
		}
		sleeps++
	})
	if sleeps != 1 || recorder.message != "no waiter registered on the queue within 30s; the queued path cannot be observed" {
		t.Fatalf("sleeps%d diagnostic%q", sleeps, recorder.message)
	}
}
