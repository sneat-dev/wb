package agents

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

// ownedRunWithHarness persists a run for one harness and a scripted fake binary.
func ownedRunWithHarness(t *testing.T, harnessName, script string, mutate func(*Record)) (Store, Record, string, OwnerDeps) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the deterministic fake harness is a POSIX shell script")
	}
	home := t.TempDir()
	t.Setenv("WB_PROJECTS_ROOT", home)
	t.Setenv("DEEPSEEK_API_KEY", "test-credential")
	store := NewStore(home)
	worktree := t.TempDir()
	runGit(t, worktree, "init", "-b", "main")
	runGit(t, worktree, "config", "user.email", "test@example.com")
	runGit(t, worktree, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktree, "add", "README.md")
	runGit(t, worktree, "commit", "-m", "initial")
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	record := Record{
		AgentID: id, State: StateRunning, RequestedProfile: "test-profile",
		Resolved: Resolved{Profile: "test-profile", Harness: harnessName, Model: "test-model"},
		Task:     "do the bounded thing", TaskSummary: "do the bounded thing",
		Repository: "acme/app", Worktree: "task-one", WorktreeDir: worktree,
		WorktreeMode: ModeNew, TimeoutMS: time.Minute.Milliseconds(),
		BaseSHA:             strings.TrimSpace(runGit(t, worktree, "rev-parse", "HEAD")),
		StartedAt:           time.Now().UTC(),
		HarnessCapabilities: HarnessCapabilities{Harness: harnessName},
	}
	if harnessName == HarnessCodex {
		record.Resolved.Provider = "deepseek"
		record.Resolved.Routing = Provider{BaseURL: "https://api.deepseek.com", CredentialEnv: "DEEPSEEK_API_KEY", WireAPI: WireAPIResponses}
	}
	if mutate != nil {
		mutate(&record)
	}
	record.LogPath = store.LogPath(record.AgentID)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), harnessName)
	if err := testenv.WriteExecutableFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	deps := DefaultOwnerDeps()
	deps.LookPath = func(name string) (string, error) {
		if name != harnessName {
			t.Fatalf("owner looked up %q, want %q", name, harnessName)
		}
		return path, nil
	}
	return store, record, worktree, deps
}

const openCodeFakeHarness = `#!/bin/sh
prompt=$(cat)
echo '{"type":"step_start"}'
echo '{"type":"tool_use","part":{"tool":"shell","state":{"status":"completed","input":{"command":"echo hi"}}}}'
echo '{"type":"step_finish","part":{"cost":0.0015,"tokens":{"input":200,"output":20,"reasoning":1,"cache":{"read":50,"write":2}}}}'
echo '{"type":"text","part":{"type":"text","text":"the opencode answer"}}'
exit 0
`

//nolint:paralleltest // the fixture sets process environment and runs a scripted harness
func TestRunOwnerRecordsAnOpenCodeRun(t *testing.T) {
	store, record, worktree, deps := ownedRunWithHarness(t, HarnessOpenCode, openCodeFakeHarness, func(record *Record) {
		record.Resolved.Model = "opencode-go/deepseek-v4.1-flash"
	})
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExecutionStatus != StatusCompleted || loaded.State != StateCompleted {
		t.Fatalf("status = %q/%q failure=%q", loaded.ExecutionStatus, loaded.State, loaded.Failure)
	}
	if !loaded.Telemetry.Available || loaded.Telemetry.Tokens == nil || *loaded.Telemetry.Tokens.Input != 200 {
		t.Fatalf("telemetry = %#v", loaded.Telemetry)
	}
	if loaded.Cost.USD == nil || *loaded.Cost.USD != 0.0015 || loaded.Cost.Source != CostHarnessEstimated {
		t.Fatalf("cost = %#v", loaded.Cost)
	}
	if loaded.Result != "the opencode answer" {
		t.Fatalf("result = %q", loaded.Result)
	}
	if loaded.ToolCalls != 1 {
		t.Fatalf("tool calls = %d", loaded.ToolCalls)
	}
	if loaded.DiffPath == "" {
		t.Fatal("a finished run must keep its diff artefact")
	}
	if _, err := os.Stat(filepath.Join(worktree, "harness-touched.txt")); err == nil {
		t.Fatal("the opencode fake must not write a codex artefact")
	}
}

const dshFakeHarness = `#!/bin/sh
# dsh receives the task as one argument and prints only the final message.
printf 'the dsh answer for: %s\n' "$3"
exit 0
`

//nolint:paralleltest // the fixture sets process environment and runs a scripted harness
func TestRunOwnerRecordsADshRunWithUnavailableUsage(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessDSH, dshFakeHarness, func(record *Record) {
		record.Resolved.Model = "deepseek-v4-flash"
		record.TaskVisibility = TaskDeliveryArgv
	})
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExecutionStatus != StatusCompleted {
		t.Fatalf("status = %q failure=%q", loaded.ExecutionStatus, loaded.Failure)
	}
	if loaded.Telemetry.Available || loaded.Telemetry.UnavailableReason == "" {
		t.Fatalf("dsh telemetry = %#v", loaded.Telemetry)
	}
	if loaded.Cost.USD != nil || loaded.Cost.Note == "" {
		t.Fatalf("dsh cost = %#v", loaded.Cost)
	}
	if !strings.Contains(loaded.Result, "the dsh answer") {
		t.Fatalf("result = %q", loaded.Result)
	}
	if loaded.Usage != nil {
		t.Fatalf("legacy usage must stay absent when nothing was observed: %#v", loaded.Usage)
	}
}

//nolint:paralleltest // the fixture sets process environment and runs a scripted harness
func TestRunOwnerRecordsACancellationDistinctFromAFailure(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessCodex, fakeHarnessScript, nil)
	if err := requestCancel(store, record.AgentID, time.Now); err != nil {
		t.Fatal(err)
	}
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExecutionStatus != StatusCancelled {
		t.Fatalf("status = %q", loaded.ExecutionStatus)
	}
	if loaded.State != StateFailed || loaded.FailureCategory != FailureCancelled {
		t.Fatalf("legacy state/category = %q/%q", loaded.State, loaded.FailureCategory)
	}
	if loaded.Validation.Status != ValidationSkipped {
		t.Fatalf("validation = %#v", loaded.Validation)
	}
	if result := store.Render(loaded); !result.Terminal || result.Status != StatusCancelled {
		t.Fatalf("rendered = %#v", result)
	}
}

//nolint:paralleltest // the fixture sets process environment and runs a scripted harness
func TestRunOwnerRunsValidationWithoutTheProviderCredential(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessCodex, fakeHarnessScript, func(record *Record) {
		record.AcceptanceRequired = true
		record.Validation = ValidationRecord{Status: ValidationPending, Steps: []ValidationStepResult{
			{
				Name: "credential must be absent", Status: ValidationPending,
				Argv: []string{"/bin/sh", "-c", `if [ -n "${DEEPSEEK_API_KEY:-}" ]; then exit 9; fi`},
			},
		}}
	})
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExecutionStatus != StatusCompleted {
		t.Fatalf("execution status = %q (%s)", loaded.ExecutionStatus, loaded.Failure)
	}
	if loaded.Validation.Status != ValidationPassed || len(loaded.Validation.Steps) != 1 {
		t.Fatalf("validation = %#v", loaded.Validation)
	}
	if loaded.Validation.Steps[0].ExitCode == nil || *loaded.Validation.Steps[0].ExitCode != 0 {
		t.Fatalf("validation exit = %#v", loaded.Validation.Steps[0])
	}
}

const echoingHarness = `#!/bin/sh
# A harness that echoes the credential it was given, to prove redaction.
last=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) last="$2"; shift 2 ;;
    *) shift ;;
  esac
done
prompt=$(cat)
printf 'the key is %s\n' "${DEEPSEEK_API_KEY:-none}"
echo '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
if [ -n "$last" ]; then
  printf 'final key %s\n' "${DEEPSEEK_API_KEY:-none}" > "$last"
fi
exit 0
`

//nolint:paralleltest // the fixture sets process environment and runs a scripted harness
func TestRunOwnerRedactsTheInjectedCredential(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessCodex, echoingHarness, nil)
	// The fake harness needs the -o path; the real argv places it last.
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	logRaw, err := os.ReadFile(loaded.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logRaw), "test-credential") {
		t.Fatalf("the credential reached the log: %q", string(logRaw))
	}
	if !strings.Contains(string(logRaw), redactionMarker) {
		t.Fatalf("the log must show a redaction: %q", string(logRaw))
	}
	if strings.Contains(loaded.Result, "test-credential") {
		t.Fatalf("the final message kept the credential: %q", loaded.Result)
	}
}

//nolint:paralleltest // the fixture sets process environment and runs a scripted harness
func TestRunOwnerRecordsTimeToFirstOutput(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessCodex, fakeHarnessScript, nil)
	now := time.Now()
	deps.Now = func() time.Time { return now.Add(2 * time.Second) }
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TimeToFirstOutputMS < 0 {
		t.Fatalf("time to first output = %d", loaded.TimeToFirstOutputMS)
	}
}

//nolint:paralleltest // the fixture sets process environment and runs a scripted harness
func TestRunOwnerRecordsAnUnavailableCredentialAsACategory(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessCodex, fakeHarnessScript, func(record *Record) {
		record.Resolved.Routing = Provider{BaseURL: "https://api.deepseek.com", CredentialEnv: "WB_AGENT_MISSING_KEY", WireAPI: WireAPIResponses}
	})
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExecutionStatus != StatusFailed || loaded.FailureCategory != FailureCredential {
		t.Fatalf("status/category = %q/%q", loaded.ExecutionStatus, loaded.FailureCategory)
	}
}

//nolint:paralleltest // the fixture sets process environment and runs a scripted harness
func TestOwnerDepsCarryNoLeaseUnlessTheDispatcherPassedOne(t *testing.T) {
	if deps := DefaultOwnerDeps(); deps.Lease != nil {
		t.Fatal("a test process must never adopt a stray descriptor as a lease")
	}
	if lease := inheritedLease(); lease != nil {
		t.Fatalf("a process without the dispatcher marker has no lease: %#v", lease)
	}
}
