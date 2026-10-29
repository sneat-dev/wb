//go:build e2e

package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// containedExecuteFixture builds the admitted record the contained owner would
// receive after a matching policy snapshot. The fake runtime is the only
// execution surface: no host harness, Docker daemon or provider is contacted.
func containedExecuteFixture(t *testing.T) (Store, Record) {
	t.Helper()
	policy := executionTestPolicy()
	worktree, gitCommon := containedOwnerWorkspace(t)
	return containedOwnerRecord(t, &policy, worktree, gitCommon)
}

func executeContainedFixture(t *testing.T, fake *fakeContainedRuntime, mutate func(*Record), save func(Record) error) (containedResult, *Record, Store) {
	t.Helper()
	store, record := containedExecuteFixture(t)
	if mutate != nil {
		mutate(&record)
	}
	if save == nil {
		save = func(Record) error { return nil }
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	launch := ContainedDshLaunch{Launch: HarnessLaunch{Argv: []string{"--profile", dshProfile, record.Task}}}
	result := executeContained(ctx, ctx, store, record.AgentID, &record, DefaultOwnerDeps(), save, fake,
		Credential{EnvName: containedOwnerCredentialEnv, Value: containedOwnerSecret}, launch, containedDshDefaultBaseURL)
	return result, &record, store
}

func TestE2EExecuteContainedStopsBeforeAnyProbeWhenCancellationWasRequested(t *testing.T) {
	t.Parallel()
	store, record := containedExecuteFixture(t)
	if err := requestCancel(store, record.AgentID, time.Now); err != nil {
		t.Fatalf("requestCancel: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	fake := &fakeContainedRuntime{}
	result := executeContained(ctx, ctx, store, record.AgentID, &record, DefaultOwnerDeps(),
		func(Record) error { return nil }, fake, Credential{}, ContainedDshLaunch{}, containedDshDefaultBaseURL)
	if result.ExecutionStatus != StatusCancelled || result.Category != FailureCancelled {
		t.Fatalf("result = %s/%s (%q), want the recorded cancellation", result.ExecutionStatus, result.Category, result.Failure)
	}
	if len(fake.commands) != 0 {
		t.Fatalf("a cancelled setup ran probes: %#v", fake.commands)
	}
}

func TestE2EExecuteContainedClassifiesProbeAndCompositionRefusals(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		fake     *fakeContainedRuntime
		want     string
		commands int
	}{
		"version probe reports nothing": {
			fake: &fakeContainedRuntime{versionOutput: stringPointer("\n")},
			want: "dsh --version reported no version",
		},
		"dump probe fails": {
			fake: &fakeContainedRuntime{workerCommandFn: func(_ context.Context, _ Credential, argv []string) (*exec.Cmd, error) {
				if containsContainedArg(argv, "--dump-config") {
					return nil, errors.New("dsh cannot compose its configuration")
				}
				return containedOwnerShell(context.Background(), containedOwnerVersion+"\n", "", 0), nil
			}},
			want: "dump the contained dsh configuration",
		},
		"composed configuration does not name the resolved model": {
			fake: &fakeContainedRuntime{dumpOutput: stringPointer(strings.Replace(containedOwnerDump(), "model: deepseek-flash", "model: other-model", 1))},
			want: "not the resolved model",
		},
		"composed configuration has no routing entry": {
			fake: &fakeContainedRuntime{dumpOutput: stringPointer("- id: agent-default-model\n  config:\n    provider: " + ProviderDshDefault + "\n    model: deepseek-flash\n")},
			want: "no llm-deepseek entry",
		},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, _, _ := executeContainedFixture(t, testCase.fake, nil, nil)
			if result.ExecutionStatus != StatusFailed || result.Category != FailureConfig {
				t.Fatalf("result = %s/%s (%q), want a configuration refusal", result.ExecutionStatus, result.Category, result.Failure)
			}
			if !strings.Contains(result.Failure, testCase.want) {
				t.Fatalf("failure = %q, want %q", result.Failure, testCase.want)
			}
			if result.HarnessRan {
				t.Fatalf("a refused setup claimed the harness ran: %#v", result)
			}
		})
	}
}

func TestE2EExecuteContainedFailsWhenARunLogCannotBeOpened(t *testing.T) {
	t.Parallel()
	t.Run("stdout run log", func(t *testing.T) {
		t.Parallel()
		store, record := containedExecuteFixture(t)
		if err := os.RemoveAll(store.Dir(record.AgentID)); err != nil {
			t.Fatalf("remove run directory: %v", err)
		}
		fake := &fakeContainedRuntime{}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		t.Cleanup(cancel)
		result := executeContained(ctx, ctx, store, record.AgentID, &record, DefaultOwnerDeps(),
			func(Record) error { return nil }, fake,
			Credential{EnvName: containedOwnerCredentialEnv, Value: containedOwnerSecret}, ContainedDshLaunch{}, containedDshDefaultBaseURL)
		if result.ExecutionStatus != StatusFailed || !strings.Contains(result.Failure, "open the contained run log") {
			t.Fatalf("result = %s (%q), want the run log refusal", result.ExecutionStatus, result.Failure)
		}
	})
	t.Run("separate stderr log", func(t *testing.T) {
		t.Parallel()
		store, record := containedExecuteFixture(t)
		stderrPath := filepath.Join(store.Dir(record.AgentID), stderrFileName)
		if err := os.Symlink(filepath.Join(t.TempDir(), "absent", "stderr.log"), stderrPath); err != nil {
			t.Fatalf("install failing stderr symlink: %v", err)
		}
		fake := &fakeContainedRuntime{}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		t.Cleanup(cancel)
		result := executeContained(ctx, ctx, store, record.AgentID, &record, DefaultOwnerDeps(),
			func(Record) error { return nil }, fake,
			Credential{EnvName: containedOwnerCredentialEnv, Value: containedOwnerSecret}, ContainedDshLaunch{}, containedDshDefaultBaseURL)
		if result.ExecutionStatus != StatusFailed || !strings.Contains(result.Failure, "open the contained run stderr log") {
			t.Fatalf("result = %s (%q), want the stderr log refusal", result.ExecutionStatus, result.Failure)
		}
	})
}

func TestE2EExecuteContainedReportsACommandThatCannotStart(t *testing.T) {
	t.Parallel()
	fake := &fakeContainedRuntime{harnessCmd: func(context.Context) *exec.Cmd {
		return exec.Command(filepath.Join(t.TempDir(), "missing-dsh"))
	}}
	result, _, _ := executeContainedFixture(t, fake, nil, nil)
	if result.ExecutionStatus != StatusFailed || !strings.Contains(result.Failure, "start dsh inside the contained execution") {
		t.Fatalf("result = %s (%q), want the start refusal", result.ExecutionStatus, result.Failure)
	}
	if result.HarnessRan {
		t.Fatalf("a command that never started claimed a harness run: %#v", result)
	}
}

func TestE2EExecuteContainedRefusesAnUnresolvableHarnessAdapterAfterTheHarnessRan(t *testing.T) {
	t.Parallel()
	fake := &fakeContainedRuntime{harnessStdout: "contained final message\n"}
	result, _, _ := executeContainedFixture(t, fake, func(record *Record) {
		record.Resolved.Harness = "not-a-supported-harness"
	}, nil)
	if !result.HarnessRan {
		t.Fatalf("the harness did start, so a terminal result must say so: %#v", result)
	}
	if result.ExecutionStatus != StatusFailed || result.Category != FailureConfig ||
		!strings.Contains(result.Failure, "resolve the contained harness adapter") {
		t.Fatalf("result = %s/%s (%q), want the adapter refusal", result.ExecutionStatus, result.Category, result.Failure)
	}
}

func TestE2EExecuteContainedClassifiesHarnessTerminalOutcomes(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		harness  string
		fake     *fakeContainedRuntime
		want     string
		status   ExecutionStatus
		category string
		exitCode bool
	}{
		"non-zero harness exit": {
			harness: HarnessDSH,
			fake:    &fakeContainedRuntime{harnessStdout: "final message\n", harnessExit: 3},
			want:    "harness exited with exit status 3", status: StatusFailed, category: FailureHarness, exitCode: true,
		},
		"zero exit but a failed turn": {
			harness: HarnessCodex,
			fake:    &fakeContainedRuntime{harnessStdout: `{"type":"turn.failed"}` + "\n"},
			want:    "harness exited zero but reported a failed turn", status: StatusFailed, category: FailureHarness, exitCode: true,
		},
		"unparsable stream without a terminal turn": {
			harness: HarnessCodex,
			fake:    &fakeContainedRuntime{harnessStdout: "{bad\n"},
			want:    "unparsable event line", status: StatusFailed, category: FailureHarness, exitCode: true,
		},
		"empty stream without a terminal turn": {
			harness: HarnessDSH,
			fake:    &fakeContainedRuntime{harnessStdout: ""},
			want:    "no terminal turn event", status: StatusFailed, category: FailureHarness, exitCode: true,
		},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, _, _ := executeContainedFixture(t, testCase.fake, func(record *Record) {
				record.Resolved.Harness = testCase.harness
			}, nil)
			if result.ExecutionStatus != testCase.status || result.Category != testCase.category {
				t.Fatalf("result = %s/%s (%q), want %s/%s", result.ExecutionStatus, result.Category, result.Failure, testCase.status, testCase.category)
			}
			if !strings.Contains(result.Failure, testCase.want) {
				t.Fatalf("failure = %q, want %q", result.Failure, testCase.want)
			}
			if testCase.exitCode && result.ExitCode == nil {
				t.Fatalf("result = %#v, want a recorded exit code", result)
			}
			if !result.HarnessRan {
				t.Fatalf("a terminal harness outcome must report that the harness ran: %#v", result)
			}
		})
	}
}

func TestE2EExecuteContainedRedactsTheCredentialInTheFinalMessage(t *testing.T) {
	t.Parallel()
	fake := &fakeContainedRuntime{harnessStdout: "the final message mentions " + containedOwnerSecret + "\n"}
	result, record, store := executeContainedFixture(t, fake, nil, nil)
	if !result.HarnessRan || result.ExecutionStatus != StatusCompleted {
		t.Fatalf("result = %s/%s (%q)", result.ExecutionStatus, result.Category, result.Failure)
	}
	if strings.Contains(record.Result, containedOwnerSecret) {
		t.Fatalf("the recorded result kept the credential: %q", record.Result)
	}
	raw, err := os.ReadFile(store.LogPath(record.AgentID))
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}
	if strings.Contains(string(raw), containedOwnerSecret) {
		t.Fatalf("the bounded run log kept the credential: %q", raw)
	}
	if !strings.Contains(string(raw), redactionMarker) {
		t.Fatalf("run log = %q, want the redaction marker", raw)
	}
}

func TestE2EExecuteContainedRefusesABoundItCannotApply(t *testing.T) {
	t.Parallel()
	result, _, _ := executeContainedFixture(t, &fakeContainedRuntime{}, func(record *Record) {
		record.TimeoutMS = 0
	}, nil)
	if result.ExecutionStatus != StatusFailed || !strings.Contains(result.Failure, "bound the contained harness") {
		t.Fatalf("result = %s (%q), want the harness bound refusal", result.ExecutionStatus, result.Failure)
	}
}

func TestE2EExecuteContainedPropagatesEachIntermediateSaveFailure(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("record store is full")
	cases := map[string]struct {
		fake   *fakeContainedRuntime
		mutate func(*Record)
		save   func(Record) error
		want   string
	}{
		"after the composed configuration was proved": {
			fake: &fakeContainedRuntime{},
			save: func(candidate Record) error {
				if candidate.HarnessVersion != "" && candidate.WorkerPID == 0 {
					return storeErr
				}
				return nil
			},
			want: "record store is full",
		},
		"after the harness was started": {
			fake: &fakeContainedRuntime{harnessStdout: "final message\n"},
			save: func(candidate Record) error {
				if candidate.WorkerPID != 0 {
					return storeErr
				}
				return nil
			},
			want: "record store is full",
		},
		"when validation starts": {
			fake: &fakeContainedRuntime{harnessStdout: "final message\n"},
			save: func(candidate Record) error {
				if candidate.Validation.Status == ValidationRunning {
					return storeErr
				}
				return nil
			},
			want: "record store is full",
		},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, _, _ := executeContainedFixture(t, testCase.fake, testCase.mutate, testCase.save)
			if result.saveErr == nil || !strings.Contains(result.saveErr.Error(), testCase.want) {
				t.Fatalf("saveErr = %v, want %q", result.saveErr, testCase.want)
			}
			if result.HarnessRan && name == "after the composed configuration was proved" {
				t.Fatalf("a refused pre-harness save claimed a run: %#v", result)
			}
		})
	}
}

func TestE2ERunContainedOwnerRefusesIncompleteAdmissionBeforeAnyRuntimeOrCredential(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	cases := map[string]struct {
		configured *ExecutionPolicy
		mutate     func(*Record)
		want       string
	}{
		"no authoritative policy": {
			configured: nil,
			want:       "requires a matching mandatory agents.execution policy",
		},
		"record carries no policy snapshot": {
			configured: &policy,
			mutate: func(record *Record) {
				record.Execution = nil
			},
			want: "requires a matching mandatory agents.execution policy",
		},
		"no trusted workspace": {
			configured: &policy,
			mutate: func(record *Record) {
				record.ExecutionWorkspace = nil
			},
			want: "requires the trusted workspace snapshot",
		},
		"harness that cannot run in the sandbox": {
			configured: &policy,
			mutate: func(record *Record) {
				record.Resolved.Harness = HarnessCodex
			},
			want: "cannot run the",
		},
		"lifetime this build refuses": {
			configured: &policy,
			mutate: func(record *Record) {
				record.TimeoutMS = 0
			},
			want: "positive run timeout",
		},
		"credential that cannot be resolved": {
			configured: &policy,
			mutate: func(record *Record) {
				record.Resolved.Routing = Provider{
					BaseURL:        "https://api.deepseek.com",
					CredentialFile: "/nonexistent/wb-test-provider.key",
					WireAPI:        WireAPIChat,
				}
			},
			want: "provider credential",
		},
		"launch that cannot be prepared": {
			configured: &policy,
			mutate: func(record *Record) {
				record.ExecutionWorkspace.WorktreeDir = record.WorktreeDir + "-other"
			},
			want: "does not match the record worktree",
		},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store, record := containedExecuteFixture(t)
			containedOwnerUseCredentialFile(t, &record)
			if testCase.mutate != nil {
				testCase.mutate(&record)
			}
			opened := false
			deps := DefaultOwnerDeps()
			deps.openContainedRuntime = func(context.Context, ContainerRuntimeOptions) (containedRuntime, error) {
				opened = true
				return nil, errors.New("the runtime must not be opened")
			}
			var saved Record
			save := func(candidate Record) error { saved = candidate; return nil }
			err := runContainedOwner(context.Background(), store, record.AgentID, record, deps, save, testCase.configured)
			if err != nil {
				t.Fatalf("runContainedOwner: %v", err)
			}
			if opened {
				t.Fatal("a refused admission opened the runtime")
			}
			if saved.ExecutionStatus != StatusFailed || !strings.Contains(saved.Failure, testCase.want) {
				t.Fatalf("saved record = %s (%q), want %q", saved.ExecutionStatus, saved.Failure, testCase.want)
			}
			if name == "lifetime this build refuses" && saved.Validation.Reason != "the contained harness timeout was refused" {
				t.Fatalf("validation reason = %q, want the harness-bound guard to fire before the whole-lifetime guard", saved.Validation.Reason)
			}
		})
	}
}

func TestE2ERunContainedOwnerJoinsASaveFailureWithACleanupFailure(t *testing.T) {
	t.Parallel()
	store, record := containedExecuteFixture(t)
	containedOwnerUseCredentialFile(t, &record)
	policy := executionTestPolicy()
	closeErr := errors.New("the container is still present")
	fake := &fakeContainedRuntime{
		harnessStdout: "final message\n",
		dumpOutput:    stringPointer(containedOwnerDumpFor(ProviderCredentialEnv)),
		closeErr:      closeErr,
	}
	deps := DefaultOwnerDeps()
	deps.openContainedRuntime = func(context.Context, ContainerRuntimeOptions) (containedRuntime, error) {
		return fake, nil
	}
	saveErr := errors.New("record store is full")
	save := func(candidate Record) error {
		if candidate.HarnessVersion != "" && candidate.WorkerPID == 0 {
			return saveErr
		}
		return nil
	}
	err := runContainedOwner(context.Background(), store, record.AgentID, record, deps, save, &policy)
	// The cleanup failure must not overwrite the earlier persistent-save
	// failure: both original errors have to remain identifiable.
	if !errors.Is(err, saveErr) || !errors.Is(err, closeErr) {
		t.Fatalf("error = %v, want both the intermediate save failure and the cleanup failure", err)
	}
	if !strings.Contains(err.Error(), closeErr.Error()) {
		t.Fatalf("error = %v, want the cleanup failure text", err)
	}
	if !fake.closedNow() {
		t.Fatal("a failed contained record save must still close the runtime")
	}
}

func TestE2ERunContainedOwnerJoinsAFinalRecordSaveFailureWithACleanupFailure(t *testing.T) {
	t.Parallel()
	store, record := containedExecuteFixture(t)
	containedOwnerUseCredentialFile(t, &record)
	policy := executionTestPolicy()
	closeErr := errors.New("the container is still present")
	fake := &fakeContainedRuntime{
		harnessStdout: "final message\n",
		dumpOutput:    stringPointer(containedOwnerDumpFor(ProviderCredentialEnv)),
		closeErr:      closeErr,
	}
	deps := DefaultOwnerDeps()
	deps.openContainedRuntime = func(context.Context, ContainerRuntimeOptions) (containedRuntime, error) {
		return fake, nil
	}
	terminalSaveErr := errors.New("the terminal record cannot be persisted")
	var intermediateSaves, terminalSaves int
	var terminalRecord Record
	save := func(candidate Record) error {
		// Only the terminal record write fails. Every intermediate save must
		// succeed, so the returned error can only come from the final save.
		if candidate.FinishedAt.IsZero() || !candidate.ExecutionStatus.Terminal() {
			intermediateSaves++
			return nil
		}
		terminalSaves++
		terminalRecord = candidate
		return terminalSaveErr
	}
	err := runContainedOwner(context.Background(), store, record.AgentID, record, deps, save, &policy)
	if terminalSaves != 1 {
		t.Fatalf("terminal save attempts = %d, want exactly the one final terminal record save", terminalSaves)
	}
	if intermediateSaves == 0 {
		t.Fatal("no intermediate save succeeded, so the only-final-save path was not isolated")
	}
	if terminalRecord.ExecutionStatus != StatusFailed || terminalRecord.FinishedAt.IsZero() {
		t.Fatalf("terminal record = %s finished %v, want the failed terminal record whose save failed",
			terminalRecord.ExecutionStatus, terminalRecord.FinishedAt)
	}
	// The terminal save failure must not replace the cleanup failure: both
	// original causes have to remain identifiable to the caller.
	if !errors.Is(err, terminalSaveErr) || !errors.Is(err, closeErr) {
		t.Fatalf("error = %v, want both the terminal save failure and the cleanup failure", err)
	}
	if !fake.closedNow() {
		t.Fatal("a failed terminal record save must still close the runtime")
	}
}

func stringPointer(value string) *string { return &value }

func TestE2ERunContainedDshVersionBoundsTheFirstReportedLine(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		output string
		want   string
		err    string
	}{
		"first line of a multi-line report": {output: "dsh 9.9.9\r\nnoise\n", want: "dsh 9.9.9"},
		"surrounding whitespace is trimmed": {output: "  dsh 9.9.9  \n", want: "dsh 9.9.9"},
		"empty report":                      {output: "\n\n", err: "reported no version"},
		"over-long version is bounded": {
			output: strings.Repeat("v", 200), want: strings.Repeat("v", 120),
		},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeContainedRuntime{workerCommandFn: func(context.Context, Credential, []string) (*exec.Cmd, error) {
				return containedOwnerShell(context.Background(), testCase.output, "", 0), nil
			}}
			version, err := runContainedDshVersion(context.Background(), fake)
			if testCase.err != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.err) {
					t.Fatalf("error = %v, want %q", err, testCase.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("runContainedDshVersion: %v", err)
			}
			if version != testCase.want {
				t.Fatalf("version = %q, want %q", version, testCase.want)
			}
		})
	}
}

func TestE2ERunContainedBoundedCommandRefusesOversizedAndFailedProbes(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		fake *fakeContainedRuntime
		want string
	}{
		"over-bound output from a failed command": {
			fake: &fakeContainedRuntime{workerCommandFn: func(ctx context.Context, _ Credential, _ []string) (*exec.Cmd, error) {
				return containedOwnerShell(ctx, strings.Repeat("x", 64), "", 2), nil
			}},
			want: "output is above its bound",
		},
		"failed command with diagnostic stderr": {
			fake: &fakeContainedRuntime{workerCommandFn: func(ctx context.Context, _ Credential, _ []string) (*exec.Cmd, error) {
				return containedOwnerShell(ctx, "", "dsh configuration is invalid", 1), nil
			}},
			want: "dsh configuration is invalid",
		},
		"failed command without diagnostic output": {
			fake: &fakeContainedRuntime{workerCommandFn: func(ctx context.Context, _ Credential, _ []string) (*exec.Cmd, error) {
				return containedOwnerShell(ctx, "", "", 1), nil
			}},
			want: "failed",
		},
		"successful command whose output is over the bound": {
			fake: &fakeContainedRuntime{workerCommandFn: func(ctx context.Context, _ Credential, _ []string) (*exec.Cmd, error) {
				return containedOwnerShell(ctx, strings.Repeat("x", 64), "", 0), nil
			}},
			want: "output is above its bound",
		},
		"command construction failure": {
			fake: &fakeContainedRuntime{workerCommandFn: func(context.Context, Credential, []string) (*exec.Cmd, error) {
				return nil, errors.New("transport cannot build the probe")
			}},
			want: "transport cannot build the probe",
		},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := runContainedBoundedCommand(context.Background(), testCase.fake, []string{"dsh", "--version"}, 16)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestE2EContainedStopCommandStopsAStartedProcessWithoutACancelHook(t *testing.T) {
	if containedOwnerIsWindows() {
		t.Skip("the stop fixture uses a POSIX sleeping process")
	}
	t.Parallel()
	command := exec.Command("sleep", "600")
	if err := command.Start(); err != nil {
		t.Fatalf("start sleeping fixture: %v", err)
	}
	stopCommand(command)
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("a stopped worker process did not reap")
	}
	if command.ProcessState == nil || command.ProcessState.Success() {
		t.Fatalf("process state = %#v, want a terminated process", command.ProcessState)
	}
	// A nil command is the no-op guard; it must never panic.
	stopCommand(nil)
}

func containedOwnerIsWindows() bool {
	return filepath.Separator == '\\'
}

func TestE2EMonitorCancellationDefaultsAndStopsItsWatcher(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	ctx, stop := monitorCancellation(context.Background(), store, "agt-default-poll", 0)
	stop()
	if ctx.Err() == nil {
		t.Fatal("the returned stop function must cancel the monitored context")
	}
}

func TestE2EExecuteContainedStopsWhenTheSentinelAppearsAfterTheConfigurationSave(t *testing.T) {
	t.Parallel()
	fake := &fakeContainedRuntime{harnessStdout: "final message\n"}
	store, record := containedExecuteFixture(t)
	save := func(candidate Record) error {
		if candidate.HarnessVersion != "" && candidate.WorkerPID == 0 {
			return requestCancel(store, record.AgentID, time.Now)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	launch := ContainedDshLaunch{Launch: HarnessLaunch{Argv: []string{"--profile", dshProfile, record.Task}}}
	result := executeContained(ctx, ctx, store, record.AgentID, &record, DefaultOwnerDeps(), save, fake,
		Credential{EnvName: containedOwnerCredentialEnv, Value: containedOwnerSecret}, launch, containedDshDefaultBaseURL)
	if result.ExecutionStatus != StatusCancelled || result.Category != FailureCancelled {
		t.Fatalf("result = %s/%s (%q), want the post-save cancellation", result.ExecutionStatus, result.Category, result.Failure)
	}
	for _, argv := range fake.commands {
		if containedHarnessArgv(argv) {
			t.Fatalf("a run cancelled before the harness command started it: %#v", fake.commands)
		}
	}
}

func TestE2EExecuteContainedReportsAFailedHarnessCommandConstruction(t *testing.T) {
	t.Parallel()
	fake := &fakeContainedRuntime{workerCommandFn: func(_ context.Context, _ Credential, argv []string) (*exec.Cmd, error) {
		if containedHarnessArgv(argv) {
			return nil, errors.New("transport cannot build the harness command")
		}
		if containsContainedArg(argv, "--dump-config") {
			return containedOwnerShell(context.Background(), containedOwnerDump(), "", 0), nil
		}
		return containedOwnerShell(context.Background(), containedOwnerVersion+"\n", "", 0), nil
	}}
	result, _, _ := executeContainedFixture(t, fake, nil, nil)
	if result.ExecutionStatus != StatusFailed || !strings.Contains(result.Failure, "build the contained harness command") {
		t.Fatalf("result = %s (%q), want the harness command construction refusal", result.ExecutionStatus, result.Failure)
	}
}

func TestE2EContainedDshEffectiveBaseURLFallsBackOnlyWhenNoBaseWasResolved(t *testing.T) {
	t.Parallel()
	if got := containedDshEffectiveBaseURL("  "); got != containedDshDefaultBaseURL {
		t.Fatalf("blank base URL = %q, want the official default %q", got, containedDshDefaultBaseURL)
	}
	if got := containedDshEffectiveBaseURL("https://api.deepseek.com/v1"); got != "https://api.deepseek.com/v1" {
		t.Fatalf("explicit base URL = %q, want it preserved", got)
	}
}

func TestE2EVerifyContainedDshDumpRefusesMissingOrMalformedEntries(t *testing.T) {
	t.Parallel()
	valid := containedOwnerDump()
	cases := map[string]struct {
		raw  string
		want string
	}{
		"not yaml":                    {raw: "provider: [\n", want: "parse the composed dsh configuration"},
		"no model entry":              {raw: "- id: other\n  config: {}\n", want: "no agent-default-model entry"},
		"wrong provider":              {raw: strings.Replace(valid, "provider: "+ProviderDshDefault, "provider: other", 1), want: "default provider"},
		"wrong api key variable":      {raw: strings.Replace(valid, "apiKeyEnv: "+containedOwnerCredentialEnv, "apiKeyEnv: OTHER_KEY", 1), want: "apiKeyEnv"},
		"wrong routing base":          {raw: strings.Replace(valid, "baseURL: https://api.deepseek.com", "baseURL: https://example.com", 1), want: "baseURL"},
		"over the document bound":     {raw: strings.Repeat("x", maxContainerDshDumpBytes+1), want: "above the"},
		"scalar document, no entries": {raw: "just-a-scalar\n", want: "no agent-default-model entry"},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := verifyContainedDshDump([]byte(testCase.raw), "deepseek-flash", containedOwnerCredentialEnv, "https://api.deepseek.com")
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestE2EFinalizeContainedResultPreservesTheSaveErrorAndTheRecordedValidation(t *testing.T) {
	t.Parallel()
	saveErr := errors.New("record store is full")
	closeErr := errors.New("the container is still present")
	t.Run("cleanup failure with no recorded validation", func(t *testing.T) {
		t.Parallel()
		result := finalizeContainedResult(containedResult{saveErr: saveErr}, closeErr)
		if result.State != StateFailed || result.ExecutionStatus != StatusFailed || result.Category != FailureInternal {
			t.Fatalf("result = %s/%s/%s (%q), want the cleanup failure", result.State, result.ExecutionStatus, result.Category, result.Failure)
		}
		if !errors.Is(result.saveErr, saveErr) {
			t.Fatalf("saveErr = %v, want the earlier persistent-save failure preserved", result.saveErr)
		}
		if !strings.Contains(result.Failure, "clean up the contained execution") {
			t.Fatalf("failure = %q, want the cleanup stage named", result.Failure)
		}
		if result.Validation.Status != ValidationSkipped || !strings.Contains(result.Validation.Reason, "proved absent") {
			t.Fatalf("validation = %#v, want the truthful absence-unknown record", result.Validation)
		}
	})
	t.Run("cleanup failure keeps a performed check", func(t *testing.T) {
		t.Parallel()
		performed := ValidationRecord{Status: ValidationPassed}
		result := finalizeContainedResult(containedResult{Validation: performed, saveErr: saveErr}, closeErr)
		if result.Validation.Status != performed.Status || result.Validation.Reason != performed.Reason {
			t.Fatalf("validation = %#v, want the performed check unchanged", result.Validation)
		}
		if !errors.Is(result.saveErr, saveErr) {
			t.Fatalf("saveErr = %v, want the earlier persistent-save failure preserved", result.saveErr)
		}
	})
	t.Run("clean run with no recorded validation", func(t *testing.T) {
		t.Parallel()
		result := finalizeContainedResult(containedResult{HarnessRan: true, saveErr: saveErr}, nil)
		if result.Validation.Status != ValidationSkipped || result.Validation.Reason != "validation did not run" {
			t.Fatalf("validation = %#v, want the last-resort skipped record", result.Validation)
		}
		if !result.HarnessRan || !errors.Is(result.saveErr, saveErr) {
			t.Fatalf("result = %#v, want the harness fact and the save error preserved", result)
		}
	})
	t.Run("clean run keeps a failed check", func(t *testing.T) {
		t.Parallel()
		failed := ValidationRecord{Status: ValidationFailed, Reason: "the objective did not pass"}
		result := finalizeContainedResult(containedResult{Validation: failed}, nil)
		if result.Validation.Status != failed.Status || result.Validation.Reason != failed.Reason {
			t.Fatalf("validation = %#v, want the failed check unchanged", result.Validation)
		}
	})
}

func TestE2EContainedRuntimeOpenerDefaultsToTheProductionOpener(t *testing.T) {
	t.Parallel()
	opener := containedRuntimeOpenerFor(nil)
	if opener == nil {
		t.Fatal("the absent opener seam selected no opener")
	}
	// The production opener still refuses an invalid policy before it resolves
	// Docker or opens any state, so this proves the default without a daemon.
	if _, err := opener(context.Background(), ContainerRuntimeOptions{}); err == nil {
		t.Fatal("the production opener accepted an invalid policy")
	}
	injectedErr := errors.New("the injected opener was selected")
	injected := func(context.Context, ContainerRuntimeOptions) (containedRuntime, error) {
		return nil, injectedErr
	}
	if _, err := containedRuntimeOpenerFor(injected)(context.Background(), ContainerRuntimeOptions{}); !errors.Is(err, injectedErr) {
		t.Fatalf("error = %v, want the injected opener's refusal", err)
	}
}

func TestE2ECollectContainedDshEntriesIgnoresANilNode(t *testing.T) {
	t.Parallel()
	entries := map[string]map[string]*yaml.Node{"kept": {"value": {Kind: yaml.ScalarNode, Tag: "!!str", Value: "kept"}}}
	collectContainedDshEntries(nil, entries)
	if len(entries) != 1 || entries["kept"] == nil {
		t.Fatalf("entries = %#v, want the nil node ignored and the existing entry kept", entries)
	}
}

func TestE2EExecuteContainedBoundsAndReportsBothCapturedStreams(t *testing.T) {
	t.Parallel()
	const limit = int64(8)
	fake := &fakeContainedRuntime{
		harnessStdout: strings.Repeat("o", 64),
		harnessStderr: strings.Repeat("e", 64),
	}
	store, record := containedExecuteFixture(t)
	deps := DefaultOwnerDeps()
	deps.containedLogLimit = limit
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	launch := ContainedDshLaunch{Launch: HarnessLaunch{Argv: []string{"--profile", dshProfile, record.Task}}}
	result := executeContained(ctx, ctx, store, record.AgentID, &record, deps, func(Record) error { return nil }, fake,
		Credential{EnvName: containedOwnerCredentialEnv, Value: containedOwnerSecret}, launch, containedDshDefaultBaseURL)
	if result.ExecutionStatus != StatusCompleted || !result.HarnessRan {
		t.Fatalf("result = %s/%s (%q), want a completed harness that still reports its status", result.ExecutionStatus, result.Category, result.Failure)
	}
	events := strings.Join(record.HarnessEvents, "\n")
	for _, want := range []string{
		fmt.Sprintf("run log truncated at %d bytes", limit),
		fmt.Sprintf("run stderr log truncated at %d bytes", limit),
	} {
		if !strings.Contains(events, want) {
			t.Fatalf("events = %q, want %q", events, want)
		}
	}
	for name, path := range map[string]string{
		"stdout": store.LogPath(record.AgentID),
		"stderr": filepath.Join(store.Dir(record.AgentID), stderrFileName),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s log: %v", name, err)
		}
		if int64(len(raw)) > limit {
			t.Fatalf("%s log grew to %d bytes, above the %d-byte bound", name, len(raw), limit)
		}
	}
}

func TestE2EExecuteContainedRedactsACredentialFromTheLastMessageFallback(t *testing.T) {
	t.Parallel()
	fake := &fakeContainedRuntime{harnessStdout: `{"type":"turn.completed"}` + "\n"}
	store, record := containedExecuteFixture(t)
	record.Resolved.Harness = HarnessCodex
	lastMessagePath := store.LastMessagePath(record.AgentID)
	if err := os.WriteFile(lastMessagePath, []byte("the final message mentions "+containedOwnerSecret+"\n"), 0o600); err != nil {
		t.Fatalf("write last message: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	launch := ContainedDshLaunch{Launch: HarnessLaunch{Argv: []string{"--profile", dshProfile, record.Task}}}
	result := executeContained(ctx, ctx, store, record.AgentID, &record, DefaultOwnerDeps(), func(Record) error { return nil }, fake,
		Credential{EnvName: containedOwnerCredentialEnv, Value: containedOwnerSecret}, launch, containedDshDefaultBaseURL)
	if result.ExecutionStatus != StatusCompleted {
		t.Fatalf("result = %s/%s (%q), want a completed run", result.ExecutionStatus, result.Category, result.Failure)
	}
	if strings.Contains(record.Result, containedOwnerSecret) {
		t.Fatalf("the final reported result kept the credential: %q", record.Result)
	}
	if !strings.Contains(record.Result, redactionMarker) {
		t.Fatalf("result = %q, want the redaction marker", record.Result)
	}
	events := strings.Join(record.HarnessEvents, "\n")
	if !strings.Contains(events, "final message contained the provider credential") {
		t.Fatalf("events = %q, want the redaction event", events)
	}
}
