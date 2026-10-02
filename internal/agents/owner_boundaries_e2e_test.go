//go:build e2e && !windows

package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

// Native owner journeys use a bounded local shell fixture and a private file
// credential. No process environment or shared harness configuration changes.
func ownerBoundaryRun(t *testing.T) (Store, Record, OwnerDeps) {
	t.Helper()
	store := newTestStore(t)
	record := sampleRecord(t)
	record.WorktreeDir = t.TempDir()
	record.BaseSHA = ""
	record.TimeoutMS = 1000
	record.Resolved.Routing.CredentialEnv = ""
	record.Resolved.Routing.CredentialFile = writeCredentialFile(t, 0600, "local-test-credential")
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	_, deps := writeFakeHarness(t)
	return store, record, deps
}

func TestE2EOwnerRefusesUnavailablePrivateLaunchFiles(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"initial save", "harness home", "harness options", "log", "process start"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			store, record, deps := ownerBoundaryRun(t)
			failure := errors.New("owner record publication failed")
			save := store.Save
			want := ""
			switch phase {
			case "initial save":
				save = func(got Record) error {
					if got.OwnerPID != os.Getpid() {
						t.Fatalf("owner pid=%d", got.OwnerPID)
					}
					return failure
				}
			case "harness home":
				if err := os.WriteFile(store.HarnessHomePath(record.AgentID), []byte("occupied"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "create private harness home"
			case "harness options":
				record.Resolved.Model = ""
				if err := store.Save(record); err != nil {
					t.Fatal(err)
				}
				want = "codex launch requires a model"
			case "log":
				if err := os.Mkdir(store.LogPath(record.AgentID), 0700); err != nil {
					t.Fatal(err)
				}
				want = "open run log"
			case "process start":
				deps.LookPath = func(string) (string, error) { return filepath.Join(t.TempDir(), "missing-executable"), nil }
				want = "start codex"
			}
			err := runOwnerWithSave(context.Background(), store, record.AgentID, deps, save, maxLogBytes)
			if phase == "initial save" {
				if !errors.Is(err, failure) {
					t.Fatalf("save error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(record.AgentID)
			if err != nil || loaded.State != StateFailed || loaded.WorkerPID != 0 || !strings.Contains(loaded.Failure, want) {
				t.Fatalf("evidence=%+v %v", loaded, err)
			}
		})
	}
}

func TestE2EOwnerRecordsNativeFailedAndMalformedTurns(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"failed exit", "malformed events", "truncated log"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			store, record, deps := ownerBoundaryRun(t)
			script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"turn.failed\"}'\nexit 3\n"
			want := "harness reported a failed turn"
			limit := int64(maxLogBytes)
			if phase == "malformed events" {
				script = "#!/bin/sh\nprintf '%s\\n' '{malformed'\n"
				want = "unparsable event line"
			}
			if phase == "truncated log" {
				script = "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"turn.completed\"}' 'extra transcript text'\n"
				limit = 8
				want = "unparsable event line"
			}
			path := filepath.Join(t.TempDir(), "harness")
			if err := testenv.WriteExecutableFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			deps.LookPath = func(string) (string, error) { return path, nil }
			if err := runOwnerWithSave(context.Background(), store, record.AgentID, deps, store.Save, limit); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(record.AgentID)
			if err != nil || loaded.State != StateFailed || loaded.WorkerPID <= 0 || !strings.Contains(loaded.Failure, want) {
				t.Fatalf("evidence=%+v %v", loaded, err)
			}
			if phase == "truncated log" {
				found := false
				for _, event := range loaded.HarnessEvents {
					found = found || strings.Contains(event, "run log truncated")
				}
				if !found {
					t.Fatalf("missing truncation evidence: %v", loaded.HarnessEvents)
				}
				info, err := os.Stat(store.LogPath(record.AgentID))
				if err != nil || info.Size() != limit {
					t.Fatalf("log size=%v %v", info, err)
				}
			}
		})
	}
}

func TestE2EOwnerWorkerPublicationFailureCancelsAndReapsUnrecordedTree(t *testing.T) {
	t.Parallel()
	store, record, deps := ownerBoundaryRun(t)
	record.TimeoutMS = 0
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "harness")
	script := `#!/bin/sh
sleep 600 &
child=$!
trap 'kill "$child" 2>/dev/null; wait "$child"; exit 0' TERM
printf '%s\n' "$child" > child.pid
wait "$child"
`
	if err := testenv.WriteExecutableFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	deps.LookPath = func(string) (string, error) { return path, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	failure := errors.New("worker publication failed")
	calls, worker, child := 0, 0, 0
	t.Cleanup(func() {
		if worker > 0 && processAlive(worker) {
			_ = syscall.Kill(-worker, syscall.SIGKILL)
			process, _ := os.FindProcess(worker)
			if process != nil {
				_, _ = process.Wait()
			}
		}
	})
	err := runOwnerWithSave(ctx, store, record.AgentID, deps, func(got Record) error {
		calls++
		if calls == 1 {
			return store.Save(got)
		}
		worker = got.WorkerPID
		if worker <= 0 {
			t.Fatal("native worker did not start")
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			raw, readErr := os.ReadFile(filepath.Join(record.WorktreeDir, "child.pid"))
			if readErr == nil {
				var err error
				child, err = strconv.Atoi(strings.TrimSpace(string(raw)))
				if err != nil || child <= 0 {
					t.Fatalf("child pid=%q %v", raw, err)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("native child never became ready: %v", readErr)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !processAlive(worker) || !processAlive(child) {
			t.Fatal("worker tree was not alive at failed publication")
		}
		return failure
	}, maxLogBytes)
	if !errors.Is(err, failure) || calls != 2 || ctx.Err() != nil {
		t.Fatalf("error=%v calls=%d context=%v", err, calls, ctx.Err())
	}
	deadline := time.Now().Add(2 * time.Second)
	for processAlive(child) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processAlive(worker) || processAlive(child) {
		t.Fatalf("unrecorded tree survived: worker=%d child=%d", worker, child)
	}
	if _, err := syscall.Wait4(worker, nil, syscall.WNOHANG, nil); !errors.Is(err, syscall.ECHILD) {
		t.Fatalf("worker was not reaped: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil || loaded.OwnerPID != os.Getpid() || loaded.WorkerPID != 0 || loaded.State != StateRunning {
		t.Fatalf("owner evidence=%+v %v", loaded, err)
	}
}

func TestE2ESpawnOwnerPropagatesNativeStartFailure(t *testing.T) {
	t.Parallel()
	pid, err := SpawnOwner(t.TempDir(), func() (string, error) { return filepath.Join(t.TempDir(), "missing-wb"), nil })
	if err == nil || pid != 0 {
		t.Fatalf("missing owner executable accepted: %d %v", pid, err)
	}
}
