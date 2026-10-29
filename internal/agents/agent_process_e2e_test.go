//go:build e2e && !windows

package agents

// These tests start real processes: a scripted harness executable, real Git, and
// shell validation steps. They live in the e2e tier so the default unit tier
// stays free of process starts (the repository's unit-tier policy).

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
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
	deps.LoadExecutionPolicy = noMandatoryExecutionPolicy
	deps.LookPath = func(name string) (string, error) {
		if name != harnessName {
			t.Fatalf("owner looked up %q, want %q", name, harnessName)
		}
		return path, nil
	}
	return store, record, worktree, deps
}

const malformedHarness = `#!/bin/sh
prompt=$(cat)
echo '{"type":"turn.completed"'
exit 0
`

const silentHarness = `#!/bin/sh
prompt=$(cat)
exit 4
`

const openCodeFakeHarness = `#!/bin/sh
prompt=$(cat)
echo '{"type":"step_start"}'
echo '{"type":"tool_use","part":{"tool":"shell","state":{"status":"completed","input":{"command":"echo hi"}}}}'
echo '{"type":"step_finish","part":{"cost":0.0015,"tokens":{"input":200,"output":20,"reasoning":1,"cache":{"read":50,"write":2}}}}'
echo '{"type":"text","part":{"type":"text","text":"the opencode answer"}}'
exit 0
`

//nolint:paralleltest // the fixture sets process environment and runs a scripted harness
func TestE2ERunOwnerRecordsAnOpenCodeRun(t *testing.T) {
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
func TestE2ERunOwnerRecordsADshRunWithUnavailableUsage(t *testing.T) {
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
func TestE2ERunOwnerRecordsACancellationDistinctFromAFailure(t *testing.T) {
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
func TestE2ERunOwnerRunsValidationWithoutTheProviderCredential(t *testing.T) {
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
func TestE2ERunOwnerRedactsTheInjectedCredential(t *testing.T) {
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
func TestE2ERunOwnerRecordsTimeToFirstOutput(t *testing.T) {
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
func TestE2ERunOwnerRecordsAnUnavailableCredentialAsACategory(t *testing.T) {
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
func TestE2EOwnerDepsCarryNoLeaseUnlessTheDispatcherPassedOne(t *testing.T) {
	if deps := DefaultOwnerDeps(); deps.Lease != nil {
		t.Fatal("a test process must never adopt a stray descriptor as a lease")
	}
	if lease := inheritedLease(); lease != nil {
		t.Fatalf("a process without the dispatcher marker has no lease: %#v", lease)
	}
}

// fakeStepCommand returns a command constructor that records the environment it
// was given and runs /bin/sh with the requested exit code.
func fakeStepCommand(t *testing.T, exitCode int) (func(ctx context.Context, name string, args ...string) *exec.Cmd, *[]string) {
	t.Helper()
	var captured []string
	command := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exit "+itoa(exitCode))
		captured = append(captured, strings.Join(cmd.Env, "\n"))
		return cmd
	}
	return command, &captured
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

//nolint:paralleltest // the test sets the provider credential in the process environment
func TestE2EValidationRunsStepsWithoutTheCredential(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "super-secret")
	runDir := t.TempDir()
	command, captured := fakeStepCommand(t, 0)
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: runDir,
		Steps:   []ValidationStep{{Name: "unit tests", Argv: []string{"go", "test", "./..."}, Timeout: "2m"}},
		Command: command, Now: time.Now,
	})
	if record.Status != ValidationPassed || len(record.Steps) != 1 || record.Steps[0].Status != ValidationPassed {
		t.Fatalf("validation = %#v", record)
	}
	if record.Steps[0].ExitCode == nil || *record.Steps[0].ExitCode != 0 {
		t.Fatalf("exit code = %v", record.Steps[0].ExitCode)
	}
	if record.Steps[0].LogPath == "" || !strings.HasSuffix(record.Steps[0].LogPath, "01-unit-tests.log") {
		t.Fatalf("log path = %q", record.Steps[0].LogPath)
	}
	if len(*captured) != 1 || strings.Contains((*captured)[0], "super-secret") {
		t.Fatalf("a validation step must not see the provider credential: %v", *captured)
	}
}

func TestE2EValidationReportsAFailedStepAndAFailedStart(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	command, _ := fakeStepCommand(t, 3)
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: runDir,
		Steps:   []ValidationStep{{Name: "lint", Argv: []string{"go", "vet"}}},
		Command: command, Now: time.Now,
	})
	if record.Status != ValidationFailed || record.Steps[0].Status != ValidationFailed {
		t.Fatalf("validation = %#v", record)
	}
	if record.Steps[0].ExitCode == nil || *record.Steps[0].ExitCode != 3 {
		t.Fatalf("exit code = %v", record.Steps[0].ExitCode)
	}

	failing := func(context.Context, string, ...string) *exec.Cmd {
		return exec.CommandContext(context.Background(), "/nonexistent/wb-validation-binary")
	}
	record = RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: runDir,
		Steps:   []ValidationStep{{Name: "missing", Argv: []string{"no-such-binary"}}},
		Command: failing, Now: time.Now,
	})
	if record.Status != ValidationFailed || record.Steps[0].ExitCode != nil {
		t.Fatalf("a step that cannot start = %#v", record)
	}
}

func TestE2EValidationSkipsWhenNothingIsConfigured(t *testing.T) {
	t.Parallel()
	record := RunValidation(context.Background(), ValidationOptions{RunDir: t.TempDir(), Now: time.Now})
	if record.Status != ValidationSkipped || record.Reason == "" {
		t.Fatalf("validation = %#v", record)
	}
}

func TestE2EValidationRefusesAnEmptyOrMalformedStep(t *testing.T) {
	t.Parallel()
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: t.TempDir(),
		Steps: []ValidationStep{
			{Name: "empty"},
			{Name: "bad timeout", Argv: []string{"true"}, Timeout: "not-a-duration"},
		},
		Command: func(context.Context, string, ...string) *exec.Cmd {
			return exec.CommandContext(context.Background(), "/usr/bin/true")
		},
		Now: time.Now,
	})
	if record.Status != ValidationFailed {
		t.Fatalf("validation = %#v", record)
	}
	if record.Steps[0].Status != ValidationSkipped {
		t.Fatalf("an empty argv must be skipped, got %#v", record.Steps[0])
	}
}

func TestE2EValidationReportsAnUnwritableLogDirectory(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	blocked := filepath.Join(runDir, validationLogDirName)
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: runDir,
		Steps: []ValidationStep{{Name: "one", Argv: []string{"true"}}},
		Now:   time.Now,
	})
	if record.Status != ValidationFailed || !strings.Contains(record.Reason, "log directory") {
		t.Fatalf("validation = %#v", record)
	}
}

func TestE2EValidationPassesTheInheritedLeaseToAStep(t *testing.T) {
	t.Parallel()
	leaseFile, err := os.CreateTemp(t.TempDir(), "lease")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = leaseFile.Close() })
	var extra int
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: t.TempDir(),
		Steps: []ValidationStep{{Name: "one", Argv: []string{"true"}}},
		Lease: leaseFile, Now: time.Now,
		Command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "/usr/bin/true")
			return cmd
		},
	})
	if record.Status != ValidationPassed {
		t.Fatalf("validation = %#v", record)
	}
	_ = extra
}

func TestE2ERunValidationHonoursAPerStepTimeout(t *testing.T) {
	t.Parallel()
	started := time.Now()
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: t.TempDir(),
		Steps: []ValidationStep{{Name: "slow", Argv: []string{"sleep", "5"}, Timeout: "50ms"}},
		Now:   time.Now,
		Command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/sleep", "5")
		},
	})
	if record.Status != ValidationFailed {
		t.Fatalf("a timed-out step must fail validation: %#v", record)
	}
	if time.Since(started) > 30*time.Second {
		t.Fatal("the step timeout was not enforced")
	}
}

func TestE2EClassifyFailureReadsTheHarnessDiagnostics(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"provider.no-route: Model unavailable": FailureProvider,
		"rate limit exceeded":                  FailureProvider,
		"credential missing":                   FailureCredential,
		"authentication failed":                FailureCredential,
		"something else":                       FailureHarness,
	}
	for diagnostic, want := range cases {
		summary := HarnessSummary{Diagnostics: []string{diagnostic}}
		if got := classifyFailure(summary); got != want {
			t.Fatalf("classifyFailure(%q) = %q, want %q", diagnostic, got, want)
		}
	}
}

func TestE2ECancelSentinelIsWrittenBeforeSignalling(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if cancelRequested(store, record.AgentID) {
		t.Fatal("a fresh run must not be marked cancelled")
	}
	if err := requestCancel(store, record.AgentID, func() time.Time { return time.Unix(5, 0) }); err != nil {
		t.Fatal(err)
	}
	if !cancelRequested(store, record.AgentID) {
		t.Fatal("the sentinel must be visible to the owner")
	}
}

func TestE2EValidationKeepsTheLeaseDescriptorNumberStable(t *testing.T) {
	t.Parallel()
	// The step's ExtraFiles is what keeps the kernel lock held; an empty lease
	// must leave the field alone rather than passing a nil descriptor.
	leaseFilesWithNil := leaseFiles(nil, 0)
	if leaseFilesWithNil != nil {
		t.Fatalf("no lease must produce no extra files: %#v", leaseFilesWithNil)
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestE2EOwnerRecordsAnUnknownHarnessAndAPreparationFailure(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessCodex, fakeHarnessScript, func(record *Record) {
		record.Resolved.Harness = "nope"
	})
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, _ := store.Load(record.AgentID)
	if loaded.FailureCategory != FailureConfig || loaded.ExecutionStatus != StatusFailed {
		t.Fatalf("unknown harness = %#v", loaded)
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestE2EOwnerRecordsAHarnessPreparationFailure(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessOpenCode, openCodeFakeHarness, func(record *Record) {
		record.Resolved.Model = "p/m#inline-variant"
	})
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, _ := store.Load(record.AgentID)
	if loaded.FailureCategory != FailureConfig || !strings.Contains(loaded.Failure, "variant") {
		t.Fatalf("preparation failure = %#v", loaded)
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestE2EOwnerRecordsAMissingHarnessBinary(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessCodex, fakeHarnessScript, nil)
	deps.LookPath = func(string) (string, error) { return "", errors.New("not on PATH") }
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, _ := store.Load(record.AgentID)
	if loaded.FailureCategory != FailureHarnessMiss {
		t.Fatalf("missing harness = %#v", loaded)
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestE2EOwnerRecordsAnUnopenableLogAndHarnessHome(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessCodex, fakeHarnessScript, nil)
	// A file where the private harness home belongs makes the directory fail.
	if err := os.WriteFile(store.HarnessHomePath(record.AgentID), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, _ := store.Load(record.AgentID)
	if loaded.FailureCategory != FailureInternal || !strings.Contains(loaded.Failure, "harness home") {
		t.Fatalf("harness home failure = %#v", loaded)
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestE2EOwnerRecordsAProcessThatCannotStart(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessCodex, fakeHarnessScript, nil)
	deps.LookPath = func(string) (string, error) { return "/nonexistent/wb-harness", nil }
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, _ := store.Load(record.AgentID)
	if loaded.FailureCategory != FailureHarness || !strings.Contains(loaded.Failure, "start") {
		t.Fatalf("start failure = %#v", loaded)
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestE2EOwnerTruncatesAChattyHarnessAndSaysSo(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessCodex, fakeHarnessScript, nil)
	if err := runOwnerWithSave(context.Background(), store, record.AgentID, deps, store.Save, 10); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, _ := store.Load(record.AgentID)
	if !containsDiagnostic(loaded.HarnessEvents, "truncated") {
		t.Fatalf("diagnostics = %#v", loaded.HarnessEvents)
	}
}

func TestE2EValidationDefaultsAndUnparsableTimeout(t *testing.T) {
	t.Parallel()
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: t.TempDir(),
		Steps: []ValidationStep{{Name: "", Argv: []string{"/usr/bin/true"}}, {Name: "bad", Argv: []string{"/usr/bin/true"}, Timeout: "soon"}},
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, name, args...)
		},
	})
	if record.Status != ValidationFailed {
		t.Fatalf("validation = %#v", record)
	}
	if record.Steps[0].LogPath == "" || !strings.HasSuffix(record.Steps[0].LogPath, "01-step.log") {
		t.Fatalf("an unnamed step must still be logged: %#v", record.Steps[0])
	}
	if record.Steps[1].LogPath != "" {
		t.Fatalf("an unparsable timeout must not run: %#v", record.Steps[1])
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestE2EOwnerInheritsNoLeaseFromAZeroDescriptor(t *testing.T) {
	//nolint:paralleltest // the test sets the dispatcher marker in the environment
	t.Setenv(leaseEnvMarker, "0")
	if lease := inheritedLease(); lease != nil {
		t.Fatalf("descriptor 0 must not be adopted: %#v", lease)
	}
	t.Setenv(leaseEnvMarker, "999999")
	if lease := inheritedLease(); lease != nil {
		t.Fatalf("a closed descriptor must not be adopted: %#v", lease)
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestE2EOwnerReportsAFailureToRecordTheWorkerAfterStartingIt(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessCodex, fakeHarnessScript, nil)
	saves := 0
	failingSave := func(record Record) error {
		saves++
		if saves == 2 {
			return errors.New("disk full")
		}
		return store.Save(record)
	}
	if err := runOwnerWithSave(context.Background(), store, record.AgentID, deps, failingSave, maxLogBytes); err == nil {
		t.Fatal("a save failure after start must be reported")
	}
}

//nolint:paralleltest // the fixture sets process environment
func TestE2EOwnerReportsAFailureToRecordItsOwnAdmission(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessCodex, fakeHarnessScript, nil)
	failing := func(Record) error { return errors.New("disk full") }
	if err := runOwnerWithSave(context.Background(), store, record.AgentID, deps, failing, maxLogBytes); err == nil {
		t.Fatal("an admission save failure must be reported")
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestE2EOwnerRecordsExitCodesForAnUnparsableOrSilentHarness(t *testing.T) {
	for name, script := range map[string]string{"malformed": malformedHarness, "silent": silentHarness} {
		//nolint:paralleltest // the fixture sets process environment
		t.Run(name, func(t *testing.T) {
			store, record, _, deps := ownedRunWithHarness(t, HarnessCodex, script, nil)
			if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
				t.Fatalf("RunOwner: %v", err)
			}
			loaded, _ := store.Load(record.AgentID)
			if loaded.ExitCode == nil {
				t.Fatalf("an exit code was expected: %#v", loaded)
			}
		})
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestE2EOwnerRefusesAnUnopenableLogAndInheritsAMarkerDescriptor(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessCodex, fakeHarnessScript, nil)
	if err := os.MkdirAll(record.LogPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, _ := store.Load(record.AgentID)
	if loaded.FailureCategory != FailureInternal || !strings.Contains(loaded.Failure, "run log") {
		t.Fatalf("log failure = %#v", loaded)
	}
}

func TestE2EResolveExistingTargetRecordsAnUnmanagedCheckout(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	runGit(t, worktree, "init", "-b", "main")
	runGit(t, worktree, "config", "user.email", "test@example.com")
	runGit(t, worktree, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktree, "add", "-A")
	runGit(t, worktree, "commit", "-qm", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, worktree, "worktree", "add", "-b", "linked", linked)
	deps := DispatchDeps{OriginSlug: func(context.Context, string) (string, error) { return "acme/app", nil }}
	target, err := resolveExistingTargetWithPolicy(context.Background(), DispatchRequest{Mode: ModePath, Worktree: linked}, deps, false)
	if err != nil {
		t.Fatalf("resolveExistingTargetWithPolicy: %v", err)
	}
	if target.Repository != "acme/app" || target.Branch != "linked" || target.BaseSHA == "" {
		t.Fatalf("target = %#v", target)
	}
	if target.Worktree != filepath.Base(target.Dir) {
		t.Fatalf("an unmanaged checkout is named by its directory: %q vs %q", target.Worktree, target.Dir)
	}
}

func TestE2ESummarizeChangesTruncatesUntrackedNamesWhenTheDiffAlreadyFilledTheList(t *testing.T) {
	t.Parallel()
	dir, base := initChangeRepo(t)
	for index := 0; index < maxChangedFiles+2; index++ {
		name := filepath.Join(dir, "tracked-"+itoaTest(index)+".txt")
		if err := os.WriteFile(name, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, dir, "add", "-A")
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	summary := SummarizeChangesWith(context.Background(), dir, base, nil)
	if summary == nil || !summary.Truncated || summary.FilesChanged <= maxChangedFiles {
		t.Fatalf("truncation = %#v", summary)
	}
	if len(summary.Untracked) != 1 || summary.Untracked[0] != "untracked.txt" {
		t.Fatalf("untracked = %#v", summary.Untracked)
	}
}

func TestE2ESummarizeChangesTruncatesTheStatusFileList(t *testing.T) {
	t.Parallel()
	// A repository with no commit has no base revision, so Git status is the
	// only evidence available.
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	for index := 0; index < maxChangedFiles+2; index++ {
		if err := os.WriteFile(filepath.Join(dir, "new-"+itoaTest(index)+".txt"), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	summary := SummarizeChangesWith(context.Background(), dir, "", nil)
	if summary == nil || !summary.Truncated || summary.FilesChanged <= maxChangedFiles {
		t.Fatalf("status truncation = %#v", summary)
	}
}

func TestE2ESummarizeChangesTruncatesBothSourcesOfFileNames(t *testing.T) {
	t.Parallel()
	// Tracked changes beyond the bound.
	dir, base := initChangeRepo(t)
	for index := 0; index < maxChangedFiles+3; index++ {
		name := filepath.Join(dir, "tracked", "file-"+itoaTest(index)+".txt")
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, dir, "add", "-A")
	summary := SummarizeChangesWith(context.Background(), dir, base, nil)
	if summary == nil || !summary.Truncated || summary.FilesChanged <= maxChangedFiles {
		t.Fatalf("tracked truncation = %#v", summary)
	}
}

//nolint:paralleltest // the dispatch/owner fixture sets process environment or a package seam
func TestE2EOwnerInheritsALeaseOnlyWithTheDispatcherMarker(t *testing.T) {
	//nolint:paralleltest // the test sets the dispatcher marker in the environment
	t.Setenv(leaseEnvMarker, "")
	if lease := inheritedLease(); lease != nil {
		t.Fatalf("no marker must mean no lease: %#v", lease)
	}
	t.Setenv(leaseEnvMarker, "not-a-number")
	if lease := inheritedLease(); lease != nil {
		t.Fatalf("a malformed marker must mean no lease: %#v", lease)
	}
	held, err := os.CreateTemp(t.TempDir(), "lease")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	// Adopt a duplicate, and close it through the adopted wrapper exactly once.
	// Wrapping the same descriptor twice would leave a finalizer that closes a
	// reused descriptor later.
	duplicate, err := unix.Dup(int(held.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(leaseEnvMarker, strconv.Itoa(duplicate))
	lease := inheritedLease()
	if lease == nil {
		t.Fatal("a valid marker must adopt the descriptor")
	}
	if info, err := lease.Stat(); err != nil {
		t.Fatalf("adopted lease: %v", err)
	} else if info.Size() != 0 {
		t.Fatalf("adopted the wrong file: %#v", info)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("close adopted lease: %v", err)
	}
}

//nolint:paralleltest // the fixture sets process environment and runs a scripted harness
func TestE2EDshKeepsStderrOutOfTheFinalMessage(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessDSH, `#!/bin/sh
printf 'the answer\n'
printf 'a diagnostic on stderr\n' >&2
exit 0
`, func(record *Record) {
		record.Resolved.Model = "deepseek-v4-flash"
		record.TaskVisibility = TaskDeliveryArgv
	})
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, _ := store.Load(record.AgentID)
	if loaded.Result != "the answer" {
		t.Fatalf("stderr leaked into the result: %q", loaded.Result)
	}
	stderr, err := os.ReadFile(filepath.Join(store.Dir(record.AgentID), stderrFileName))
	if err != nil || !strings.Contains(string(stderr), "a diagnostic on stderr") {
		t.Fatalf("stderr log = %q %v", stderr, err)
	}
}

//nolint:paralleltest // the fixture sets process environment and runs a scripted harness
func TestE2EOwnerReportsAnUnopenableStderrLog(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessDSH, dshFakeHarness, func(record *Record) {
		record.Resolved.Model = "deepseek-v4-flash"
	})
	// A directory where the stderr log belongs makes the split stream fail.
	if err := os.MkdirAll(filepath.Join(store.Dir(record.AgentID), stderrFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, _ := store.Load(record.AgentID)
	if loaded.FailureCategory != FailureInternal || !strings.Contains(loaded.Failure, "stderr log") {
		t.Fatalf("stderr failure = %#v", loaded)
	}
}

//nolint:paralleltest // the fixture sets process environment and runs a scripted harness
func TestE2EOwnerRecordsAPreCancelledQueuedAttempt(t *testing.T) {
	store, record, _, deps := ownedRunWithHarness(t, HarnessCodex, fakeHarnessScript, func(record *Record) {
		record.ExecutionStatus = StatusQueued
	})
	if err := requestCancel(store, record.AgentID, time.Now); err != nil {
		t.Fatal(err)
	}
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, _ := store.Load(record.AgentID)
	if loaded.ExecutionStatus != StatusCancelled || loaded.WorkerPID != 0 {
		t.Fatalf("pre-cancelled attempt = %#v", loaded)
	}
}

func itoaTest(value int) string {
	if value == 0 {
		return "0"
	}
	out := ""
	for value > 0 {
		out = string(rune('0'+value%10)) + out
		value /= 10
	}
	return out
}

func containsDiagnostic(diagnostics []string, fragment string) bool {
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic, fragment) {
			return true
		}
	}
	return false
}
