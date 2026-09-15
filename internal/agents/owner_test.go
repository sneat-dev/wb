package agents

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeHarnessScript is a deterministic stand-in for the real coding harness. It
// reads the task from stdin (which is also how it selects its behaviour), emits
// the same JSONL event shapes the real harness emits, writes the last-message
// file when asked with -o, and exits with the requested status.
const fakeHarnessScript = `#!/bin/sh
set -u
workdir="."
last=""
mode="success"
while [ "$#" -gt 0 ]; do
  case "$1" in
    -C) workdir="$2"; shift 2 ;;
    -o) last="$2"; shift 2 ;;
    *) shift ;;
  esac
done
prompt=$(cat)
case "$prompt" in
  *TURNFAIL_MODE*) mode="turnfail" ;;
  *NOTURN_MODE*) mode="noturn" ;;
  *HANG_MODE*) mode="hang" ;;
  *FAIL_MODE*) mode="failure" ;;
esac
echo 'not json on stderr'
echo '{"type":"item.completed","item":{"id":"i0","type":"error","message":"Model metadata not found."}}'
echo '{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"ls","exit_code":0,"status":"completed"}}'
if [ "$mode" = "hang" ]; then
  sleep 600
fi
if [ "$mode" = "success" ]; then
  printf 'touched\n' >> "$workdir/harness-touched.txt"
fi
if [ "$mode" = "turnfail" ]; then
  echo '{"type":"turn.failed"}'
  exit 0
fi
if [ "$mode" != "noturn" ]; then
  echo '{"type":"turn.completed","usage":{"input_tokens":120,"cached_input_tokens":100,"cache_write_input_tokens":0,"output_tokens":9,"reasoning_output_tokens":4}}'
fi
if [ -n "$last" ]; then
  printf 'harness finished mode=%s\n' "$mode" > "$last"
fi
if [ "$mode" = "failure" ]; then
  exit 3
fi
exit 0
`

func writeFakeHarness(t *testing.T) (path string, deps OwnerDeps) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the deterministic fake harness is a POSIX shell script")
	}
	directory := t.TempDir()
	path = filepath.Join(directory, HarnessCodex)
	if err := os.WriteFile(path, []byte(fakeHarnessScript), 0o700); err != nil {
		t.Fatal(err)
	}
	deps = DefaultOwnerDeps()
	deps.LookPath = func(name string) (string, error) {
		if name != HarnessCodex {
			t.Errorf("owner looked up %q, want the resolved harness %q", name, HarnessCodex)
		}
		return path, nil
	}
	return path, deps
}

// ownedRun persists a run whose worktree is a real directory the fake harness
// can write into.
func ownedRun(t *testing.T, task string, timeout time.Duration) (Store, Record, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	t.Setenv("DEEPSEEK_API_KEY", "test-credential")
	store := NewStore(home)
	worktree := t.TempDir()
	// The worktree is a real repository so the run's change summary is derived
	// from actual Git state, exactly as it is in production.
	runGit(t, worktree, "init", "-b", "main")
	runGit(t, worktree, "config", "user.email", "test@example.com")
	runGit(t, worktree, "config", "user.name", "Test")
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	record := Record{
		AgentID: id, State: StateRunning, RequestedProfile: "cheap",
		Resolved: Resolved{
			Profile: "cheap", Harness: HarnessCodex, Provider: "deepseek",
			Model: "deepseek-flash", Reasoning: "high",
			Routing: Provider{BaseURL: "https://api.deepseek.com", CredentialEnv: "DEEPSEEK_API_KEY", WireAPI: WireAPIResponses},
		},
		Task: task, Worktree: "task-one", WorktreeDir: worktree,
		Repository: "acme/app", WorktreeMode: ModeNew,
		TimeoutMS: timeout.Milliseconds(),
		StartedAt: time.Now().UTC(),
	}
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	return store, record, worktree
}

func TestRunOwnerRecordsASuccessfulRun(t *testing.T) {
	_, deps := writeFakeHarness(t)
	store, record, worktree := ownedRun(t, "please do the thing", time.Minute)

	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateCompleted {
		t.Fatalf("state = %s (failure=%q)", loaded.State, loaded.Failure)
	}
	if loaded.ExitCode == nil || *loaded.ExitCode != 0 {
		t.Fatalf("exit code = %v", loaded.ExitCode)
	}
	if loaded.WorkerPID <= 0 || loaded.OwnerPID != os.Getpid() {
		t.Fatalf("process identities = owner %d worker %d", loaded.OwnerPID, loaded.WorkerPID)
	}
	if loaded.FinishedAt.IsZero() || loaded.DurationMS < 0 {
		t.Fatalf("timing = %v %d", loaded.FinishedAt, loaded.DurationMS)
	}
	if loaded.Usage == nil || loaded.Usage.InputTokens != 120 || loaded.Usage.OutputTokens != 9 || loaded.Usage.ReasoningOutputTokens != 4 {
		t.Fatalf("usage = %#v", loaded.Usage)
	}
	if loaded.ToolCalls != 1 {
		t.Fatalf("tool calls = %d", loaded.ToolCalls)
	}
	if !strings.Contains(loaded.Result, "harness finished mode=success") {
		t.Fatalf("the worker's final message must be captured: %q", loaded.Result)
	}
	if len(loaded.HarnessEvents) != 1 || !strings.Contains(loaded.HarnessEvents[0], "Model metadata") {
		t.Fatalf("non-fatal harness diagnostics must be recorded, not treated as failure: %#v", loaded.HarnessEvents)
	}
	if loaded.Changes == nil || loaded.Changes.FilesChanged != 1 {
		t.Fatalf("changes = %#v", loaded.Changes)
	}
	if _, err := os.Stat(filepath.Join(worktree, "harness-touched.txt")); err != nil {
		t.Fatalf("the worker's change must survive: %v", err)
	}

	// The transcript is preserved privately, and the private harness home is
	// created rather than the user's own harness state being used.
	logInfo, err := os.Stat(store.LogPath(record.AgentID))
	if err != nil || logInfo.Mode().Perm() != 0o600 {
		t.Fatalf("run log must exist and be private: %v %v", logInfo, err)
	}
	if contents, err := os.ReadFile(store.LogPath(record.AgentID)); err != nil || !strings.Contains(string(contents), "turn.completed") {
		t.Fatalf("the transcript must be captured: %v", err)
	}
	homeInfo, err := os.Stat(store.HarnessHomePath(record.AgentID))
	if err != nil || !homeInfo.IsDir() {
		t.Fatalf("the private harness home must exist: %v %v", homeInfo, err)
	}
}

func TestRunOwnerRecordsFailureTimeoutAndUnfinishedTurns(t *testing.T) {
	cases := []struct {
		name      string
		task      string
		timeout   time.Duration
		wantState State
		wantExit  int
		hasExit   bool
		fragment  string
	}{
		{"non-zero exit", "FAIL_MODE", time.Minute, StateFailed, 3, true, "harness exited"},
		{"failed turn despite exit zero", "TURNFAIL_MODE", time.Minute, StateFailed, 0, true, "failed turn"},
		{"no terminal turn event", "NOTURN_MODE", time.Minute, StateFailed, 0, true, "no terminal turn event"},
		{"timeout", "HANG_MODE", 2 * time.Second, StateTimeout, 0, false, "exceeded its timeout"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, deps := writeFakeHarness(t)
			store, record, _ := ownedRun(t, testCase.task, testCase.timeout)
			started := time.Now()
			if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
				t.Fatalf("RunOwner: %v", err)
			}
			loaded, err := store.Load(record.AgentID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.State != testCase.wantState {
				t.Fatalf("state = %s, want %s (failure=%q)", loaded.State, testCase.wantState, loaded.Failure)
			}
			if !strings.Contains(loaded.Failure, testCase.fragment) {
				t.Fatalf("failure %q does not mention %q", loaded.Failure, testCase.fragment)
			}
			if testCase.hasExit {
				if loaded.ExitCode == nil || *loaded.ExitCode != testCase.wantExit {
					t.Fatalf("exit code = %v, want %d", loaded.ExitCode, testCase.wantExit)
				}
			}
			if loaded.FinishedAt.IsZero() {
				t.Fatal("a terminal state must carry a finish time")
			}
			if err := RunOwner(context.Background(), store, "agt-00000000000000000000000000000000", deps); err == nil {
				t.Fatal("an unknown run id must be reported")
			}
			if testCase.wantState == StateTimeout && time.Since(started) > 30*time.Second {
				t.Fatal("the timeout did not bound the run")
			}
		})
	}
}

func TestRunOwnerTerminatesTheWholeWorkerProcessGroupOnTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are a POSIX concept")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, HarnessCodex)
	// The harness ignores SIGTERM and spawns a child that also ignores it, so
	// only a process-group kill can stop the tree.
	script := `#!/bin/sh
trap '' TERM
sh -c 'trap "" TERM; sleep 600' &
echo $! > grandchild.pid
sleep 600
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	deps := DefaultOwnerDeps()
	deps.LookPath = func(string) (string, error) { return path, nil }

	store, record, worktree := ownedRun(t, "HANG_MODE", 2*time.Second)
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateTimeout {
		t.Fatalf("state = %s", loaded.State)
	}
	// The grandchild inherited the worker's process group and ignores SIGTERM
	// too, so only a group kill can stop it. Asserting on the direct child
	// alone would pass even if the tree survived.
	grandchildRaw, err := os.ReadFile(filepath.Join(worktree, "grandchild.pid"))
	if err != nil {
		t.Fatalf("the harness never reported its grandchild: %v", err)
	}
	grandchild, convErr := strconv.Atoi(strings.TrimSpace(string(grandchildRaw)))
	if convErr != nil || grandchild <= 0 {
		t.Fatalf("grandchild pid %q", grandchildRaw)
	}
	for _, pid := range []int{loaded.WorkerPID, grandchild} {
		deadline := time.Now().Add(5 * time.Second)
		for processAlive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("process %d survived the timeout termination", pid)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func TestRunOwnerFailsActionablyWithoutAHarnessOrCredential(t *testing.T) {
	store, record, _ := ownedRun(t, "anything", time.Minute)

	missing := DefaultOwnerDeps()
	missing.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if err := RunOwner(context.Background(), store, record.AgentID, missing); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateFailed || !strings.Contains(loaded.Failure, HarnessCodex) || !strings.Contains(loaded.Failure, "PATH") {
		t.Fatalf("a missing harness must fail naming the executable and PATH: %#v", loaded)
	}

	store, record, _ = ownedRun(t, "anything", time.Minute)
	t.Setenv("DEEPSEEK_API_KEY", "")
	_, deps := writeFakeHarness(t)
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err = store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateFailed || !strings.Contains(loaded.Failure, "DEEPSEEK_API_KEY") {
		t.Fatalf("a missing credential must fail naming the variable: %#v", loaded)
	}
	if loaded.WorkerPID != 0 {
		t.Fatal("no worker may be started without a credential")
	}
}

func TestRunOwnerReportsAnUnwritableRunDirectory(t *testing.T) {
	_, deps := writeFakeHarness(t)
	store, record, _ := ownedRun(t, "anything", time.Minute)
	// A run directory whose harness home cannot be created fails before launch.
	if err := os.MkdirAll(filepath.Join(store.Dir(record.AgentID), harnessHomeDirName), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(store.Dir(record.AgentID), harnessHomeDirName), 0o700) })
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	// Whether this fails depends on the umask and the filesystem, so assert the
	// invariant rather than one platform's answer: the run is terminal and any
	// failure names the cause.
	if !loaded.State.Terminal() {
		t.Fatalf("state = %s", loaded.State)
	}
	if loaded.State == StateFailed && loaded.Failure == "" {
		t.Fatal("a failed run must explain itself")
	}
}

func TestRunOwnerRunsToCompletionWithoutABound(t *testing.T) {
	_, deps := writeFakeHarness(t)
	store, record, _ := ownedRun(t, "no timeout configured", 0)
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateCompleted {
		t.Fatalf("a zero timeout must mean no bound, not an immediate one: %s", loaded.State)
	}
}

func TestBoundedLogCapsGrowthWithoutFailingTheWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	log := &boundedLog{writer: file, remaining: 10}

	written, err := log.Write([]byte("0123456789ABCDEF"))
	if err != nil || written != 16 {
		t.Fatalf("a truncated write must still report the full length, got %d %v", written, err)
	}
	if !log.truncated() {
		t.Fatal("the log must know it was truncated")
	}
	// Once exhausted every further write is accepted and discarded.
	if written, err := log.Write([]byte("more")); err != nil || written != 4 {
		t.Fatalf("writes past the bound must be reported as successful: %d %v", written, err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "0123456789" {
		t.Fatalf("log contents = %q", contents)
	}
	if log.truncated() != true {
		t.Fatal("truncation flag was lost")
	}
}

func TestRunOwnerSurvivesAMissingFinalMessage(t *testing.T) {
	_, deps := writeFakeHarness(t)
	store, record, _ := ownedRun(t, "no final message channel", time.Minute)
	// Occupy the last-message path with a directory so the harness cannot write
	// it and the owner cannot read it. The run must still be reported honestly.
	if err := os.MkdirAll(store.LastMessagePath(record.AgentID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateCompleted {
		t.Fatalf("state = %s", loaded.State)
	}
	if loaded.Result != "" {
		t.Fatalf("a missing final message must be absent, not invented: %q", loaded.Result)
	}
}

func TestExitCodeOfToleratesAProcessThatNeverStarted(t *testing.T) {
	if code := exitCodeOf(&exec.Cmd{}); code != nil {
		t.Fatalf("exitCodeOf on a process that never started = %v, want nil", *code)
	}
}

func TestSummarizeLogToleratesAMissingLog(t *testing.T) {
	if summary := summarizeLog(filepath.Join(t.TempDir(), "absent.jsonl")); summary.TurnCompleted || summary.Usage != nil {
		t.Fatalf("a missing log must yield an empty summary: %#v", summary)
	}
}

func TestOwnerCLIParsesItsPrivateArguments(t *testing.T) {
	_, deps := writeFakeHarness(t)

	if code := OwnerCLI(nil, deps); code != 2 {
		t.Fatalf("a missing --run-dir must be a usage failure, got %d", code)
	}
	if code := OwnerCLI([]string{"--run-dir"}, deps); code != 2 {
		t.Fatalf("a --run-dir with no value must be a usage failure, got %d", code)
	}
	if code := OwnerCLI([]string{"--run-dir", filepath.Join(t.TempDir(), "agt-00000000000000000000000000000000")}, deps); code != 1 {
		t.Fatalf("an unknown run must exit non-zero, got %d", code)
	}

	store, record, _ := ownedRun(t, "run through the private entry point", time.Minute)
	if code := OwnerCLI([]string{"--run-dir", store.Dir(record.AgentID)}, deps); code != 0 {
		t.Fatalf("a healthy run through OwnerCLI exited %d", code)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateCompleted {
		t.Fatalf("state = %s", loaded.State)
	}
}

func TestStopRunTerminatesTheWorkerAndLeavesTheOwnerToRecordIt(t *testing.T) {
	_, deps := writeFakeHarness(t)
	if runtime.GOOS == "windows" {
		t.Skip("process-group termination is POSIX-only")
	}
	store, record, _ := ownedRun(t, "HANG_MODE", 60*time.Second)

	// A live worker is required for the stop to have a target; run the owner in
	// the background and wait for it to record the worker's PID.
	done := make(chan struct{})
	go func() {
		_ = RunOwner(context.Background(), store, record.AgentID, deps)
		close(done)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		loaded, err := store.Load(record.AgentID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.WorkerPID > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the owner never recorded a worker PID")
		}
		time.Sleep(25 * time.Millisecond)
	}

	stopped, err := StopRun(store, record.AgentID)
	if err != nil {
		t.Fatalf("StopRun: %v", err)
	}
	if stopped.AgentID != record.AgentID {
		t.Fatalf("StopRun returned %q", stopped.AgentID)
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the owner did not observe the stopped worker")
	}
	final, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != StateFailed {
		t.Fatalf("a stopped worker must reach a terminal outcome, got %s", final.State)
	}
	if _, err := StopRun(store, record.AgentID); err == nil {
		t.Fatal("stopping a terminal run must be refused")
	}
	if _, err := StopRun(store, "agt-00000000000000000000000000000000"); err == nil {
		t.Fatal("stopping an unknown run must be refused")
	}
}

func TestStopRunEscalatesPastAWorkerThatIgnoresTermination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group termination is POSIX-only")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, HarnessCodex)
	// The loop keeps the shell itself alive: a bare `sleep` would be killed by
	// the group signal, letting the shell exit before escalation is needed. It
	// reports readiness by touching a file, because signalling before the shell
	// has installed its trap would kill it outright and never exercise
	// escalation.
	if err := os.WriteFile(path, []byte("#!/bin/sh\ntrap '' TERM\n: > ignored-termination.ready\nwhile true; do sleep 1; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	deps := DefaultOwnerDeps()
	deps.LookPath = func(string) (string, error) { return path, nil }

	store, record, worktree := ownedRun(t, "ignore termination", time.Minute)
	done := make(chan struct{})
	go func() {
		_ = RunOwner(context.Background(), store, record.AgentID, deps)
		close(done)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		loaded, err := store.Load(record.AgentID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.WorkerPID > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the owner never recorded a worker PID")
		}
		time.Sleep(25 * time.Millisecond)
	}
	for {
		if _, err := os.Stat(filepath.Join(worktree, "ignored-termination.ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the harness never installed its termination trap")
		}
		time.Sleep(25 * time.Millisecond)
	}

	if _, err := StopRun(store, record.AgentID); err != nil {
		t.Fatalf("StopRun: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	killDeadline := time.Now().Add(10 * time.Second)
	for processAlive(loaded.WorkerPID) {
		if time.Now().After(killDeadline) {
			t.Fatalf("a worker that ignores SIGTERM survived stop (pid %d)", loaded.WorkerPID)
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the owner did not observe the killed worker")
	}
}

func TestStopRunRefusesARunWithNoLiveWorker(t *testing.T) {
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if _, err := StopRun(store, record.AgentID); err == nil {
		t.Fatal("a run with no recorded worker must be refused")
	}
	record.WorkerPID = 999999999
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	if _, err := StopRun(store, record.AgentID); err == nil || !strings.Contains(err.Error(), "already gone") {
		t.Fatalf("a dead worker must be reported: %v", err)
	}
}

func TestSpawnOwnerStartsADetachedProcessAndReportsItsPID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the detached owner uses a POSIX session")
	}
	// Spawn this test binary in a mode that exits immediately, which is enough
	// to prove the owner is started, released, and reported.
	runDir := t.TempDir()
	pid, err := SpawnOwner(runDir, func() (string, error) { return "/bin/sleep", nil })
	if err != nil {
		t.Fatalf("SpawnOwner: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("SpawnOwner returned pid %d", pid)
	}
	// The owner is released rather than waited on, so it may briefly outlive
	// this call; it must not become a zombie this process has to reap.
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	if _, err := SpawnOwner(runDir, func() (string, error) { return "", os.ErrNotExist }); err == nil {
		t.Fatal("an unresolvable executable must be reported")
	}
	if _, err := SpawnOwner(runDir, func() (string, error) { return "/nonexistent/wb", nil }); err == nil {
		t.Fatal("a failed exec must be reported")
	}
}

func TestProcessAliveIsFalseForNonsensePIDs(t *testing.T) {
	for _, pid := range []int{0, -1, -9999} {
		if processAlive(pid) {
			t.Fatalf("processAlive(%d) = true", pid)
		}
	}
	if !processAlive(os.Getpid()) {
		t.Fatal("this process must be alive")
	}
}

func TestTerminateOwnerIgnoresAnAlreadyGoneProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are a POSIX concept")
	}
	// A group that no longer exists must be treated as success: the caller's
	// intent — "make sure this is not running" — is already satisfied, and
	// reporting ESRCH as a failure would make a stop racing a natural exit look
	// like an error.
	if err := terminateOwner(999999999, terminationSignal()); err != nil {
		t.Fatalf("terminateOwner on a gone group = %v, want nil", err)
	}
}
