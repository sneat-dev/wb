package agents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	containedOwnerCredentialEnv = "WB_TEST_CONTAINED_OWNER_KEY"
	containedOwnerSecret        = "sk-contained-owner-secret"
	containedOwnerVersion       = "dsh 9.9.9"
	containedOwnerContainerID   = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	containedOwnerImageID       = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

// fakeContainedRuntime is the per-instance opener seam. It models one verified
// container whose probes, harness and validation all run through the same
// WorkerCommand surface, so the owner's ordering and credential use are
// observable without any Docker, host harness or provider call.
type fakeContainedRuntime struct {
	mutex sync.Mutex

	containerID string
	imageID     string
	limits      ContainerVerifiedLimits

	prepareHomeFn func(context.Context) error
	prepareHome   chan struct{}
	prepareErr    error
	probeErr      error
	closeErr      error
	closed        bool

	commands    [][]string
	credentials []Credential

	harnessStdout   string
	harnessStderr   string
	harnessExit     int
	harnessBlock    bool
	harnessMarker   string
	validationDone  chan struct{}
	validationBlock bool
}

func (fake *fakeContainedRuntime) ContainerID() string { return fake.containerID }
func (fake *fakeContainedRuntime) ImageID() string     { return fake.imageID }
func (fake *fakeContainedRuntime) VerifiedLimits() ContainerVerifiedLimits {
	return fake.limits
}

func (fake *fakeContainedRuntime) PrepareWorkerHome(ctx context.Context) error {
	if fake.prepareHome != nil {
		close(fake.prepareHome)
	}
	if fake.prepareHomeFn != nil {
		return fake.prepareHomeFn(ctx)
	}
	return fake.prepareErr
}

func (fake *fakeContainedRuntime) WorkerCommand(ctx context.Context, credential Credential, argv []string) (*exec.Cmd, error) {
	fake.mutex.Lock()
	fake.commands = append(fake.commands, append([]string(nil), argv...))
	fake.credentials = append(fake.credentials, credential)
	probeErr := fake.probeErr
	fake.mutex.Unlock()
	if probeErr != nil && containedProbeArgv(argv) {
		return nil, probeErr
	}
	switch {
	case len(argv) == 2 && argv[0] == HarnessDSH && argv[1] == "--version":
		return containedOwnerShell(ctx, containedOwnerVersion+"\n", "", 0), nil
	case len(argv) >= 2 && argv[0] == HarnessDSH && containsContainedArg(argv, "--dump-config"):
		return containedOwnerShell(ctx, containedOwnerDump(), "", 0), nil
	case containedHarnessArgv(argv):
		if fake.harnessBlock {
			if fake.harnessMarker != "" {
				return exec.CommandContext(ctx, "/bin/sh", "-c",
					"printf started > "+shellQuote(fake.harnessMarker)+"\nexec sleep 600"), nil
			}
			return containedBlockingCommand(ctx), nil
		}
		return containedOwnerShell(ctx, fake.harnessStdout, fake.harnessStderr, fake.harnessExit), nil
	default:
		if fake.validationBlock {
			return containedBlockingCommand(ctx), nil
		}
		return containedOwnerShell(ctx, "validation ok\n", "", 0), nil
	}
}

// containedProbeArgv reports whether argv is one of the bounded in-container
// read-only probes. The harness command also starts with the dsh executable, so
// a probe failure must be matched on the probe flags, not on argv[0].
func containedProbeArgv(argv []string) bool {
	if len(argv) == 0 || argv[0] != HarnessDSH {
		return false
	}
	return containsContainedArg(argv, "--version") || containsContainedArg(argv, "--dump-config")
}

// containedHarnessArgv reports whether argv is the prepared harness invocation:
// the dsh executable followed by --profile and no read-only probe flag. The
// credential-free --dump-config probe also starts dsh --profile, so the probe
// must be excluded by its precise flags rather than by argv[0] alone.
func containedHarnessArgv(argv []string) bool {
	return len(argv) >= 2 && argv[0] == HarnessDSH && argv[1] == "--profile" && !containedProbeArgv(argv)
}

func (fake *fakeContainedRuntime) ValidationTransport() ValidationCommandFactory {
	return func(ctx context.Context, argv []string) (*exec.Cmd, error) {
		return fake.WorkerCommand(ctx, Credential{}, argv)
	}
}

func (fake *fakeContainedRuntime) Close() error {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.closed = true
	return fake.closeErr
}

func (fake *fakeContainedRuntime) closedNow() bool {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return fake.closed
}

func (fake *fakeContainedRuntime) recordedCredentials() []Credential {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]Credential(nil), fake.credentials...)
}

func containsContainedArg(argv []string, wanted string) bool {
	for _, argument := range argv {
		if argument == wanted {
			return true
		}
	}
	return false
}

// containedBlockingCommand is the blocking fixture process. It must be the
// sleeping process itself, never a shell that spawns it: the owner's Wait also
// waits for the goroutines copying the command's output pipes, and a surviving
// child would keep those pipes open until the full sleep ended. Canceling the
// fixture then reaps the process promptly, exactly as a canceled container
// client would.
func containedBlockingCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "sleep", "600")
}

func containedOwnerShell(ctx context.Context, stdout, stderr string, exit int) *exec.Cmd {
	script := "printf '%s' " + shellQuote(stdout)
	if stderr != "" {
		script += "\nprintf '%s' " + shellQuote(stderr) + " >&2"
	}
	if exit != 0 {
		script += fmt.Sprintf("\nexit %d", exit)
	}
	return exec.CommandContext(ctx, "/bin/sh", "-c", script)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func containedOwnerDump() string {
	return `- id: agent-default-model
  config:
    provider: ` + ProviderDshDefault + `
    model: deepseek-flash
- id: llm-deepseek
  config:
    apiKeyEnv: ` + containedOwnerCredentialEnv + `
    baseURL: https://api.deepseek.com
    reasoningEffort: ` + containedDshReasoningEffort + `
    thinking: ` + containedDshThinking + `
    maxTokens: 32768
`
}

func containedOwnerWorkspace(t *testing.T) (worktree, gitCommon string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worktree = filepath.Join(root, "worktree")
	gitCommon = filepath.Join(root, "git-common")
	for _, directory := range []string{worktree, gitCommon} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return worktree, gitCommon
}

func containedOwnerRecord(t *testing.T, policy *ExecutionPolicy, worktree, gitCommon string) (Store, Record) {
	t.Helper()
	store := NewStore(t.TempDir())
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	record := Record{
		AgentID: id, State: StateRunning, ExecutionStatus: StatusQueued,
		RequestedProfile: "contained",
		Resolved: Resolved{
			Profile: "contained", Harness: HarnessDSH, Provider: ProviderDshDefault,
			Model: "deepseek-flash",
			Routing: Provider{
				BaseURL: "https://api.deepseek.com", CredentialEnv: containedOwnerCredentialEnv,
				WireAPI: WireAPIChat,
			},
		},
		Task: "do the contained thing", TaskSummary: "do the contained thing",
		Repository: "acme/app", Worktree: "task-one", WorktreeDir: worktree,
		WorktreeMode: ModeNew, TimeoutMS: time.Minute.Milliseconds(),
		StartedAt: time.Now().UTC(),
		Execution: SnapshotExecution(policy),
		ExecutionWorkspace: SnapshotExecutionWorkspace(&ContainerWorkspace{
			WorktreeDir: worktree, GitCommonDir: gitCommon,
		}),
		Validation: ValidationRecord{Status: ValidationPending, Steps: []ValidationStepResult{
			{Name: "check", Argv: []string{"validate"}, Status: ValidationPending},
		}},
	}
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	return store, record
}

func containedOwnerDeps(t *testing.T, policy ExecutionPolicy, fake *fakeContainedRuntime) OwnerDeps {
	t.Helper()
	deps := DefaultOwnerDeps()
	deps.LoadExecutionPolicy = func() (*ExecutionPolicy, error) {
		copied := policy
		return &copied, nil
	}
	deps.LookPath = func(string) (string, error) {
		t.Error("a contained owner must never look up a host harness")
		return "", errors.New("host probe attempted")
	}
	deps.openContainedRuntime = func(_ context.Context, options ContainerRuntimeOptions) (containedRuntime, error) {
		if options.Policy != policy {
			t.Errorf("runtime policy = %#v, want %#v", options.Policy, policy)
		}
		if options.Workspace == nil || options.Workspace.InputDir == "" {
			t.Errorf("runtime workspace = %#v, want the generated scoped input", options.Workspace)
		}
		if options.Lifetime <= 0 || options.Lifetime > maxContainerLifetime {
			t.Errorf("runtime lifetime = %s, want a positive value within %s", options.Lifetime, maxContainerLifetime)
		}
		return fake, nil
	}
	deps.CancelPollInterval = 5 * time.Millisecond
	return deps
}

func TestRunOwnerRunsMatchingContainedDshWithoutAnyHostProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	fake := &fakeContainedRuntime{
		containerID:   containedOwnerContainerID,
		imageID:       containedOwnerImageID,
		limits:        ContainerVerifiedLimits{MemoryBytes: policy.MemoryBytes, MemorySwapBytes: policy.MemorySwapBytes, CPUCount: policy.CPUCount, PIDsLimit: policy.PIDsLimit},
		harnessStdout: "contained final message " + containedOwnerSecret + "\n",
		harnessStderr: "credential seen on stderr: " + containedOwnerSecret + "\n",
	}
	deps := containedOwnerDeps(t, policy, fake)

	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateCompleted || loaded.ExecutionStatus != StatusCompleted {
		t.Fatalf("state = %s/%s failure=%q", loaded.State, loaded.ExecutionStatus, loaded.Failure)
	}
	if loaded.HarnessVersion != containedOwnerVersion {
		t.Fatalf("harness version = %q, want the reported %q", loaded.HarnessVersion, containedOwnerVersion)
	}
	if !strings.Contains(loaded.Result, "contained final message") {
		t.Fatalf("result = %q", loaded.Result)
	}
	if loaded.ExecutionRuntime == nil || !loaded.ExecutionRuntime.CleanupVerified ||
		loaded.ExecutionRuntime.CleanupPending || loaded.ExecutionRuntime.CleanupFailed {
		t.Fatalf("runtime facts = %#v", loaded.ExecutionRuntime)
	}
	if loaded.ExecutionRuntime.ContainerID != containedOwnerContainerID ||
		loaded.ExecutionRuntime.ResolvedImageID != containedOwnerImageID ||
		loaded.ExecutionRuntime.VerifiedLimits == nil ||
		*loaded.ExecutionRuntime.VerifiedLimits != fake.limits {
		t.Fatalf("runtime identity = %#v", loaded.ExecutionRuntime)
	}
	if !fake.closedNow() {
		t.Fatal("a completed contained run must close its runtime before reporting success")
	}
	// The harness gets exactly one credential; every probe and validation
	// command is credential-free.
	credentials := fake.recordedCredentials()
	if len(credentials) < 4 {
		t.Fatalf("worker commands = %#v", fake.commands)
	}
	harnessCredentials := 0
	for _, credential := range credentials {
		if credential.EnvName == "" && credential.Value == "" {
			continue
		}
		harnessCredentials++
		if credential.EnvName != containedOwnerCredentialEnv || credential.Value != containedOwnerSecret {
			t.Fatalf("unexpected credential injection = %#v", credential)
		}
	}
	if harnessCredentials != 1 {
		t.Fatalf("credential injections = %d, want exactly the harness", harnessCredentials)
	}
	// WorkerCommand emits `docker exec <ID> <argv>` and never guesses an
	// executable, so the harness argv must start with dsh and carry exactly the
	// prepared profile, patch and task arguments.
	wantHarnessArgv := []string{"dsh", "--profile", "headless", "--patch", "/wb-input/dsh-model-patch.yml", record.Task}
	var harnessArgvs [][]string
	for _, argv := range fake.commands {
		if containedHarnessArgv(argv) {
			harnessArgvs = append(harnessArgvs, argv)
		}
	}
	if len(harnessArgvs) != 1 {
		t.Fatalf("harness commands = %#v, want exactly one", harnessArgvs)
	}
	if !reflect.DeepEqual(harnessArgvs[0], wantHarnessArgv) {
		t.Fatalf("harness argv = %#v, want %#v", harnessArgvs[0], wantHarnessArgv)
	}
	// The actual contained execution must record truthful capabilities on the
	// existing axes, not the adapter's legacy host claim.
	if loaded.HarnessCapabilities.Credential != CredentialWBInjected {
		t.Fatalf("credential capability = %q, want %q", loaded.HarnessCapabilities.Credential, CredentialWBInjected)
	}
	if loaded.HarnessCapabilities.Isolation != IsolationLinuxContainer {
		t.Fatalf("isolation capability = %q, want %q", loaded.HarnessCapabilities.Isolation, IsolationLinuxContainer)
	}
	if loaded.HarnessCapabilities.Version != containedOwnerVersion {
		t.Fatalf("capability version = %q, want the in-container %q", loaded.HarnessCapabilities.Version, containedOwnerVersion)
	}
	logRaw, err := os.ReadFile(store.LogPath(record.AgentID))
	if err != nil {
		t.Fatal(err)
	}
	stderrRaw, err := os.ReadFile(filepath.Join(store.Dir(record.AgentID), stderrFileName))
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{"stdout": string(logRaw), "stderr": string(stderrRaw)} {
		if strings.Contains(raw, containedOwnerSecret) {
			t.Fatalf("%s leaked the provider credential: %q", name, raw)
		}
	}
	if !strings.Contains(string(stderrRaw), redactionMarker) {
		t.Fatalf("the separate stderr stream was not redacted: %q", string(stderrRaw))
	}
	if strings.Contains(loaded.Result, containedOwnerSecret) || strings.Contains(loaded.Failure, containedOwnerSecret) {
		t.Fatal("a credential reached the rendered result or failure")
	}
	for _, raw := range []string{fmt.Sprintf("%#v", loaded.ExecutionRuntime), fmt.Sprintf("%#v", loaded.Telemetry)} {
		if strings.Contains(raw, containedOwnerSecret) {
			t.Fatal("a credential reached runtime facts or telemetry")
		}
	}
}

func TestRunOwnerRefusesAnInContainerPreflightFailureBeforeStartingHarness(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	fake := &fakeContainedRuntime{
		containerID: containedOwnerContainerID, imageID: containedOwnerImageID,
		probeErr: errors.New("dsh is missing from the image"),
	}
	deps := containedOwnerDeps(t, policy, fake)
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExecutionStatus != StatusFailed || !strings.Contains(loaded.Failure, "dsh is missing") {
		t.Fatalf("failure = %q / %q", loaded.ExecutionStatus, loaded.Failure)
	}
	// A probe that fails inside the container is a configuration/overlay
	// refusal, not an infrastructure timeout, whenever the setup pool is live.
	if loaded.FailureCategory != FailureConfig {
		t.Fatalf("failure category = %q, want %q for a live-setup probe refusal", loaded.FailureCategory, FailureConfig)
	}
	if loaded.WorkerPID != 0 {
		t.Fatalf("a failed in-container preflight started a harness: worker PID %d", loaded.WorkerPID)
	}
	if !fake.closedNow() {
		t.Fatal("a failed preflight must close the runtime")
	}
	for _, argv := range fake.commands {
		if containedHarnessArgv(argv) {
			t.Fatalf("a failed preflight started the harness: %#v", fake.commands)
		}
	}
	if loaded.ExecutionRuntime == nil || !loaded.ExecutionRuntime.CleanupVerified {
		t.Fatalf("runtime facts = %#v", loaded.ExecutionRuntime)
	}
}

// executeContainedWithExpiredSetupPool drives the production setup path with the
// exact context pair the owner derives: a live total lifetime and a setup pool
// that has already expired. executeContained already takes both contexts, so
// this exercises the real setup-budget branch without a ten-minute wait and
// without adding a second budget seam.
func executeContainedWithExpiredSetupPool(t *testing.T, fake *fakeContainedRuntime) containedResult {
	t.Helper()
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	ctx := context.Background()
	setupCtx, cancelSetup := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancelSetup()
	return executeContained(ctx, setupCtx, store, record.AgentID, &record, DefaultOwnerDeps(), func(Record) error { return nil }, fake, Credential{}, ContainedDshLaunch{}, containedDshDefaultBaseURL)
}

func assertContainedSetupTimeout(t *testing.T, result containedResult, stage, detail string) {
	t.Helper()
	if result.State != StateTimeout || result.ExecutionStatus != StatusTimedOut || result.Category != FailureTimeout {
		t.Fatalf("result = %s/%s/%s (%q), want an infrastructure timeout", result.State, result.ExecutionStatus, result.Category, result.Failure)
	}
	if result.HarnessRan {
		t.Fatalf("an expired setup pool claimed a harness run: %#v", result)
	}
	for _, want := range []string{stage, context.DeadlineExceeded.Error()} {
		if !strings.Contains(result.Failure, want) {
			t.Fatalf("failure = %q, want %q", result.Failure, want)
		}
	}
	if detail != "" && !strings.Contains(result.Failure, detail) {
		t.Fatalf("failure = %q, want the underlying detail %q preserved", result.Failure, detail)
	}
}

func TestExecuteContainedClassifiesAnExpiredSetupPoolAsATimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Parallel()
	cases := []struct {
		name  string
		fake  *fakeContainedRuntime
		stage string
	}{
		{
			name:  "worker home preparation",
			fake:  &fakeContainedRuntime{prepareHomeFn: func(ctx context.Context) error { return ctx.Err() }},
			stage: "prepare the contained worker home",
		},
		{
			name:  "version probe",
			fake:  &fakeContainedRuntime{},
			stage: "read the contained dsh version",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			result := executeContainedWithExpiredSetupPool(t, testCase.fake)
			assertContainedSetupTimeout(t, result, testCase.stage, "")
			for _, argv := range testCase.fake.commands {
				if containedHarnessArgv(argv) {
					t.Fatalf("an expired setup pool started the harness: %#v", testCase.fake.commands)
				}
			}
		})
	}
}

func TestExecuteContainedKeepsAnOperatorStopAsCancellationWhenTheSetupPoolExpired(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Parallel()
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	if err := requestCancel(store, record.AgentID, time.Now); err != nil {
		t.Fatalf("requestCancel: %v", err)
	}
	ctx := context.Background()
	setupCtx, cancelSetup := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancelSetup()
	fake := &fakeContainedRuntime{prepareHomeFn: func(ctx context.Context) error { return ctx.Err() }}
	result := executeContained(ctx, setupCtx, store, record.AgentID, &record, DefaultOwnerDeps(), func(Record) error { return nil }, fake, Credential{}, ContainedDshLaunch{}, containedDshDefaultBaseURL)
	if result.ExecutionStatus != StatusCancelled || result.Category != FailureCancelled {
		t.Fatalf("result = %s/%s (%q), want an operator cancellation, not a setup timeout", result.ExecutionStatus, result.Category, result.Failure)
	}
}

func TestExecuteContainedKeepsAnOrdinaryProbeFailureAsConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Parallel()
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	ctx := context.Background()
	setupCtx, cancelSetup := context.WithTimeout(ctx, time.Minute)
	defer cancelSetup()
	fake := &fakeContainedRuntime{probeErr: errors.New("dsh overlay is invalid")}
	result := executeContained(ctx, setupCtx, store, record.AgentID, &record, DefaultOwnerDeps(), func(Record) error { return nil }, fake, Credential{}, ContainedDshLaunch{}, containedDshDefaultBaseURL)
	if result.ExecutionStatus != StatusFailed || result.Category != FailureConfig {
		t.Fatalf("result = %s/%s (%q), want an ordinary configuration failure", result.ExecutionStatus, result.Category, result.Failure)
	}
	if !strings.Contains(result.Failure, "dsh overlay is invalid") {
		t.Fatalf("failure = %q, want the configuration detail preserved", result.Failure)
	}
}

func TestContainedRuntimeOpenFailureClassifiesTheActualCause(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	const agentID = "agt-runtime-open-classification"
	live := context.Background()
	expiredSetup, cancelExpired := context.WithDeadline(live, time.Now().Add(-time.Second))
	defer cancelExpired()
	liveSetup, cancelLive := context.WithTimeout(live, time.Minute)
	defer cancelLive()

	t.Run("expired setup pool", func(t *testing.T) {
		t.Parallel()
		result := containedRuntimeOpenFailure(live, expiredSetup, store, agentID, errors.New("docker create: signal: killed"))
		assertContainedSetupTimeout(t, result, "open the verified container", "signal: killed")
	})

	t.Run("operator cancellation", func(t *testing.T) {
		t.Parallel()
		cancelled, cancel := context.WithCancel(live)
		cancel()
		result := containedRuntimeOpenFailure(cancelled, expiredSetup, store, agentID, errors.New("docker create: signal: killed"))
		if result.ExecutionStatus != StatusCancelled || result.Category != FailureCancelled {
			t.Fatalf("result = %s/%s (%q), want an operator cancellation", result.ExecutionStatus, result.Category, result.Failure)
		}
	})

	t.Run("expired total lifetime stays an execution timeout", func(t *testing.T) {
		t.Parallel()
		outer, cancel := context.WithDeadline(live, time.Now().Add(-time.Second))
		defer cancel()
		result := containedRuntimeOpenFailure(outer, expiredSetup, store, agentID, errors.New("docker create: signal: killed"))
		if result.ExecutionStatus != StatusTimedOut || result.Category != FailureTimeout {
			t.Fatalf("result = %s/%s (%q), want the total lifetime timeout", result.ExecutionStatus, result.Category, result.Failure)
		}
		if strings.Contains(result.Failure, "setup budget") {
			t.Fatalf("failure = %q, want the total lifetime timeout, not a setup budget claim", result.Failure)
		}
	})

	t.Run("ordinary open failure", func(t *testing.T) {
		t.Parallel()
		result := containedRuntimeOpenFailure(live, liveSetup, store, agentID, errors.New("docker is missing"))
		if result.State != StateFailed || result.ExecutionStatus != StatusFailed || result.Category != FailureInternal {
			t.Fatalf("result = %s/%s/%s (%q), want an internal open failure", result.State, result.ExecutionStatus, result.Category, result.Failure)
		}
		if !strings.Contains(result.Failure, "open the verified container") || !strings.Contains(result.Failure, "docker is missing") {
			t.Fatalf("failure = %q, want the actual stage and detail", result.Failure)
		}
	})
}

func TestRunOwnerRecordsAnOrdinaryContainedRuntimeOpenFailureAsInternal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	deps := containedOwnerDeps(t, policy, &fakeContainedRuntime{})
	deps.openContainedRuntime = func(context.Context, ContainerRuntimeOptions) (containedRuntime, error) {
		return nil, errors.New("docker: cannot connect to the daemon")
	}
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateFailed || loaded.ExecutionStatus != StatusFailed || loaded.FailureCategory != FailureInternal {
		t.Fatalf("record = %s/%s/%s (%q), want an internal open failure", loaded.State, loaded.ExecutionStatus, loaded.FailureCategory, loaded.Failure)
	}
	if !strings.Contains(loaded.Failure, "open the verified container") || !strings.Contains(loaded.Failure, "cannot connect") {
		t.Fatalf("failure = %q, want the actual stage and detail", loaded.Failure)
	}
	if loaded.ExecutionRuntime != nil {
		t.Fatalf("a refused open must not claim runtime facts: %#v", loaded.ExecutionRuntime)
	}
}

func TestRunOwnerCancelsContainedSetupBeforeAnyHarnessPIDExists(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	entered := make(chan struct{})
	fake := &fakeContainedRuntime{
		containerID: containedOwnerContainerID, imageID: containedOwnerImageID,
		prepareHome: entered,
		prepareHomeFn: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	deps := containedOwnerDeps(t, policy, fake)
	done := make(chan error, 1)
	go func() { done <- RunOwner(context.Background(), store, record.AgentID, deps) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the owner never entered worker-home preparation")
	}
	if _, err := StopRun(store, record.AgentID, DefaultOwnerDeps()); err != nil {
		t.Fatalf("StopRun: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunOwner: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the owner did not stop after the cancellation sentinel")
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExecutionStatus != StatusCancelled {
		t.Fatalf("status = %q, want cancelled", loaded.ExecutionStatus)
	}
	if loaded.WorkerPID != 0 {
		t.Fatalf("worker PID = %d, want zero during setup", loaded.WorkerPID)
	}
	if !fake.closedNow() {
		t.Fatal("cancellation must close the whole runtime")
	}
}

func TestRunOwnerCancelsContainedValidationAndClosesTheRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	validationStarted := make(chan struct{})
	fake := &fakeContainedRuntime{
		containerID: containedOwnerContainerID, imageID: containedOwnerImageID,
		harnessStdout:  "contained final message\n",
		validationDone: validationStarted,
	}
	deps := containedOwnerDeps(t, policy, fake)
	// Block the validation command until the owner's context is canceled by
	// the sentinel, and signal when validation has actually started.
	originalOpener := deps.openContainedRuntime
	deps.openContainedRuntime = func(ctx context.Context, options ContainerRuntimeOptions) (containedRuntime, error) {
		fake.validationBlock = true
		return originalOpener(ctx, options)
	}
	deps.Validation = func(ctx context.Context, options ValidationOptions) ValidationRecord {
		close(validationStarted)
		command, err := options.Transport(ctx, []string{"validate"})
		if err != nil || command == nil {
			return ValidationRecord{Status: ValidationFailed, Reason: "transport unavailable"}
		}
		if err := command.Run(); err != nil {
			return ValidationRecord{Status: ValidationFailed, Reason: err.Error()}
		}
		return ValidationRecord{Status: ValidationPassed}
	}
	done := make(chan error, 1)
	go func() { done <- RunOwner(context.Background(), store, record.AgentID, deps) }()
	select {
	case <-validationStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the owner never reached validation")
	}
	if _, err := StopRun(store, record.AgentID, DefaultOwnerDeps()); err != nil {
		t.Fatalf("StopRun: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunOwner: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the owner did not stop during validation")
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExecutionStatus != StatusCancelled {
		t.Fatalf("status = %q, want cancelled", loaded.ExecutionStatus)
	}
	if !fake.closedNow() {
		t.Fatal("cancellation during validation must close the whole runtime")
	}
}

func TestRunOwnerRefusesAContainedLifetimeBeyondTheMaximumBeforeOpening(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	record.TimeoutMS = int64((maxContainerLifetime + time.Hour) / time.Millisecond)
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	opened := false
	deps := containedOwnerDeps(t, policy, &fakeContainedRuntime{})
	deps.openContainedRuntime = func(context.Context, ContainerRuntimeOptions) (containedRuntime, error) {
		opened = true
		return &fakeContainedRuntime{}, nil
	}
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	if opened {
		t.Fatal("an over-long lifetime must be refused before a container is opened")
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExecutionStatus != StatusFailed || loaded.ExecutionRuntime != nil {
		t.Fatalf("record = %#v", loaded)
	}
}

func TestRunOwnerRefusesToReportCompletedWhenContainedCleanupFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	fake := &fakeContainedRuntime{
		containerID: containedOwnerContainerID, imageID: containedOwnerImageID,
		harnessStdout: "contained final message " + containedOwnerSecret + "\n",
		closeErr:      errors.New("the container is still present"),
	}
	deps := containedOwnerDeps(t, policy, fake)
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State == StateCompleted || loaded.ExecutionStatus == StatusCompleted {
		t.Fatalf("a failed cleanup must not report completed: %#v", loaded)
	}
	if loaded.ExecutionStatus != StatusFailed || loaded.FailureCategory != FailureInternal {
		t.Fatalf("status/category = %q/%q (%s)", loaded.ExecutionStatus, loaded.FailureCategory, loaded.Failure)
	}
	if loaded.ExecutionRuntime == nil || !loaded.ExecutionRuntime.CleanupFailed ||
		loaded.ExecutionRuntime.CleanupVerified || loaded.ExecutionRuntime.CleanupPending {
		t.Fatalf("cleanup facts = %#v", loaded.ExecutionRuntime)
	}
	if !strings.Contains(loaded.ExecutionRuntime.CleanupFailure, "still present") {
		t.Fatalf("cleanup failure = %q", loaded.ExecutionRuntime.CleanupFailure)
	}
}

func TestRunOwnerClosesTheContainedRuntimeWhenARecordSaveFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	fake := &fakeContainedRuntime{
		containerID: containedOwnerContainerID, imageID: containedOwnerImageID,
		harnessStdout: "contained final message " + containedOwnerSecret + "\n",
	}
	deps := containedOwnerDeps(t, policy, fake)
	save := func(candidate Record) error {
		if candidate.ExecutionRuntime != nil {
			return errors.New("record store is full")
		}
		return store.Save(candidate)
	}
	err := runOwnerWithSave(context.Background(), store, record.AgentID, deps, save, maxLogBytes)
	if err == nil || !strings.Contains(err.Error(), "record store is full") {
		t.Fatalf("error = %v, want the save failure", err)
	}
	if !fake.closedNow() {
		t.Fatal("a save failure must close the runtime")
	}
}

func TestRunOwnerDefaultsANilLoaderToTheAuthoritativePolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "wb"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := executionYAML(ExecutionModeLinuxContainer, executionTestImage, 8589934592, 8589934592, 2, 128, 1)
	if err := os.WriteFile(filepath.Join(configHome, "wb", "wb.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store, record := containedOwnerRun(t, nil)
	deps := DefaultOwnerDeps()
	// Deliberately no LoadExecutionPolicy: RunOwner must default it.
	lookups := 0
	deps.LookPath = func(string) (string, error) {
		lookups++
		return "", errors.New("host probe attempted")
	}
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	if lookups != 0 {
		t.Fatalf("a stale host record reached LookPath %d times", lookups)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loaded.Failure, "carries no mandatory policy snapshot") {
		t.Fatalf("failure = %q, want the stale-host refusal", loaded.Failure)
	}
	if loaded.ExecutionStatus != StatusFailed || loaded.ExecutionRuntime != nil {
		t.Fatalf("record = %#v", loaded)
	}
}

func TestRunOwnerCompletesAContainedHarnessWithNoValidationSuites(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	// The legacy no-suites record snapshots zero validation steps.
	record.Validation = ValidationRecord{Status: ValidationPending}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	fake := &fakeContainedRuntime{
		containerID: containedOwnerContainerID, imageID: containedOwnerImageID,
		harnessStdout: "contained final message\n",
	}
	deps := containedOwnerDeps(t, policy, fake)
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateCompleted || loaded.ExecutionStatus != StatusCompleted {
		t.Fatalf("state = %s/%s failure=%q", loaded.State, loaded.ExecutionStatus, loaded.Failure)
	}
	if loaded.Validation.Status != ValidationSkipped || !strings.Contains(loaded.Validation.Reason, "no validation steps") {
		t.Fatalf("validation = %#v, want the legacy skipped no-suites record", loaded.Validation)
	}
}

func TestRunOwnerFailsAContainedRunWhenAConfiguredValidatorSkips(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	fake := &fakeContainedRuntime{
		containerID: containedOwnerContainerID, imageID: containedOwnerImageID,
		harnessStdout: "contained final message\n",
	}
	deps := containedOwnerDeps(t, policy, fake)
	deps.Validation = func(_ context.Context, options ValidationOptions) ValidationRecord {
		if len(options.Steps) != 1 {
			t.Errorf("validation steps = %#v, want the one snapshotted step", options.Steps)
		}
		return ValidationRecord{Status: ValidationSkipped, Reason: "the validator declined to run"}
	}
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State == StateCompleted || loaded.ExecutionStatus == StatusCompleted {
		t.Fatalf("a configured check that skipped must not complete: %#v", loaded)
	}
	if loaded.ExecutionStatus != StatusFailed || loaded.FailureCategory != FailureValidation {
		t.Fatalf("status/category = %q/%q (%s)", loaded.ExecutionStatus, loaded.FailureCategory, loaded.Failure)
	}
	if loaded.Validation.Status != ValidationSkipped || loaded.Validation.Reason != "the validator declined to run" {
		t.Fatalf("validation = %#v, want the validator's own skipped record preserved", loaded.Validation)
	}
	if !fake.closedNow() {
		t.Fatal("a failed validation must still close the runtime")
	}
}

func TestRunOwnerPreservesPassedValidationWhenContainedCleanupFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	fake := &fakeContainedRuntime{
		containerID: containedOwnerContainerID, imageID: containedOwnerImageID,
		harnessStdout: "contained final message\n",
		closeErr:      errors.New("the container is still present"),
	}
	deps := containedOwnerDeps(t, policy, fake)
	deps.Validation = func(context.Context, ValidationOptions) ValidationRecord {
		return ValidationRecord{Status: ValidationPassed}
	}
	var saved []Record
	save := func(candidate Record) error {
		saved = append(saved, candidate)
		return store.Save(candidate)
	}
	if err := runOwnerWithSave(context.Background(), store, record.AgentID, deps, save, maxLogBytes); err != nil {
		t.Fatalf("runOwnerWithSave: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	// The cleanup failure must fail the execution and block the slot...
	if loaded.ExecutionStatus != StatusFailed || loaded.ExecutionRuntime == nil ||
		!loaded.ExecutionRuntime.CleanupFailed || loaded.ExecutionRuntime.CleanupVerified {
		t.Fatalf("status/cleanup = %q/%#v", loaded.ExecutionStatus, loaded.ExecutionRuntime)
	}
	// ...while the check that actually ran keeps its real evidence.
	if loaded.Validation.Status != ValidationPassed {
		t.Fatalf("validation = %#v, want the performed passed check retained", loaded.Validation)
	}
	for _, candidate := range saved {
		if candidate.State == StateCompleted || candidate.ExecutionStatus == StatusCompleted {
			t.Fatalf("a cleanup failure persisted a completed record: %#v", candidate)
		}
	}
}

func TestRunOwnerBoundsTheContainedGitMetadataContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	fake := &fakeContainedRuntime{
		containerID: containedOwnerContainerID, imageID: containedOwnerImageID,
		harnessStdout: "contained final message\n",
	}
	deps := containedOwnerDeps(t, policy, fake)
	var observed context.Context
	deps.observeGitMetadataContext = func(metadataCtx context.Context) { observed = metadataCtx }
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	if observed == nil {
		t.Fatal("the post-run Git metadata snapshots were never given a context")
	}
	deadline, ok := observed.Deadline()
	if !ok {
		t.Fatal("the Git metadata context has no finite deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > containedGitMetadataTimeout {
		t.Fatalf("Git metadata deadline is in %s, want it within %s", remaining, containedGitMetadataTimeout)
	}
}

func TestContainedGitMetadataContextSurvivesACancelledOwnerButStaysFinite(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	metadataCtx, stop := containedGitMetadataContext(parent, DefaultOwnerDeps())
	defer stop()
	if metadataCtx.Err() != nil {
		t.Fatalf("the metadata context was cancelled with the owner: %v", metadataCtx.Err())
	}
	deadline, ok := metadataCtx.Deadline()
	if !ok {
		t.Fatal("the metadata context has no finite deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > containedGitMetadataTimeout {
		t.Fatalf("metadata deadline is in %s, want it within %s", remaining, containedGitMetadataTimeout)
	}
}

func TestVerifyContainedDshDumpRequiresThePinnedFlashDefaults(t *testing.T) {
	t.Parallel()
	valid := containedOwnerDump()
	cases := map[string]struct {
		dump string
		want string
	}{
		"valid": {
			dump: valid,
		},
		"wrong reasoning effort": {
			dump: strings.Replace(valid, "reasoningEffort: "+containedDshReasoningEffort, "reasoningEffort: high", 1),
			want: "reasoningEffort",
		},
		"missing reasoning effort": {
			dump: strings.Replace(valid, "    reasoningEffort: "+containedDshReasoningEffort+"\n", "", 1),
			want: "reasoningEffort",
		},
		"thinking disabled": {
			dump: strings.Replace(valid, "thinking: "+containedDshThinking, "thinking: disabled", 1),
			want: "thinking",
		},
		"missing thinking": {
			dump: strings.Replace(valid, "    thinking: "+containedDshThinking+"\n", "", 1),
			want: "thinking",
		},
		"wrong max tokens": {
			dump: strings.Replace(valid, "maxTokens: 32768", "maxTokens: 65536", 1),
			want: "maxTokens",
		},
		"max tokens quoted as text": {
			dump: strings.Replace(valid, "maxTokens: 32768", `maxTokens: "32768"`, 1),
			want: "maxTokens",
		},
		"max tokens not decimal": {
			dump: strings.Replace(valid, "maxTokens: 32768", "maxTokens: 0x8000", 1),
			want: "maxTokens",
		},
		"missing max tokens": {
			dump: strings.Replace(valid, "    maxTokens: 32768\n", "", 1),
			want: "maxTokens",
		},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := verifyContainedDshDump([]byte(testCase.dump), "deepseek-flash", containedOwnerCredentialEnv, "https://api.deepseek.com")
			if testCase.want == "" {
				if err != nil {
					t.Fatalf("the exact composed configuration must verify: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want a %q mismatch", err, testCase.want)
			}
		})
	}
}

// waitForObservedFile blocks until path exists or the test deadline passes. It
// waits for a real process observation instead of sleeping for a guessed
// duration, so the ordering assertion stays deterministic on a slow host.
func waitForObservedFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("the contained harness never started: %s did not appear", path)
}

func TestContainedOwnerReadsGitOnlyAfterTheRuntimeIsClosedOnSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	fake := &fakeContainedRuntime{
		containerID: containedOwnerContainerID, imageID: containedOwnerImageID,
		harnessStdout: "contained final message\n",
	}
	deps := containedOwnerDeps(t, policy, fake)
	var closedAtObservation []bool
	deps.observeGitMetadataContext = func(context.Context) {
		closedAtObservation = append(closedAtObservation, fake.closedNow())
	}
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	if len(closedAtObservation) != 1 || !closedAtObservation[0] {
		t.Fatalf("Git metadata was observed with runtime closed = %v, want exactly one observation after Close", closedAtObservation)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateCompleted || loaded.ExecutionStatus != StatusCompleted {
		t.Fatalf("state = %s/%s failure=%q", loaded.State, loaded.ExecutionStatus, loaded.Failure)
	}
}

// TestFakeContainedBlockingCommandsReapOnCancellation pins the blocking
// fixtures to a single process that owns the output pipe. A shell wrapper that
// spawns `sleep` would survive the cancellation of the shell and keep the pipe
// open, so the owner's Wait would block for the whole sleep instead of the
// fixture being reaped promptly.
func TestFakeContainedBlockingCommandsReapOnCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX blocking command")
	}
	cases := map[string]struct {
		block func(*fakeContainedRuntime)
		argv  []string
	}{
		"harness": {
			block: func(fake *fakeContainedRuntime) { fake.harnessBlock = true },
			argv:  []string{HarnessDSH, "--profile", "fixture"},
		},
		"validation": {
			block: func(fake *fakeContainedRuntime) { fake.validationBlock = true },
			argv:  []string{"validate", "fixture"},
		},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeContainedRuntime{
				containerID: containedOwnerContainerID, imageID: containedOwnerImageID,
			}
			testCase.block(fake)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			command, err := fake.WorkerCommand(ctx, Credential{}, testCase.argv)
			if err != nil {
				t.Fatalf("WorkerCommand: %v", err)
			}
			if base := filepath.Base(command.Path); base != "sleep" {
				t.Fatalf("blocking fixture command = %q, want the sleeping process itself so cancellation leaves no orphan holding the output pipe", command.Path)
			}
			var stdout, stderr bytes.Buffer
			// A non-*os.File writer makes os/exec own a pipe and copy it from a
			// goroutine, which Wait must not block on after cancellation.
			command.Stdout = &stdout
			command.Stderr = &stderr
			if err := command.Start(); err != nil {
				t.Fatalf("start blocking fixture command: %v", err)
			}
			cancel()
			reaped := make(chan error, 1)
			go func() { reaped <- command.Wait() }()
			select {
			case err := <-reaped:
				if err == nil {
					t.Fatal("a canceled blocking fixture command must report the cancellation")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("a canceled blocking fixture command did not reap promptly: an orphaned child still holds the output pipe")
			}
		})
	}
}

func TestContainedOwnerReadsGitOnlyAfterTheRuntimeIsClosedOnTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	record.TimeoutMS = 500
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	fake := &fakeContainedRuntime{
		containerID: containedOwnerContainerID, imageID: containedOwnerImageID,
		harnessBlock: true,
	}
	deps := containedOwnerDeps(t, policy, fake)
	var closedAtObservation []bool
	deps.observeGitMetadataContext = func(context.Context) {
		closedAtObservation = append(closedAtObservation, fake.closedNow())
	}
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	if len(closedAtObservation) != 1 || !closedAtObservation[0] {
		t.Fatalf("Git metadata was observed with runtime closed = %v, want exactly one observation after Close", closedAtObservation)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExecutionStatus != StatusTimedOut || loaded.State != StateTimeout {
		t.Fatalf("state = %s/%s, want a timeout", loaded.State, loaded.ExecutionStatus)
	}
	if strings.Contains(loaded.Failure, "process group") {
		t.Fatalf("a contained timeout must not claim a host process group was killed: %q", loaded.Failure)
	}
}

func TestContainedOwnerReadsGitOnlyAfterTheRuntimeIsClosedOnCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	marker := filepath.Join(t.TempDir(), "harness-started")
	fake := &fakeContainedRuntime{
		containerID: containedOwnerContainerID, imageID: containedOwnerImageID,
		harnessBlock: true, harnessMarker: marker,
	}
	deps := containedOwnerDeps(t, policy, fake)
	var closedAtObservation []bool
	deps.observeGitMetadataContext = func(context.Context) {
		closedAtObservation = append(closedAtObservation, fake.closedNow())
	}
	done := make(chan error, 1)
	go func() { done <- RunOwner(context.Background(), store, record.AgentID, deps) }()
	waitForObservedFile(t, marker)
	if _, err := StopRun(store, record.AgentID, DefaultOwnerDeps()); err != nil {
		t.Fatalf("StopRun: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunOwner: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the owner did not stop after the cancellation sentinel")
	}
	if len(closedAtObservation) != 1 || !closedAtObservation[0] {
		t.Fatalf("Git metadata was observed with runtime closed = %v, want exactly one observation after Close", closedAtObservation)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExecutionStatus != StatusCancelled {
		t.Fatalf("status = %q, want cancelled", loaded.ExecutionStatus)
	}
}

func TestContainedOwnerNeverReadsGitWhenCleanupFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	fake := &fakeContainedRuntime{
		containerID: containedOwnerContainerID, imageID: containedOwnerImageID,
		harnessStdout: "contained final message\n",
		closeErr:      errors.New("the container is still present"),
	}
	deps := containedOwnerDeps(t, policy, fake)
	observed := 0
	deps.observeGitMetadataContext = func(context.Context) { observed++ }
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	if observed != 0 {
		t.Fatalf("a failed cleanup captured %d Git metadata snapshots, want none", observed)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExecutionStatus != StatusFailed || loaded.Changes != nil {
		t.Fatalf("a failed cleanup must not claim a stable change summary: %#v changes=%#v", loaded.ExecutionStatus, loaded.Changes)
	}
}

func TestContainedOwnerRequestsFailFastValidationInTheSameContainer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the contained owner fixture uses a POSIX shell command")
	}
	t.Setenv(containedOwnerCredentialEnv, containedOwnerSecret)
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	store, record := containedOwnerRecord(t, &policy, worktree, gitCommon)
	record.Validation = ValidationRecord{Status: ValidationPending, Steps: []ValidationStepResult{
		{Name: "first", Argv: []string{"validate", "first"}, Status: ValidationPending},
		{Name: "second", Argv: []string{"validate", "second"}, Status: ValidationPending},
	}}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	fake := &fakeContainedRuntime{
		containerID: containedOwnerContainerID, imageID: containedOwnerImageID,
		harnessStdout: "contained final message\n",
	}
	deps := containedOwnerDeps(t, policy, fake)
	var captured ValidationOptions
	deps.Validation = func(_ context.Context, options ValidationOptions) ValidationRecord {
		captured = options
		return ValidationRecord{Status: ValidationPassed}
	}
	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	if !captured.FailFast {
		t.Fatal("contained validation must be fail-fast so a timed-out step cannot overlap a later one")
	}
	if captured.Transport == nil {
		t.Fatal("contained validation must use the runtime's in-container transport")
	}
	if len(captured.Steps) != 2 {
		t.Fatalf("validation steps = %#v, want both snapshotted steps", captured.Steps)
	}
}
