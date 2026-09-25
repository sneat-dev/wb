package agents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/process"
	"github.com/sneat-dev/wb/internal/runner"
)

// realRunner returns the production [runner.Runner]. It is a function, not
// a package-level var, so this package carries no mutable global seam: each
// call constructs a fresh, stateless [runner.Real], and a test reaches the
// runner-backed helpers below through their own unexported seam variant
// instead, passing a [runnertest.Fake].
func realRunner() runner.Runner { return runner.New() }

// OwnerArgument selects the private, detached run-owner command. It is handled
// before normal command dispatch, following WB's existing self-exec convention
// for internal child processes: the dispatching process re-invokes its own
// executable with this argument rather than depending on a daemon.
const OwnerArgument = "--wb-internal-agent-run"

// maxLogBytes bounds the captured harness stream so a long, chatty worker
// cannot fill the disk. Beyond the bound the log is truncated and the run
// records that it was, rather than the writes failing under the harness.
const maxLogBytes = 128 << 20

// defaultStopGrace is how long a stopped worker has to honour a graceful
// signal before the stop escalates to a kill.
const defaultStopGrace = 3 * time.Second

// defaultStopPollInterval is how often StopRun re-checks whether the worker
// has exited during the grace period.
const defaultStopPollInterval = 50 * time.Millisecond

// OwnerDeps are the seams the run owner needs. They are injected so the whole
// owner can be exercised deterministically against a fake harness.
type OwnerDeps struct {
	// LookPath resolves the harness executable. Resolving it by name through
	// PATH is what lets a test substitute a fake harness without a production
	// override flag.
	LookPath func(string) (string, error)
	// Now is the clock, injected for deterministic tests.
	Now func() time.Time
	// Sleep is StopRun's grace-period poll seam, injected for deterministic
	// tests. A test exercising a worker that ignores termination shrinks
	// StopGrace/StopPollInterval instead of leaving Sleep real, because that
	// path genuinely waits on a real OS process; every other test replaces
	// Sleep with a recorder.
	Sleep func(time.Duration)
	// StopGrace and StopPollInterval configure StopRun's escalation wait.
	// Both are struct fields, not package-level mutable vars, so a test
	// cannot leave shared package state mutated for another test running in
	// parallel. StopRun does not default a zero OwnerDeps -- every caller
	// (production and test) builds on DefaultOwnerDeps, which already
	// populates every field, so StopRun trusts deps as given.
	StopGrace        time.Duration
	StopPollInterval time.Duration
}

// DefaultOwnerDeps returns the production seams.
func DefaultOwnerDeps() OwnerDeps {
	return OwnerDeps{
		LookPath: exec.LookPath, Now: time.Now, Sleep: time.Sleep,
		StopGrace: defaultStopGrace, StopPollInterval: defaultStopPollInterval,
	}
}

// RunOwner executes one dispatched run to completion and records its terminal
// state. It is the process that outlives the dispatching CLI, so everything a
// later `status` or `await` can learn about the run is written here.
func RunOwner(ctx context.Context, store Store, agentID string, deps OwnerDeps) error {
	if deps.LookPath == nil {
		deps.LookPath = exec.LookPath
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	record, err := store.Load(agentID)
	if err != nil {
		return err
	}
	// The bound is enforced here, in the process that outlives the dispatcher,
	// using the same process-group cancellation internal/process already owns.
	// Leaving it to the harness would make it advisory.
	if record.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(record.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	record.OwnerPID = os.Getpid()
	if err := store.Save(record); err != nil {
		return err
	}

	finish := func(state State, failure string) error {
		record.State = state
		record.Failure = failure
		record.FinishedAt = deps.Now().UTC()
		if !record.StartedAt.IsZero() {
			record.DurationMS = record.FinishedAt.Sub(record.StartedAt).Milliseconds()
		}
		return store.Save(record)
	}

	credential, err := ResolveCredential(record.Resolved.Routing)
	if err != nil {
		return finish(StateFailed, err.Error())
	}
	executable, err := deps.LookPath(record.Resolved.Harness)
	if err != nil {
		return finish(StateFailed, fmt.Sprintf("harness executable %q not found on PATH: %v", record.Resolved.Harness, err))
	}
	harnessHome := store.HarnessHomePath(agentID)
	if err := os.MkdirAll(harnessHome, 0o700); err != nil {
		return finish(StateFailed, fmt.Sprintf("create private harness home: %v", err))
	}
	// The harness is told the resolved variable *name*, whichever source the
	// credential came from, so file-sourced credentials use the same
	// per-process mechanism as environment-sourced ones.
	routing := record.Resolved.Routing
	routing.CredentialEnv = credential.EnvName
	routing.CredentialFile = ""
	argv, err := CodexArgv(HarnessOptions{
		WorktreeDir:     record.WorktreeDir,
		Model:           record.Resolved.Model,
		Reasoning:       record.Resolved.Reasoning,
		ProviderName:    record.Resolved.Provider,
		Provider:        routing,
		LastMessagePath: store.LastMessagePath(agentID),
	})
	if err != nil {
		return finish(StateFailed, err.Error())
	}

	logFile, err := os.OpenFile(store.LogPath(agentID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return finish(StateFailed, fmt.Sprintf("open run log: %v", err))
	}
	defer func() { _ = logFile.Close() }()
	log := &boundedLog{writer: logFile, remaining: maxLogBytes}

	// The harness reads the task from stdin, so task text never appears in the
	// process table, and the command line stays free of it in every log.
	command := process.CommandContext(ctx, executable, argv...)
	command.Dir = record.WorktreeDir
	command.Env = append(WorkerEnvironment(credential), "CODEX_HOME="+harnessHome)
	command.Stdin = strings.NewReader(record.Task)
	command.Stdout = log
	command.Stderr = log
	if err := command.Start(); err != nil {
		return finish(StateFailed, fmt.Sprintf("start %s: %v", record.Resolved.Harness, err))
	}
	record.WorkerPID = command.Process.Pid
	if err := store.Save(record); err != nil {
		return err
	}

	waitErr := command.Wait()
	summary := summarizeLog(store.LogPath(agentID))
	record.Usage = summary.Usage
	record.ToolCalls = summary.ToolCalls
	record.HarnessEvents = summary.Diagnostics
	if log.truncated() {
		record.HarnessEvents = append(record.HarnessEvents,
			fmt.Sprintf("run log truncated at %d bytes", maxLogBytes))
	}
	if message, readErr := os.ReadFile(store.LastMessagePath(agentID)); readErr == nil {
		record.Result = BoundResult(string(message))
		// The harness writes this file itself, so WB tightens its mode after
		// reading it rather than assuming the harness's umask.
		_ = os.Chmod(store.LastMessagePath(agentID), 0o600)
	}
	record.Changes = SummarizeChanges(context.WithoutCancel(ctx), record.WorktreeDir, record.BaseSHA)

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		// The context owns the whole process group, so expiry has already
		// terminated the worker tree; the distinct state is what tells a
		// supervisor this was a bound, not a failure of the work.
		return finish(StateTimeout, "run exceeded its timeout and its process group was terminated")
	case waitErr != nil:
		code := exitCodeOf(command)
		record.ExitCode = code
		failure := fmt.Sprintf("harness exited with %v", waitErr)
		if summary.TurnFailed {
			failure = "harness reported a failed turn"
		}
		return finish(StateFailed, failure)
	case summary.TurnFailed:
		code := 0
		record.ExitCode = &code
		return finish(StateFailed, "harness exited zero but reported a failed turn")
	case summary.MalformedLines > 0:
		code := exitCodeOf(command)
		record.ExitCode = code
		return finish(StateFailed, fmt.Sprintf("harness produced no terminal turn event and %d unparsable event line(s); the run is not a success", summary.MalformedLines))
	case !summary.TurnCompleted:
		code := exitCodeOf(command)
		record.ExitCode = code
		return finish(StateFailed, "harness produced no terminal turn event; the run is not a success")
	default:
		code := exitCodeOf(command)
		record.ExitCode = code
		return finish(StateCompleted, "")
	}
}

func exitCodeOf(command *exec.Cmd) *int {
	if command.ProcessState == nil {
		return nil
	}
	code := command.ProcessState.ExitCode()
	return &code
}

// summarizeLog reads the harness event stream back. A log that cannot be read
// yields an empty summary rather than failing the run: the exit status and the
// log path are still true and still useful.
func summarizeLog(path string) HarnessSummary {
	file, err := os.Open(path)
	if err != nil {
		return HarnessSummary{}
	}
	defer func() { _ = file.Close() }()
	return SummarizeEvents(io.LimitReader(file, maxLogBytes))
}

// boundedLog caps the captured harness stream and records that it did.
type boundedLog struct {
	mutex     sync.Mutex
	writer    io.Writer
	remaining int64
	cut       bool
}

func (log *boundedLog) Write(data []byte) (int, error) {
	log.mutex.Lock()
	defer log.mutex.Unlock()
	length := len(data)
	if log.remaining <= 0 {
		log.cut = true
		// Report the full length so the harness sees a successful write; the
		// alternative is the harness failing on a run whose only problem is
		// that it was talkative.
		return length, nil
	}
	if int64(length) > log.remaining {
		data = data[:log.remaining]
		log.cut = true
	}
	written, err := log.writer.Write(data)
	log.remaining -= int64(written)
	return length, err
}

func (log *boundedLog) truncated() bool {
	log.mutex.Lock()
	defer log.mutex.Unlock()
	return log.cut
}

// OwnerCLI runs the private owner command line:
//
//	wb --wb-internal-agent-run --run-dir <absolute run directory>
//
// It is called from main before cobra parses anything, so the owner can never
// be affected by, or accidentally honour, a user-facing flag contract.
func OwnerCLI(arguments []string, deps OwnerDeps) int {
	var runDir string
	for index := 0; index < len(arguments); index++ {
		if arguments[index] == "--run-dir" && index+1 < len(arguments) {
			runDir = arguments[index+1]
			index++
		}
	}
	if strings.TrimSpace(runDir) == "" {
		_, _ = fmt.Fprintln(os.Stderr, "wb agent owner: --run-dir is required")
		return 2
	}
	clean := filepath.Clean(runDir)
	agentID := filepath.Base(clean)
	store := Store{Root: filepath.Dir(clean)}
	if err := RunOwner(context.Background(), store, agentID, deps); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "wb agent owner:", err)
		return 1
	}
	return 0
}

// SpawnOwner starts the detached run owner for a persisted run and returns
// without waiting for it. The owner is this same WB executable re-invoked with
// the private owner argument, so a dispatched run needs no daemon and survives
// the dispatching CLI exiting.
func SpawnOwner(runDir string, executable func() (string, error)) (int, error) {
	return spawnOwner(realRunner(), runDir, executable)
}

// spawnOwner is [SpawnOwner]'s test seam: every production call site reaches
// it only through SpawnOwner, which always passes [realRunner], so
// production behaviour is unchanged. A test passes a [runnertest.Fake] to
// reach both failure branches deterministically.
//
// It reaches r.Detach with the exact argv, working directory ("" -- inherit
// the caller's, matching the original exec.Command(path, ...) call, which
// never set a Dir either) and environment (nil -- inherit the caller's
// ambient one, the same net effect as the original's explicit
// command.Env = os.Environ()) the hand-rolled exec.Command/
// process.ConfigureDetached/Start/Release sequence this replaces used.
// [runner.Real.Detach] itself already does that same sequence -- including
// redirecting stdio to the null device, which a nil Stdin/Stdout/Stderr
// does the same way exec.Command's did explicitly -- with one disclosed
// simplification: a failure to Release the process handle after Start is
// swallowed rather than returned, matching every other Detach call site in
// this codebase (daemon launch, browser.go, lifecycle hooks) rather than
// carrying owner.go's own one-off handling of that essentially unreachable
// case.
func spawnOwner(r runner.Runner, runDir string, executable func() (string, error)) (int, error) {
	path, err := executable()
	if err != nil {
		return 0, fmt.Errorf("locate the wb executable for the run owner: %w", err)
	}
	pid, err := r.Detach("", path, OwnerArgument, "--run-dir", runDir) //nolint:gosec // current wb executable and fixed arguments
	if err != nil {
		return 0, err
	}
	return pid, nil
}

// StopRun terminates a running worker's process group. The owner is left alive
// deliberately: it observes the non-zero exit and records the terminal state,
// so a stopped run reports a real outcome instead of vanishing into
// "abandoned".
func StopRun(store Store, agentID string, deps OwnerDeps) (Record, error) {
	record, err := store.Load(agentID)
	if err != nil {
		return Record{}, err
	}
	if record.State.Terminal() {
		return record, fmt.Errorf("agent run %s is already %s", agentID, record.State)
	}
	if record.WorkerPID <= 0 {
		return record, fmt.Errorf("agent run %s has no recorded worker process to stop", agentID)
	}
	if !processAlive(record.WorkerPID) {
		return record, fmt.Errorf("agent run %s worker process %d is already gone", agentID, record.WorkerPID)
	}
	// Graceful first, then hard: a worker that ignores SIGTERM must still be
	// stopped, because stop's contract is "terminate the worker and everything
	// it started", not "ask it politely".
	if err := terminateOwner(record.WorkerPID, terminationSignal()); err != nil {
		return record, err
	}
	deadline := deps.Now().Add(deps.StopGrace)
	exited := waitForProcessExit(deps.Now, deps.Sleep, deps.StopPollInterval, deadline, func() bool {
		return processAlive(record.WorkerPID)
	})
	if !exited {
		return record, terminateOwner(record.WorkerPID, killSignal())
	}
	return record, nil
}

// waitForProcessExit polls alive() every pollInterval (via the injected
// clock/sleep seam, never a direct time.Now/time.Sleep call) until it
// reports false or now() reaches deadline. It returns false when the
// deadline passed while alive() was still true — StopRun's signal to
// escalate to SIGKILL — and true once alive() reports false.
func waitForProcessExit(now func() time.Time, sleep func(time.Duration), pollInterval time.Duration, deadline time.Time, alive func() bool) bool {
	for alive() && now().Before(deadline) {
		sleep(pollInterval)
	}
	return !alive()
}
