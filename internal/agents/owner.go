package agents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sneat-dev/wb/internal/process"
)

// OwnerArgument selects the private, detached run-owner command. It is handled
// before normal command dispatch, following WB's existing self-exec convention
// for internal child processes: the dispatching process re-invokes its own
// executable with this argument rather than depending on a daemon.
const OwnerArgument = "--wb-internal-agent-run"

// inheritedLeaseFD is the descriptor number the dispatcher hands the owner. The
// owner forwards it to the harness and to every validation step, so the kernel
// keeps the worktree claim while any of them is alive.
const inheritedLeaseFD = 3

// leaseEnvMarker tells the owner that fd 3 really is the dispatcher's lease. A
// process that merely happens to have fd 3 open (a test binary, for example)
// must never adopt it.
const leaseEnvMarker = "WB_AGENT_LEASE_FD"

// cancelRequestedFileName marks a run whose stop was requested, so the owner can
// tell a deliberate termination from a crash.
const cancelRequestedFileName = "cancel-requested"

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
	// Lease is the inherited worktree claim. Production reads the descriptor the
	// dispatcher passed; a test supplies a file it already holds a lock on.
	Lease *os.File
	// Validation runs the snapshotted objective checks. It is a seam so a test
	// can assert the environment and the inherited lease without spawning a
	// process tree.
	Validation func(context.Context, ValidationOptions) ValidationRecord
	// LoadExecutionPolicy re-reads the operator's authoritative mandatory
	// execution policy before this owner starts anything. A nil seam is
	// defaulted by RunOwner to LoadDefaultExecutionPolicy: an owner must never
	// treat "no loader injected" as "no mandatory sandbox". Tests that need the
	// historical host behaviour inject an explicit loader that returns nil.
	LoadExecutionPolicy func() (*ExecutionPolicy, error)
	// CancelPollInterval is how often a contained execution checks the durable
	// cancellation sentinel during setup, the harness and validation. Zero
	// selects the production default. It is a struct field, not package state.
	CancelPollInterval time.Duration
	// openContainedRuntime is the narrow per-instance runtime opener for the
	// contained branch. Production leaves it nil and OpenContainerRuntime is
	// used; a focused test supplies a fake. It is deliberately unexported so
	// there is no public bypass.
	openContainedRuntime containedRuntimeOpener
	// observeGitMetadataContext is a focused test seam: it sees the finite,
	// cancellation-independent context the post-run Git metadata snapshots are
	// given, so a test can prove the real production context is bounded. It
	// never replaces the context and is unexported so no user option can reach it.
	observeGitMetadataContext func(context.Context)
	// harnessFor resolves the host harness adapter. Production leaves it nil and
	// the real HarnessFor is used; a focused test supplies a fake adapter to
	// exercise a capability combination the default registry does not expose.
	// It is unexported so there is no public bypass.
	harnessFor func(string) (Harness, error)
	// containedLogLimit bounds each captured contained stream. Zero selects the
	// production maxLogBytes and a larger value is clamped; it is unexported so
	// no user flag or configuration can raise the bound.
	containedLogLimit int64
}

// DefaultOwnerDeps returns the production seams. Lease is deliberately not set
// here: only OwnerCLI, the detached entry point, may adopt an inherited
// descriptor, because a test process's fd 3 is not a lease.
//
// LoadExecutionPolicy is deliberately not set here even though RunOwner
// defaults a nil seam to the authoritative loader: keeping it absent lets a
// test see whether it injected an explicit policy seam, and ProductionOwnerDeps
// makes the production wiring unambiguous.
func DefaultOwnerDeps() OwnerDeps {
	return OwnerDeps{
		LookPath: exec.LookPath, Now: time.Now, Sleep: time.Sleep,
		StopGrace: defaultStopGrace, StopPollInterval: defaultStopPollInterval,
		Validation:         RunValidation,
		CancelPollInterval: defaultCancelPollInterval,
	}
}

// ProductionOwnerDeps is DefaultOwnerDeps plus the authoritative policy loader.
// It is the wiring the detached owner entry point uses, and a direct RunOwner
// gets the same authority because a nil loader is defaulted rather than read as
// "no mandatory sandbox".
func ProductionOwnerDeps() OwnerDeps {
	deps := DefaultOwnerDeps()
	deps.LoadExecutionPolicy = LoadDefaultExecutionPolicy
	return deps
}

// inheritedLease returns the lease descriptor the dispatcher passed, or nil when
// this owner was started without one.
func inheritedLease() *os.File {
	raw := strings.TrimSpace(os.Getenv(leaseEnvMarker))
	if raw == "" {
		return nil
	}
	fd, err := strconv.Atoi(raw)
	if err != nil || fd <= 0 {
		return nil
	}
	file := os.NewFile(uintptr(fd), "wb-agent-lease")
	if _, err := file.Stat(); err != nil {
		return nil
	}
	return file
}

// RunOwner executes one dispatched run to completion and records its terminal
// state. It is the process that outlives the dispatching CLI, so everything a
// later `status` or `await` can learn about the run is written here.
func RunOwner(ctx context.Context, store Store, agentID string, deps OwnerDeps) error {
	return runOwnerWithSave(ctx, store, agentID, deps, store.Save, maxLogBytes)
}

func runOwnerWithSave(ctx context.Context, store Store, agentID string, deps OwnerDeps, save func(Record) error, logLimit int64) error {
	if deps.LookPath == nil {
		deps.LookPath = exec.LookPath
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Validation == nil {
		deps.Validation = RunValidation
	}
	// The configuration is the authority even for a direct RunOwner call. A
	// nil seam is defaulted, never read as "no mandatory sandbox": otherwise a
	// stale host record could launch after the operator configured one.
	if deps.LoadExecutionPolicy == nil {
		deps.LoadExecutionPolicy = LoadDefaultExecutionPolicy
	}
	if deps.CancelPollInterval <= 0 {
		deps.CancelPollInterval = defaultCancelPollInterval
	}
	record, err := store.Load(agentID)
	if err != nil {
		return err
	}
	// The mandatory policy is decided before the owner resolves a credential,
	// looks up a harness, creates its private home, starts a child or runs
	// validation. The fresh user configuration is the authority: a stale host
	// record is refused when the operator declares a policy, and a matching,
	// valid policy admits exactly the contained dsh path.
	configured, policyErr := ownerExecutionPolicy(record, deps)
	if policyErr != nil {
		record.State = StateFailed
		record.ExecutionStatus = StatusFailed
		record.Failure = policyErr.Error()
		record.FailureCategory = FailureConfig
		record.Validation = ValidationRecord{Status: ValidationSkipped, Reason: "the mandatory execution policy refuses this run"}
		record.FinishedAt = deps.Now().UTC()
		return save(record)
	}
	// A queued attempt can be cancelled before it ever starts; honour the
	// sentinel without launching anything.
	if cancelRequested(store, agentID) {
		record.State = StateFailed
		record.ExecutionStatus = StatusCancelled
		record.Failure = "the execution was cancelled before it started"
		record.FailureCategory = FailureCancelled
		record.Validation = ValidationRecord{Status: ValidationSkipped, Reason: "the execution was cancelled"}
		record.FinishedAt = deps.Now().UTC()
		return save(record)
	}
	record.OwnerPID = os.Getpid()
	record.ExecutionStatus = StatusRunning
	record.ExecutionStartedAt = deps.Now().UTC()
	if !record.StartedAt.IsZero() {
		record.QueueDurationMS = record.ExecutionStartedAt.Sub(record.StartedAt).Milliseconds()
	}
	if err := save(record); err != nil {
		return err
	}
	if record.Execution != nil {
		return runContainedOwner(ctx, store, agentID, record, deps, save, configured)
	}
	// The bound is enforced here, in the process that outlives the dispatcher,
	// using the same process-group cancellation internal/process already owns.
	// Leaving it to the harness would make it advisory. A contained execution
	// applies its own whole-lifetime bound inside runContainedOwner.
	if record.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(record.TimeoutMS)*time.Millisecond)
		defer cancel()
	}

	finish := func(state State, failure, category string) error {
		record.State = state
		if !record.ExecutionStatus.Terminal() {
			record.ExecutionStatus = executionStatusFromState(state)
		}
		record.Failure = failure
		record.FailureCategory = category
		record.FinishedAt = deps.Now().UTC()
		if started := executionStart(record); !started.IsZero() {
			record.DurationMS = record.FinishedAt.Sub(started).Milliseconds()
		}
		return save(record)
	}

	adapter, err := ownerHarnessFor(deps, record.Resolved.Harness)
	if err != nil {
		return finish(StateFailed, err.Error(), FailureConfig)
	}
	credential := Credential{}
	if adapter.Capabilities().Credential == CredentialWBInjected {
		credential, err = ResolveCredential(record.Resolved.Routing)
		if err != nil {
			return finish(StateFailed, err.Error(), FailureCredential)
		}
	}
	executable, err := deps.LookPath(harnessExecutable(record.Resolved.Harness))
	if err != nil {
		return finish(StateFailed, fmt.Sprintf("harness executable %q not found on PATH: %v", record.Resolved.Harness, err), FailureHarnessMiss)
	}
	harnessHome := store.HarnessHomePath(agentID)
	if err := os.MkdirAll(harnessHome, 0o700); err != nil {
		return finish(StateFailed, fmt.Sprintf("create private harness home: %v", err), FailureInternal)
	}
	// The harness is told the resolved variable *name*, whichever source the
	// credential came from, so file-sourced credentials use the same
	// per-process mechanism as environment-sourced ones.
	routing := record.Resolved.Routing
	if credential.EnvName != "" {
		routing.CredentialEnv = credential.EnvName
	}
	routing.CredentialFile = ""
	launch, err := adapter.Prepare(Launch{
		Timeout:         time.Duration(record.TimeoutMS) * time.Millisecond,
		RunDir:          store.Dir(agentID),
		WorktreeDir:     record.WorktreeDir,
		Model:           record.Resolved.Model,
		Variant:         effortVariant(record),
		Effort:          record.EffortEffective,
		ProviderName:    record.Resolved.Provider,
		Provider:        routing,
		LastMessagePath: store.LastMessagePath(agentID),
		Task:            record.Task,
	})
	if err != nil {
		return finish(StateFailed, err.Error(), FailureConfig)
	}

	logFile, err := os.OpenFile(store.LogPath(agentID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return finish(StateFailed, fmt.Sprintf("open run log: %v", err), FailureInternal)
	}
	defer func() { _ = logFile.Close() }()
	log := &boundedLog{writer: logFile, remaining: logLimit}
	firstOutput := &firstWriteClock{now: deps.Now, started: record.StartedAt}
	redactor := newRedactingWriter(log, credential.Value)
	var sink io.Writer = log
	if redactor != nil {
		sink = redactor
	}
	sink = &timedWriter{inner: sink, clock: firstOutput}

	// The harness reads the task from stdin where it can, so task text never
	// appears in the process table. An adapter that must use argv says so in its
	// capabilities and the record keeps that fact.
	command := process.CommandContext(ctx, executable, launch.Argv...)
	command.Dir = record.WorktreeDir
	command.Env = append(WorkerEnvironment(credential), launch.Env...)
	if record.Resolved.Harness == HarnessCodex {
		command.Env = append(command.Env, "CODEX_HOME="+harnessHome)
	}
	if launch.Stdin != "" {
		command.Stdin = strings.NewReader(launch.Stdin)
	}
	command.Stdout = sink
	var stderrRedactor *redactingWriter
	var stderrLog *boundedLog
	if launch.SplitStderr {
		stderrFile, stderrErr := os.OpenFile(filepath.Join(store.Dir(agentID), stderrFileName), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if stderrErr != nil {
			return finish(StateFailed, fmt.Sprintf("open run stderr log: %v", stderrErr), FailureInternal)
		}
		defer func() { _ = stderrFile.Close() }()
		// The separate stderr stream is bounded and redacted by the same rule as
		// stdout. A harness that prints its credential to stderr must not leak it
		// just because the two streams reach different files.
		stderrLog = &boundedLog{writer: stderrFile, remaining: logLimit}
		stderrRedactor = newRedactingWriter(stderrLog, credential.Value)
		var stderrSink io.Writer = stderrLog
		if stderrRedactor != nil {
			stderrSink = stderrRedactor
		}
		command.Stderr = stderrSink
	} else {
		command.Stderr = sink
	}
	command.ExtraFiles = leaseFiles(deps.Lease, 0)
	if err := command.Start(); err != nil {
		return finish(StateFailed, fmt.Sprintf("start %s: %v", record.Resolved.Harness, err), FailureHarness)
	}
	record.WorkerPID = command.Process.Pid
	if err := save(record); err != nil {
		// No durable record owns this child yet. Cancel through the same tree-owned
		// process boundary and reap it even when the caller supplied no timeout.
		_ = command.Cancel()
		_ = command.Wait()
		return err
	}

	waitErr := command.Wait()
	// Wait has joined the stdout-copying goroutine, so the owner goroutine can
	// now publish the clock's private result without racing it.
	if elapsed, ok := firstOutput.elapsedMS(); ok {
		record.TimeToFirstOutputMS = elapsed
	}
	if redactor != nil {
		_ = redactor.Flush()
	}
	if stderrRedactor != nil {
		_ = stderrRedactor.Flush()
	}
	summary := summarizeLogFile(store.LogPath(agentID), adapter)
	record.Usage = legacyUsage(summary.Tokens)
	record.ToolCalls = summary.ToolCalls
	record.HarnessEvents = append(record.HarnessEvents, summary.Diagnostics...)
	if log.truncated() {
		record.HarnessEvents = append(record.HarnessEvents,
			fmt.Sprintf("run log truncated at %d bytes", logLimit))
	}
	if stderrLog != nil && stderrLog.truncated() {
		record.HarnessEvents = append(record.HarnessEvents,
			fmt.Sprintf("run stderr log truncated at %d bytes", logLimit))
	}
	record.Telemetry = buildTelemetry(record.Resolved.Harness, summary)
	record.Cost = buildCost(record.Resolved.Harness, summary, deps.Now())
	if message := finalMessage(summary, store.LastMessagePath(agentID)); message != "" {
		redacted, count := RedactText(message, credential.Value)
		record.Result = BoundResult(redacted)
		if count > 0 {
			record.HarnessEvents = append(record.HarnessEvents, "the final message contained the provider credential and was redacted")
		}
	}
	record.Changes = SummarizeChangesWith(context.WithoutCancel(ctx), record.WorktreeDir, record.BaseSHA, record.DirtyFiles)
	if path, err := store.WriteDiff(context.WithoutCancel(ctx), agentID, record.WorktreeDir, record.BaseSHA, DefaultTargetDeps()); err == nil {
		record.DiffPath = path
	}

	cancelled := cancelRequested(store, agentID)
	switch {
	case cancelled:
		record.Validation = ValidationRecord{Status: ValidationSkipped, Reason: "the execution was cancelled"}
		record.ExecutionStatus = StatusCancelled
		return finish(StateFailed, "the execution was cancelled by an operator", FailureCancelled)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		// The context owns the whole process group, so expiry has already
		// terminated the worker tree; the distinct state is what tells a
		// supervisor this was a bound, not a failure of the work.
		record.Validation = ValidationRecord{Status: ValidationSkipped, Reason: "the execution exceeded its timeout"}
		record.ExecutionStatus = StatusTimedOut
		return finish(StateTimeout, "run exceeded its timeout and its process group was terminated", FailureTimeout)
	case waitErr != nil:
		code := exitCodeOf(command)
		record.ExitCode = code
		record.Validation = ValidationRecord{Status: ValidationSkipped, Reason: "the harness did not complete"}
		return finish(StateFailed, failureMessage(summary, waitErr), classifyFailure(summary))
	case summary.TurnFailed:
		code := 0
		record.ExitCode = &code
		record.Validation = ValidationRecord{Status: ValidationSkipped, Reason: "the harness reported a failed turn"}
		return finish(StateFailed, "harness exited zero but reported a failed turn", classifyFailure(summary))
	case summary.MalformedLines > 0:
		code := exitCodeOf(command)
		record.ExitCode = code
		record.Validation = ValidationRecord{Status: ValidationSkipped, Reason: "the harness stream was unparsable"}
		return finish(StateFailed, fmt.Sprintf("harness produced no terminal turn event and %d unparsable event line(s); the run is not a success", summary.MalformedLines), FailureHarness)
	case !summary.TurnCompleted:
		code := exitCodeOf(command)
		record.ExitCode = code
		record.Validation = ValidationRecord{Status: ValidationSkipped, Reason: "the harness produced no completion evidence"}
		return finish(StateFailed, "harness produced no terminal turn event; the run is not a success", FailureHarness)
	default:
		code := exitCodeOf(command)
		record.ExitCode = code
		record.Validation = runValidation(ctx, store, agentID, record, deps)
		return finish(StateCompleted, "", "")
	}
}

// runValidation records the running state before the first step, so a long
// validation is visible instead of appearing as a stuck completed run.
func runValidation(ctx context.Context, store Store, agentID string, record Record, deps OwnerDeps) ValidationRecord {
	steps := validationStepsFromRecord(record)
	if len(steps) == 0 {
		return ValidationRecord{Status: ValidationSkipped, Reason: "no validation steps were configured"}
	}
	startedAt := deps.Now().UTC()
	record.Validation = ValidationRecord{Status: ValidationRunning, Steps: record.Validation.Steps, StartedAt: &startedAt}
	_ = store.Save(record)
	validation := deps.Validation(context.WithoutCancel(ctx), ValidationOptions{
		WorktreeDir: record.WorktreeDir,
		RunDir:      store.Dir(agentID),
		Steps:       steps,
		Lease:       deps.Lease,
		Now:         deps.Now,
	})
	return validation
}

// ownerHarnessFor resolves the adapter through the optional per-instance seam,
// defaulting to the exact production HarnessFor. The mandatory execution policy
// has already been decided before this is called, so a supplied adapter can
// never bypass the gate that decides whether a host run may start at all.
func ownerHarnessFor(deps OwnerDeps, harness string) (Harness, error) {
	if deps.harnessFor != nil {
		return deps.harnessFor(harness)
	}
	return HarnessFor(harness)
}

func validationStepsFromRecord(record Record) []ValidationStep {
	steps := make([]ValidationStep, 0, len(record.Validation.Steps))
	for _, step := range record.Validation.Steps {
		steps = append(steps, ValidationStep{Name: step.Name, Argv: step.Argv, Timeout: step.Timeout})
	}
	return steps
}

// ownerExecutionPolicy applies the one mandatory-policy authority to a
// persisted record. The fresh user configuration is the authority, so an owner
// that can reload it refuses a stale host record, and any configuration load
// error refuses too: a broken file must never read as "no mandatory sandbox".
func ownerExecutionPolicy(record Record, deps OwnerDeps) (*ExecutionPolicy, error) {
	if deps.LoadExecutionPolicy == nil {
		deps.LoadExecutionPolicy = LoadDefaultExecutionPolicy
	}
	configured, err := deps.LoadExecutionPolicy()
	if err != nil {
		return nil, fmt.Errorf("reload the authoritative agents.execution policy: %w", err)
	}
	if err := RequireRunnableExecution(configured, record.Execution, "run record", record.Resolved.Harness); err != nil {
		return nil, err
	}
	return configured, nil
}

// executionStart is when the harness began, falling back to admission time for
// a record written before the two were distinguished.
func executionStart(record Record) time.Time {
	if !record.ExecutionStartedAt.IsZero() {
		return record.ExecutionStartedAt
	}
	return record.StartedAt
}

func effortVariant(record Record) string {
	if record.EffortApplied && record.EffortEffective != "" {
		return record.Resolved.EffortVariants[record.EffortEffective]
	}
	return record.Resolved.Variant
}

func leaseFiles(lease *os.File, index int) []*os.File {
	if lease == nil {
		return nil
	}
	files := make([]*os.File, index+1)
	files[index] = lease
	return files
}

func finalMessage(summary HarnessSummary, lastMessagePath string) string {
	if strings.TrimSpace(summary.FinalMessage) != "" {
		return summary.FinalMessage
	}
	if strings.TrimSpace(lastMessagePath) == "" {
		return ""
	}
	message, err := os.ReadFile(lastMessagePath)
	if err != nil {
		return ""
	}
	// The harness writes this file itself with its own umask, so WB tightens
	// the mode after reading it.
	_ = os.Chmod(lastMessagePath, 0o600)
	return string(message)
}

func failureMessage(summary HarnessSummary, waitErr error) string {
	if summary.TurnFailed {
		return "harness reported a failed turn"
	}
	return fmt.Sprintf("harness exited with %v", waitErr)
}

// classifyFailure turns the harness's own diagnostics into a category an
// orchestrator can branch on without parsing prose.
func classifyFailure(summary HarnessSummary) string {
	for _, diagnostic := range summary.Diagnostics {
		lowered := strings.ToLower(diagnostic)
		switch {
		case strings.Contains(lowered, "provider."), strings.Contains(lowered, "rate limit"), strings.Contains(lowered, "unauthorized"):
			return FailureProvider
		case strings.Contains(lowered, "credential"), strings.Contains(lowered, "api key"), strings.Contains(lowered, "authentication"):
			return FailureCredential
		}
	}
	return FailureHarness
}

func cancelRequested(store Store, agentID string) bool {
	_, err := os.Stat(filepath.Join(store.Dir(agentID), cancelRequestedFileName))
	return err == nil
}

// requestCancel writes the sentinel StopRun uses to distinguish a deliberate
// stop from a crash.
func requestCancel(store Store, agentID string, now func() time.Time) error {
	path := filepath.Join(store.Dir(agentID), cancelRequestedFileName)
	return os.WriteFile(path, []byte(now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

func exitCodeOf(command *exec.Cmd) *int {
	if command.ProcessState == nil {
		return nil
	}
	code := command.ProcessState.ExitCode()
	return &code
}

// summarizeLogFile reads the harness event stream back. A log that cannot be
// read yields an empty summary rather than failing the run: the exit status and
// the log path are still true and still useful.
func summarizeLogFile(path string, adapter Harness) HarnessSummary {
	file, err := os.Open(path)
	if err != nil {
		return HarnessSummary{}
	}
	defer func() { _ = file.Close() }()
	return adapter.Summarize(io.LimitReader(file, maxLogBytes))
}

// firstWriteClock records how long the harness ran before its first output.
// It deliberately holds no Record pointer: mark runs on os/exec's stdout-copying
// goroutine while the owner goroutine reads and saves the record, so mutating
// the record here would be a data race. The clock keeps a private, mutex-guarded
// elapsed value that only the owner copies into the record, after Wait has
// joined the copying goroutines.
type firstWriteClock struct {
	mutex    sync.Mutex
	now      func() time.Time
	started  time.Time
	elapsed  int64
	observed bool
}

func (clock *firstWriteClock) mark() {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	if clock.observed || clock.started.IsZero() {
		return
	}
	clock.observed = true
	clock.elapsed = clock.now().Sub(clock.started).Milliseconds()
}

// elapsed reports the recorded first-output latency. A zero elapsed value with
// observed false means no output was ever written (or the clock had no start),
// which the owner records as the zero value it already is.
func (clock *firstWriteClock) elapsedMS() (int64, bool) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.elapsed, clock.observed
}

// timedWriter marks the first byte written through it.
type timedWriter struct {
	inner io.Writer
	clock *firstWriteClock
}

func (writer *timedWriter) Write(data []byte) (int, error) {
	if len(data) > 0 {
		writer.clock.mark()
	}
	return writer.inner.Write(data)
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
	// This is the one place an inherited lease descriptor may be adopted: the
	// dispatcher passed it, and no other process has this owner's fd 3 meaning.
	if deps.Lease == nil {
		deps.Lease = inheritedLease()
	}
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
func SpawnOwner(runDir string, executable func() (string, error), extraFiles []*os.File) (int, error) {
	return spawnOwnerWithNull(runDir, executable, extraFiles, func() (*os.File, error) { return os.OpenFile(os.DevNull, os.O_RDWR, 0) })
}

func spawnOwnerWithNull(runDir string, executable func() (string, error), extraFiles []*os.File, openNull func() (*os.File, error)) (int, error) {
	path, err := executable()
	if err != nil {
		return 0, fmt.Errorf("locate the wb executable for the run owner: %w", err)
	}
	command := exec.Command(path, OwnerArgument, "--run-dir", runDir) //nolint:gosec // current wb executable and fixed arguments
	command.Env = os.Environ()
	command.ExtraFiles = extraFiles
	if len(extraFiles) > 0 {
		// The marker is what lets the owner distinguish a real lease descriptor
		// from a stray fd 3 it happened to inherit.
		command.Env = append(command.Env, leaseEnvMarker+"="+strconv.Itoa(inheritedLeaseFD))
	}
	process.ConfigureDetached(command)
	null, err := openNull()
	if err != nil {
		return 0, err
	}
	defer func() { _ = null.Close() }()
	command.Stdin, command.Stdout, command.Stderr = null, null, null
	if err := command.Start(); err != nil {
		return 0, err
	}
	pid := command.Process.Pid
	// Go 1.27 Release only errors on Windows when called a second time.
	// This fresh, locally owned Process has not escaped or been released.
	_ = command.Process.Release()
	return pid, nil
}

// StopRun terminates a running worker's process group. The owner is left alive
// deliberately: it observes the non-zero exit and records the terminal state,
// so a stopped run reports a real outcome instead of vanishing into
// "abandoned". A contained execution is stopped only through the durable
// cancellation sentinel: its owner cancels the whole runtime, because a host
// PID is either absent during setup or not authoritative for the container.
func StopRun(store Store, agentID string, deps OwnerDeps) (Record, error) {
	return stopRunWithSignal(store, agentID, deps, terminateOwner)
}

func stopRunWithSignal(store Store, agentID string, deps OwnerDeps, terminate func(int, syscall.Signal) error) (Record, error) {
	record, err := store.Load(agentID)
	if err != nil {
		return Record{}, err
	}
	if record.State.Terminal() {
		return record, fmt.Errorf("agent run %s is already %s", agentID, record.State)
	}
	if record.Execution != nil {
		// A contained execution's authority is the durable sentinel, not a host
		// PID that may be zero during setup/validation or reused after a crash.
		// The owner's monitor cancels its context and closes the whole runtime.
		if err := requestCancel(store, agentID, deps.Now); err != nil {
			return record, fmt.Errorf("record cancellation request for %s: %w", agentID, err)
		}
		return record, nil
	}
	if record.WorkerPID <= 0 {
		if record.ExecutionStatus == StatusQueued {
			// Nothing is running yet: the sentinel is the whole cancellation. The
			// owner records it when it reaches the front of the queue.
			if err := requestCancel(store, agentID, deps.Now); err != nil {
				return record, fmt.Errorf("record cancellation request for %s: %w", agentID, err)
			}
			return record, nil
		}
		return record, fmt.Errorf("agent run %s has no recorded worker process to stop", agentID)
	}
	if !processAlive(record.WorkerPID) {
		return record, fmt.Errorf("agent run %s worker process %d is already gone", agentID, record.WorkerPID)
	}
	// The sentinel is written before the signal so a run that exits instantly
	// still records a deliberate cancellation rather than a crash.
	if err := requestCancel(store, agentID, deps.Now); err != nil {
		return record, fmt.Errorf("record cancellation request for %s: %w", agentID, err)
	}
	// Graceful first, then hard: a worker that ignores SIGTERM must still be
	// stopped, because stop's contract is "terminate the worker and everything
	// it started", not "ask it politely".
	if err := terminate(record.WorkerPID, terminationSignal()); err != nil {
		return record, err
	}
	deadline := deps.Now().Add(deps.StopGrace)
	exited := waitForProcessExit(deps.Now, deps.Sleep, deps.StopPollInterval, deadline, func() bool {
		return processAlive(record.WorkerPID)
	})
	if !exited {
		return record, terminate(record.WorkerPID, killSignal())
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
