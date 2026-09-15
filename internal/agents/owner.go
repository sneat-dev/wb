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
)

// OwnerArgument selects the private, detached run-owner command. It is handled
// before normal command dispatch, following WB's existing self-exec convention
// for internal child processes: the dispatching process re-invokes its own
// executable with this argument rather than depending on a daemon.
const OwnerArgument = "--wb-internal-agent-run"

// maxLogBytes bounds the captured harness stream so a long, chatty worker
// cannot fill the disk. Beyond the bound the log is truncated and the run
// records that it was, rather than the writes failing under the harness.
const maxLogBytes = 128 << 20

// OwnerDeps are the seams the run owner needs. They are injected so the whole
// owner can be exercised deterministically against a fake harness.
type OwnerDeps struct {
	// LookPath resolves the harness executable. Resolving it by name through
	// PATH is what lets a test substitute a fake harness without a production
	// override flag.
	LookPath func(string) (string, error)
	// Now is the clock, injected for deterministic tests.
	Now func() time.Time
}

// DefaultOwnerDeps returns the production seams.
func DefaultOwnerDeps() OwnerDeps {
	return OwnerDeps{LookPath: exec.LookPath, Now: time.Now}
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

	if name, missing := MissingCredential(record.Resolved.Routing.CredentialEnv); missing {
		return finish(StateFailed, fmt.Sprintf("provider credential %s is not set in the dispatching environment", name))
	}
	executable, err := deps.LookPath(record.Resolved.Harness)
	if err != nil {
		return finish(StateFailed, fmt.Sprintf("harness executable %q not found on PATH: %v", record.Resolved.Harness, err))
	}
	harnessHome := store.HarnessHomePath(agentID)
	if err := os.MkdirAll(harnessHome, 0o700); err != nil {
		return finish(StateFailed, fmt.Sprintf("create private harness home: %v", err))
	}
	argv, err := CodexArgv(HarnessOptions{
		WorktreeDir:     record.WorktreeDir,
		Model:           record.Resolved.Model,
		Reasoning:       record.Resolved.Reasoning,
		ProviderName:    record.Resolved.Provider,
		Provider:        record.Resolved.Routing,
		HarnessHome:     harnessHome,
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
	command.Env = append(WorkerEnvironment(record.Resolved.Routing.CredentialEnv), "CODEX_HOME="+harnessHome)
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
	path, err := executable()
	if err != nil {
		return 0, fmt.Errorf("locate the wb executable for the run owner: %w", err)
	}
	command := exec.Command(path, OwnerArgument, "--run-dir", runDir) //nolint:gosec // current wb executable and fixed arguments
	command.Env = os.Environ()
	configureDetached(command)
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = null.Close() }()
	command.Stdin, command.Stdout, command.Stderr = null, null, null
	if err := command.Start(); err != nil {
		return 0, err
	}
	pid := command.Process.Pid
	if err := command.Process.Release(); err != nil {
		return 0, err
	}
	return pid, nil
}

// StopRun terminates a running worker's process group. The owner is left alive
// deliberately: it observes the non-zero exit and records the terminal state,
// so a stopped run reports a real outcome instead of vanishing into
// "abandoned".
func StopRun(store Store, agentID string) (Record, error) {
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
	return record, terminateOwner(record.WorkerPID, terminationSignal())
}
