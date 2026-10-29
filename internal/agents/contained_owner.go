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
	"time"

	"gopkg.in/yaml.v3"
)

// This file wires the already-proven primitives into one native contained
// execution. It is the owner's contained branch: the only process it starts on
// the host is Docker itself, and every workload command runs through the one
// container the runtime opened and verified.
//
// The container is opened only after the fresh configuration has re-admitted
// the record's mandatory snapshot, the record's finite lifetime has been
// computed, and the one explicit provider credential has been resolved. Host
// LookPath, Preflight and DetectVersion are never called from here.

const (
	// containerPreflightTimeout bounds each in-container read-only probe.
	containerPreflightTimeout = 15 * time.Second
	// maxContainerDshDumpBytes bounds the --dump-config document before YAML
	// parsing can allocate without limit.
	maxContainerDshDumpBytes = 1 << 20
	// maxContainerDshVersionBytes bounds the harness version output.
	maxContainerDshVersionBytes = 8 << 10
	// defaultCancelPollInterval is how often the owner checks for the
	// cancellation sentinel while Docker is doing setup, the harness is
	// running, or validation is running.
	defaultCancelPollInterval = 100 * time.Millisecond
	// containedGitMetadataTimeout bounds the host Git metadata reads taken
	// after cleanup. The context is independent of the owner's lifetime (a
	// cancelled run still has a stable artifact to record) but always finite, so
	// a stuck Git read cannot hold the global execution slot forever. It is used
	// only after runtime.Close has proved the container namespace absent, so the
	// read never observes a workload that may still be writing the worktree.
	containedGitMetadataTimeout = 30 * time.Second
)

// containedRuntime is the narrow per-instance surface the contained owner
// needs. Production is *ContainerRuntime; a focused test supplies a fake
// through OwnerDeps.openContainedRuntime. There is deliberately no public
// option, flag or package global that can bypass the real runtime.
type containedRuntime interface {
	ContainerID() string
	ImageID() string
	VerifiedLimits() ContainerVerifiedLimits
	PrepareWorkerHome(context.Context) error
	WorkerCommand(context.Context, Credential, []string) (*exec.Cmd, error)
	ValidationTransport() ValidationCommandFactory
	Close() error
}

// containedRuntimeOpener opens one runtime. It is the seam tests replace.
type containedRuntimeOpener func(context.Context, ContainerRuntimeOptions) (containedRuntime, error)

func openContainedRuntime(ctx context.Context, options ContainerRuntimeOptions) (containedRuntime, error) {
	return OpenContainerRuntime(ctx, options)
}

// containedResult is the terminal outcome the container execution produced.
// It is applied to the durable record only after Close has proved absence.
type containedResult struct {
	State           State
	ExecutionStatus ExecutionStatus
	Failure         string
	Category        string
	ExitCode        *int
	Validation      ValidationRecord
	// HarnessRan reports that the harness command itself was started. A setup
	// failure that never reached the harness must not claim a run-time change
	// snapshot.
	HarnessRan bool
	// saveErr records an intermediate save failure, which must still close the
	// runtime before the owner returns.
	saveErr error
}

func containedCancelledResult() containedResult {
	return containedResult{
		State: StateFailed, ExecutionStatus: StatusCancelled,
		Failure: "the execution was cancelled by an operator", Category: FailureCancelled,
		Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the execution was cancelled"},
	}
}

func containedTimedOutResult(reason string) containedResult {
	return containedResult{
		State: StateTimeout, ExecutionStatus: StatusTimedOut,
		Failure: reason, Category: FailureTimeout,
		Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the execution exceeded its timeout"},
	}
}

// containedSetupBudgetExpired reports whether the finite setup pool, rather than
// the whole execution lifetime or an operator stop, has run out. It requires the
// outer context to still be live, so an expired total lifetime can never be
// reported as a setup timeout, and callers still report an operator stop as a
// cancellation.
func containedSetupBudgetExpired(ctx, setupCtx context.Context) bool {
	return ctx.Err() == nil && errors.Is(setupCtx.Err(), context.DeadlineExceeded)
}

// containedSetupTimeoutResult is the terminal outcome for a setup stage that
// exhausted its finite pool. The stage names the actual step that was running
// and the raw context deadline text is preserved, so an infrastructure timeout
// is never recorded as an ordinary configuration refusal. A setup stage never
// started the harness, so the result claims no run-time workspace change.
func containedSetupTimeoutResult(stage string, err error) containedResult {
	failure := fmt.Sprintf("%s exceeded the %s container setup budget: %v", stage, containerSetupBudget, context.DeadlineExceeded)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		failure = fmt.Sprintf("%s exceeded the %s container setup budget: %v: %v", stage, containerSetupBudget, context.DeadlineExceeded, err)
	}
	return containedResult{
		State: StateTimeout, ExecutionStatus: StatusTimedOut,
		Failure: failure, Category: FailureTimeout,
		Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the contained setup exceeded its finite budget"},
	}
}

// containedRuntimeOpenFailure classifies a refused runtime open. An expired
// setup pool is an infrastructure timeout, distinct from the generic internal
// failure of an open that genuinely failed; an operator stop or the expired
// outer lifetime is still reported as exactly that. The refused open already
// ran its own partial-open cleanup before this is recorded, so this claims no
// container.
func containedRuntimeOpenFailure(ctx, setupCtx context.Context, store Store, agentID string, err error) containedResult {
	if outcome, ok := containedCancellationOutcome(ctx, store, agentID); ok {
		return outcome
	}
	if containedSetupBudgetExpired(ctx, setupCtx) {
		return containedSetupTimeoutResult("open the verified container", err)
	}
	return containedResult{
		State: StateFailed, ExecutionStatus: StatusFailed,
		Failure: fmt.Sprintf("open the verified container: %v", err), Category: FailureInternal,
		Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the container runtime could not be opened"},
	}
}

// runContainedOwner executes one record under its admitted mandatory policy.
// The caller has already reloaded and matched the authoritative configuration.
func runContainedOwner(baseCtx context.Context, store Store, agentID string, record Record, deps OwnerDeps, save func(Record) error, configured *ExecutionPolicy) error {
	if configured == nil || record.Execution == nil {
		return finishContainedRecord(store, agentID, record, save, deps, containedResult{
			State: StateFailed, ExecutionStatus: StatusFailed,
			Failure: "contained execution requires a matching mandatory agents.execution policy", Category: FailureConfig,
			Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the mandatory execution policy is unavailable"},
		})
	}
	if record.ExecutionWorkspace == nil {
		return finishContainedRecord(store, agentID, record, save, deps, containedResult{
			State: StateFailed, ExecutionStatus: StatusFailed,
			Failure: "contained execution requires the trusted workspace snapshot from admission", Category: FailureConfig,
			Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the trusted execution workspace is missing"},
		})
	}
	if record.Resolved.Harness != HarnessDSH {
		return finishContainedRecord(store, agentID, record, save, deps, containedResult{
			State: StateFailed, ExecutionStatus: StatusFailed,
			Failure: fmt.Sprintf("contained execution cannot run the %q harness", record.Resolved.Harness), Category: FailureConfig,
			Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the harness cannot run inside the mandatory sandbox"},
		})
	}
	// The whole execution is finite before a credential is read or a container
	// is opened. This is the same bound the runtime enforces.
	lifetime, err := ContainedRecordLifetime(record)
	if err != nil {
		return finishContainedRecord(store, agentID, record, save, deps, containedResult{
			State: StateFailed, ExecutionStatus: StatusFailed,
			Failure: err.Error(), Category: FailureConfig,
			Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the contained lifetime was refused"},
		})
	}
	if _, err := containedRunTimeout(record.TimeoutMS); err != nil {
		return finishContainedRecord(store, agentID, record, save, deps, containedResult{
			State: StateFailed, ExecutionStatus: StatusFailed,
			Failure: err.Error(), Category: FailureConfig,
			Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the contained harness timeout was refused"},
		})
	}
	// Exactly one explicit official routing credential. There is no host DSH
	// store and no ambient credential fallback.
	credential, err := ResolveCredential(record.Resolved.Routing)
	if err != nil {
		return finishContainedRecord(store, agentID, record, save, deps, containedResult{
			State: StateFailed, ExecutionStatus: StatusFailed,
			Failure: err.Error(), Category: FailureCredential,
			Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the provider credential could not be resolved"},
		})
	}
	baseURL := containedDshEffectiveBaseURL(record.Resolved.Routing.BaseURL)

	ctx, cancel := context.WithTimeout(baseCtx, lifetime)
	defer cancel()
	// The sentinel is authoritative during setup, the harness and validation.
	ctx, stopMonitor := monitorCancellation(ctx, store, agentID, deps.CancelPollInterval)
	defer stopMonitor()
	// Setup gets its own finite pool derived from the same total lifetime. It
	// covers the runtime open, the worker-home preparation and the read-only
	// probes; a slow setup therefore can never consume the declared harness or
	// validation time.
	setupCtx, cancelSetup := context.WithTimeout(ctx, containerSetupBudget)
	defer cancelSetup()

	launch, err := PrepareContainedDshLaunchFromRecord(record, store.Dir(agentID), credential.EnvName)
	if err != nil {
		return finishContainedRecord(store, agentID, record, save, deps, containedResult{
			State: StateFailed, ExecutionStatus: StatusFailed,
			Failure: err.Error(), Category: FailureConfig,
			Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the contained launch could not be prepared"},
		})
	}
	opener := deps.openContainedRuntime
	if opener == nil {
		opener = openContainedRuntime
	}
	runtime, err := opener(setupCtx, ContainerRuntimeOptions{
		Policy: *configured, RunID: agentID, Lifetime: lifetime, Workspace: launch.Workspace,
	})
	if err != nil {
		result := containedRuntimeOpenFailure(ctx, setupCtx, store, agentID, err)
		return finishContainedRecord(store, agentID, record, save, deps, result)
	}
	// Runtime identity is persisted only after the runtime actually opened. A
	// partial open already cleaned itself up and never reaches this point.
	facts := &ExecutionRuntimeFacts{
		ContainerID:     runtime.ContainerID(),
		ResolvedImageID: runtime.ImageID(),
		VerifiedLimits:  snapshotVerifiedLimits(runtime.VerifiedLimits()),
		CleanupPending:  true,
	}
	record.ExecutionRuntime = facts
	if err := save(record); err != nil {
		return errors.Join(err, runtime.Close())
	}

	result := executeContained(ctx, setupCtx, store, agentID, &record, deps, save, runtime, credential, launch, baseURL)
	// The runtime is closed before any host-side handoff work. A cancelled
	// Docker exec client does not prove the in-container workload stopped, so
	// only Close's namespace-absence proof makes the mounted worktree stable.
	closeErr := runtime.Close()
	if closeErr != nil {
		facts.CleanupPending = false
		facts.CleanupVerified = false
		facts.CleanupFailed = true
		facts.CleanupFailure = BoundResult(closeErr.Error())
		failure := fmt.Sprintf("clean up the contained execution: %v", closeErr)
		// Keep the independent validation evidence exactly as it was actually
		// recorded (Passed, Failed or Skipped). A cleanup failure changes the
		// execution outcome, never what the checks proved.
		validation := result.Validation
		if validation.Status == "" {
			validation = ValidationRecord{Status: ValidationSkipped, Reason: "the container could not be proved absent"}
		}
		result = containedResult{
			State: StateFailed, ExecutionStatus: StatusFailed,
			Failure: failure, Category: FailureInternal, ExitCode: result.ExitCode,
			Validation: validation,
		}
	} else {
		facts.CleanupPending = false
		facts.CleanupVerified = true
		facts.CleanupFailed = false
		facts.CleanupFailure = ""
	}
	if result.saveErr != nil {
		return errors.Join(result.saveErr, closeErr)
	}
	// A stable handoff is captured only when cleanup proved the namespace absent
	// and the harness actually ran. On an uncertain live workload there is no
	// stable change summary to record, and reporting one would be a guess.
	if closeErr == nil && result.HarnessRan {
		captureContainedHandoff(ctx, store, agentID, &record, deps)
	}
	if result.Validation.Status == "" {
		result.Validation = ValidationRecord{Status: ValidationSkipped, Reason: "validation did not run"}
	}
	record.State = result.State
	record.ExecutionStatus = result.ExecutionStatus
	record.Failure = result.Failure
	record.FailureCategory = result.Category
	record.Validation = result.Validation
	record.ExitCode = result.ExitCode
	record.FinishedAt = deps.Now().UTC()
	if started := executionStart(record); !started.IsZero() {
		record.DurationMS = record.FinishedAt.Sub(started).Milliseconds()
	}
	return save(record)
}

func snapshotVerifiedLimits(limits ContainerVerifiedLimits) *ContainerVerifiedLimits {
	copied := limits
	return &copied
}

// finishContainedRecord records a terminal failure reached before a runtime
// receipt exists. It never fabricates runtime facts: it claims no verified
// container. A refused or partial open that created a container still holds its
// cleanup failure in the durable per-user slot, not in this record.
func finishContainedRecord(store Store, agentID string, record Record, save func(Record) error, deps OwnerDeps, result containedResult) error {
	record.State = result.State
	record.ExecutionStatus = result.ExecutionStatus
	record.Failure = result.Failure
	record.FailureCategory = result.Category
	record.Validation = result.Validation
	record.ExitCode = result.ExitCode
	record.FinishedAt = deps.Now().UTC()
	if started := executionStart(record); !started.IsZero() {
		record.DurationMS = record.FinishedAt.Sub(started).Milliseconds()
	}
	return save(record)
}

// executeContained performs the in-container setup, harness and validation. It
// returns the outcome; the caller owns Close and only then makes the record
// terminal. It never reads host Git metadata: a Docker exec client that has
// returned does not prove the in-container workload stopped, so the stable
// handoff is the caller's job after Close.
func executeContained(ctx, setupCtx context.Context, store Store, agentID string, record *Record, deps OwnerDeps, save func(Record) error, runtime containedRuntime, credential Credential, launch ContainedDshLaunch, baseURL string) containedResult {
	failure := func(stage string, err error) containedResult {
		if outcome, ok := containedCancellationOutcome(ctx, store, agentID); ok {
			return outcome
		}
		return containedResult{
			State: StateFailed, ExecutionStatus: StatusFailed,
			Failure: fmt.Sprintf("%s: %v", stage, err), Category: FailureConfig,
			Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the contained setup did not complete"},
		}
	}
	// setupFailure adds the finite setup pool to that classifier: when the pool
	// itself expires, the stage is an infrastructure timeout, not a
	// configuration refusal. An operator stop or the expired outer lifetime is
	// still reported as such, and an ordinary configuration/overlay failure
	// never reaches the timeout branch.
	setupFailure := func(stage string, err error) containedResult {
		if containedSetupBudgetExpired(ctx, setupCtx) {
			if outcome, ok := containedCancellationOutcome(ctx, store, agentID); ok {
				return outcome
			}
			return containedSetupTimeoutResult(stage, err)
		}
		return failure(stage, err)
	}
	if err := runtime.PrepareWorkerHome(setupCtx); err != nil {
		return setupFailure("prepare the contained worker home", err)
	}
	if outcome, ok := containedCancellationOutcome(ctx, store, agentID); ok {
		return outcome
	}
	version, err := runContainedDshVersion(setupCtx, runtime)
	if err != nil {
		return setupFailure("read the contained dsh version", err)
	}
	dump, err := runContainedDshDump(setupCtx, runtime)
	if err != nil {
		return setupFailure("dump the contained dsh configuration", err)
	}
	if err := verifyContainedDshDump(dump, record.Resolved.Model, credential.EnvName, baseURL); err != nil {
		return failure("verify the contained dsh configuration", err)
	}
	// Only after the composed configuration has been proven does the record
	// carry a version, and it is the version the container actually reported.
	// The capability projection is also made truthful here, on the actual
	// execution, using the existing record axes: this run injects its one
	// provider credential and isolates the worker in the verified container.
	record.HarnessVersion = version
	record.HarnessCapabilities = containedHarnessCapabilities(record.HarnessCapabilities)
	record.HarnessCapabilities.Version = version
	if err := save(*record); err != nil {
		return containedResult{saveErr: err}
	}
	if outcome, ok := containedCancellationOutcome(ctx, store, agentID); ok {
		return outcome
	}

	runTimeout, err := containedRunTimeout(record.TimeoutMS)
	if err != nil {
		return failure("bound the contained harness", err)
	}
	runCtx, cancelRun := context.WithTimeout(ctx, runTimeout)
	defer cancelRun()
	// WorkerCommand takes argv *including* the executable and emits
	// `docker exec <ID> <argv>`. The prepared launch argv starts at --profile,
	// exactly as the version and dump probes above carry their own executable,
	// so the literal harness executable is prepended here. The generic
	// WorkerCommand must not guess an executable: validation argv is generic.
	harnessArgv := append([]string{HarnessDSH}, launch.Launch.Argv...)
	command, err := runtime.WorkerCommand(runCtx, credential, harnessArgv)
	if err != nil {
		return failure("build the contained harness command", err)
	}
	logFile, err := os.OpenFile(store.LogPath(agentID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return failure("open the contained run log", err)
	}
	defer func() { _ = logFile.Close() }()
	stdoutLog := &boundedLog{writer: logFile, remaining: maxLogBytes}
	firstOutput := &firstWriteClock{now: deps.Now, started: record.StartedAt}
	stdoutRedactor := newRedactingWriter(stdoutLog, credential.Value)
	var stdout io.Writer = stdoutLog
	if stdoutRedactor != nil {
		stdout = stdoutRedactor
	}
	command.Stdout = &timedWriter{inner: stdout, clock: firstOutput}

	stderrFile, err := os.OpenFile(filepath.Join(store.Dir(agentID), stderrFileName), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return failure("open the contained run stderr log", err)
	}
	defer func() { _ = stderrFile.Close() }()
	stderrLog := &boundedLog{writer: stderrFile, remaining: maxLogBytes}
	stderrRedactor := newRedactingWriter(stderrLog, credential.Value)
	var stderr io.Writer = stderrLog
	if stderrRedactor != nil {
		stderr = stderrRedactor
	}
	command.Stderr = stderr

	if err := command.Start(); err != nil {
		return failure("start dsh inside the contained execution", err)
	}
	record.WorkerPID = command.Process.Pid
	if err := save(*record); err != nil {
		stopCommand(command)
		_ = command.Wait()
		return containedResult{saveErr: err}
	}
	// Every outcome from here on belongs to a harness that actually started, so
	// it may carry a real workspace change once cleanup has proved the
	// namespace absent.
	finishHarness := func(result containedResult) containedResult {
		result.HarnessRan = true
		return result
	}
	waitErr := command.Wait()
	// Wait has reaped the client and every stdout/stderr copying goroutine has
	// finished, so the owner goroutine can now copy the clock's private result
	// into the record without racing it.
	if elapsed, ok := firstOutput.elapsedMS(); ok {
		record.TimeToFirstOutputMS = elapsed
	}
	if stdoutRedactor != nil {
		_ = stdoutRedactor.Flush()
	}
	if stderrRedactor != nil {
		_ = stderrRedactor.Flush()
	}
	if stdoutLog.truncated() {
		record.HarnessEvents = append(record.HarnessEvents,
			fmt.Sprintf("run log truncated at %d bytes", maxLogBytes))
	}
	if stderrLog.truncated() {
		record.HarnessEvents = append(record.HarnessEvents,
			fmt.Sprintf("run stderr log truncated at %d bytes", maxLogBytes))
	}

	adapter, adapterErr := HarnessFor(record.Resolved.Harness)
	if adapterErr != nil {
		return finishHarness(failure("resolve the contained harness adapter", adapterErr))
	}
	summary := summarizeLogFile(store.LogPath(agentID), adapter)
	record.Usage = legacyUsage(summary.Tokens)
	record.ToolCalls = summary.ToolCalls
	record.HarnessEvents = append(record.HarnessEvents, summary.Diagnostics...)
	record.Telemetry = buildTelemetry(record.Resolved.Harness, summary)
	record.Cost = buildCost(record.Resolved.Harness, summary, deps.Now())
	if message := finalMessage(summary, store.LastMessagePath(agentID)); message != "" {
		redacted, count := RedactText(message, credential.Value)
		record.Result = BoundResult(redacted)
		if count > 0 {
			record.HarnessEvents = append(record.HarnessEvents, "the final message contained the provider credential and was redacted")
		}
	}

	if outcome, ok := containedCancellationOutcome(ctx, store, agentID); ok {
		return finishHarness(outcome)
	}
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		code := exitCodeOf(command)
		record.ExitCode = code
		return finishHarness(containedTimedOutResult("run exceeded its timeout; the contained namespace was removed before this result was recorded"))
	case waitErr != nil:
		code := exitCodeOf(command)
		return finishHarness(containedResult{
			State: StateFailed, ExecutionStatus: StatusFailed,
			Failure: failureMessage(summary, waitErr), Category: classifyFailure(summary), ExitCode: code,
			Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the harness did not complete"},
		})
	case summary.TurnFailed:
		code := 0
		return finishHarness(containedResult{
			State: StateFailed, ExecutionStatus: StatusFailed,
			Failure: "harness exited zero but reported a failed turn", Category: classifyFailure(summary), ExitCode: &code,
			Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the harness reported a failed turn"},
		})
	case summary.MalformedLines > 0:
		code := exitCodeOf(command)
		return finishHarness(containedResult{
			State: StateFailed, ExecutionStatus: StatusFailed,
			Failure: fmt.Sprintf("harness produced no terminal turn event and %d unparsable event line(s); the run is not a success", summary.MalformedLines), Category: FailureHarness, ExitCode: code,
			Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the harness stream was unparsable"},
		})
	case !summary.TurnCompleted:
		code := exitCodeOf(command)
		return finishHarness(containedResult{
			State: StateFailed, ExecutionStatus: StatusFailed,
			Failure: "harness produced no terminal turn event; the run is not a success", Category: FailureHarness, ExitCode: code,
			Validation: ValidationRecord{Status: ValidationSkipped, Reason: "the harness produced no completion evidence"},
		})
	}

	code := exitCodeOf(command)
	validation := runContainedValidation(ctx, store, agentID, record, deps, runtime.ValidationTransport(), save)
	if validation.result.saveErr != nil {
		return finishHarness(validation.result)
	}
	if outcome, ok := containedCancellationOutcome(ctx, store, agentID); ok {
		return finishHarness(outcome)
	}
	if validation.record.Status != ValidationPassed && !(validation.skippedWithoutSteps && validation.record.Status == ValidationSkipped) {
		return finishHarness(containedResult{
			State: StateFailed, ExecutionStatus: StatusFailed,
			Failure: "objective validation did not pass", Category: FailureValidation, ExitCode: code,
			Validation: validation.record,
		})
	}
	return finishHarness(containedResult{
		State: StateCompleted, ExecutionStatus: StatusCompleted, ExitCode: code, Validation: validation.record,
	})
}

type containedValidation struct {
	record ValidationRecord
	// skippedWithoutSteps is true only when the record snapshotted zero
	// validation steps, so the Skipped status is the legacy "nothing to run"
	// outcome instead of a configured check that did not pass.
	skippedWithoutSteps bool
	result              containedResult
}

// runContainedValidation uses a separate finite budget derived from the
// snapshotted steps. It is cancel-aware because it is derived from the owner's
// context (never context.WithoutCancel) and it targets the same container.
func runContainedValidation(ctx context.Context, store Store, agentID string, record *Record, deps OwnerDeps, transport ValidationCommandFactory, save func(Record) error) containedValidation {
	steps := validationStepsFromRecord(*record)
	if len(steps) == 0 {
		return containedValidation{
			record:              ValidationRecord{Status: ValidationSkipped, Reason: "no validation steps were configured"},
			skippedWithoutSteps: true,
		}
	}
	startedAt := deps.Now().UTC()
	record.Validation = ValidationRecord{Status: ValidationRunning, Steps: record.Validation.Steps, StartedAt: &startedAt}
	if err := save(*record); err != nil {
		return containedValidation{result: containedResult{saveErr: err}}
	}
	validationCtx := ctx
	budget := containedValidationBudget(steps)
	if budget > 0 {
		var cancel context.CancelFunc
		validationCtx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}
	validation := deps.Validation(validationCtx, ValidationOptions{
		WorktreeDir: record.WorktreeDir,
		RunDir:      store.Dir(agentID),
		Steps:       steps,
		Now:         deps.Now,
		Transport:   transport,
		// A timed-out contained step's Docker client may return while the exec'd
		// process is still winding down inside the namespace. Stop after the
		// first failed step so no later step starts concurrently, then let the
		// owner close the whole namespace before any host Git read.
		FailFast: true,
	})
	return containedValidation{record: validation}
}

// containedValidationBudget is the finite budget the snapshotted validation
// steps may consume. It uses the same defaults as the runner.
func containedValidationBudget(steps []ValidationStep) time.Duration {
	var budget time.Duration
	for _, step := range steps {
		timeout, err := parseStepTimeout(step.Timeout)
		if err != nil || timeout <= 0 {
			timeout = defaultValidationStepTimeout
		}
		budget += timeout
	}
	return budget
}

// containedCancellationOutcome reports the sentinel/context outcome when one is
// already decisive. When the caller's finite lifetime (not an operator stop)
// has expired, it reports a timeout instead.
func containedCancellationOutcome(ctx context.Context, store Store, agentID string) (containedResult, bool) {
	if cancelRequested(store, agentID) || errors.Is(ctx.Err(), context.Canceled) {
		return containedCancelledResult(), true
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return containedTimedOutResult("the contained execution exceeded its finite lifetime"), true
	}
	return containedResult{}, false
}

// stopCommand stops a started worker client after a save failure, without
// assuming the command supports process-group cancellation.
func stopCommand(command *exec.Cmd) {
	if command == nil {
		return
	}
	if command.Cancel != nil {
		_ = command.Cancel()
		return
	}
	if command.Process != nil {
		_ = command.Process.Kill()
	}
}

// monitorCancellation watches the durable cancellation sentinel and cancels the
// owner context as soon as it appears. It always stops its goroutine through
// the returned function, so no watcher outlives the execution.
func monitorCancellation(ctx context.Context, store Store, agentID string, interval time.Duration) (context.Context, func()) {
	if interval <= 0 {
		interval = defaultCancelPollInterval
	}
	monitorCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if cancelRequested(store, agentID) {
				cancel()
				return
			}
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return monitorCtx, func() {
		cancel()
		<-done
	}
}

// runContainedDshVersion reads the real version from the verified container.
func runContainedDshVersion(ctx context.Context, runtime containedRuntime) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, containerPreflightTimeout)
	defer cancel()
	output, err := runContainedBoundedCommand(probeCtx, runtime, []string{"dsh", "--version"}, maxContainerDshVersionBytes)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(output))
	if index := strings.IndexAny(line, "\r\n"); index >= 0 {
		line = strings.TrimSpace(line[:index])
	}
	if line == "" {
		return "", errors.New("dsh --version reported no version")
	}
	if len(line) > 120 {
		line = line[:120]
	}
	return line, nil
}

// runContainedDshDump composes the exact launch configuration in the same
// container, without any credential in its environment.
func runContainedDshDump(ctx context.Context, runtime containedRuntime) ([]byte, error) {
	probeCtx, cancel := context.WithTimeout(ctx, containerPreflightTimeout)
	defer cancel()
	return runContainedBoundedCommand(probeCtx, runtime, []string{
		"dsh", "--profile", dshProfile, "--patch", containedDshPatchTarget, "--dump-config",
	}, maxContainerDshDumpBytes)
}

func runContainedBoundedCommand(ctx context.Context, runtime containedRuntime, argv []string, limit int) ([]byte, error) {
	command, err := runtime.WorkerCommand(ctx, Credential{}, argv)
	if err != nil {
		return nil, err
	}
	stdout := &boundedContainerOutput{limit: limit}
	stderr := &boundedContainerOutput{limit: maxContainerDiagnosticBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if stdout.exceeded || stderr.exceeded {
			return nil, fmt.Errorf("%s output is above its bound", strings.Join(argv, " "))
		}
		detail := strings.TrimSpace(stderr.buffer.String())
		if detail != "" {
			return nil, fmt.Errorf("%s failed: %v: %s", strings.Join(argv, " "), err, detail)
		}
		return nil, fmt.Errorf("%s failed: %w", strings.Join(argv, " "), err)
	}
	if stdout.exceeded {
		return nil, fmt.Errorf("%s output is above its bound", strings.Join(argv, " "))
	}
	return stdout.buffer.Bytes(), nil
}

// captureContainedHandoff records the host Git change summary and diff of a
// contained execution that runtime.Close has already proved absent. It is the
// only place the contained path reads host Git state, so a snapshot is never
// taken while a workload may still be writing the mounted worktree. The read
// context is independent of a cancelled owner (the artifact must still describe
// what happened) but always finite.
func captureContainedHandoff(ctx context.Context, store Store, agentID string, record *Record, deps OwnerDeps) {
	metadataCtx, cancel := containedGitMetadataContext(ctx, deps)
	defer cancel()
	record.Changes = SummarizeChangesWith(metadataCtx, record.WorktreeDir, record.BaseSHA, record.DirtyFiles)
	if path, err := store.WriteDiff(metadataCtx, agentID, record.WorktreeDir, record.BaseSHA, DefaultTargetDeps()); err == nil {
		record.DiffPath = path
	}
}

// containedGitMetadataContext supplies the post-run Git metadata snapshots a
// context that is independent of the owner's cancellation yet always finite.
// The optional observer is a focused test seam so a test can prove the real
// supplied context carries a deadline; it never changes production behaviour.
func containedGitMetadataContext(ctx context.Context, deps OwnerDeps) (context.Context, context.CancelFunc) {
	metadataCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), containedGitMetadataTimeout)
	if deps.observeGitMetadataContext != nil {
		deps.observeGitMetadataContext(metadataCtx)
	}
	return metadataCtx, cancel
}

// containedDshEffectiveBaseURL is the endpoint the prepared overlay pins.
func containedDshEffectiveBaseURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return containedDshDefaultBaseURL
	}
	return raw
}

// verifyContainedDshDump parses the composed configuration and requires the
// exact model/provider, provider routing and bounded Flash defaults the launch
// overlay asked for. A missing, different or wrongly typed entry fails closed
// instead of trusting prose or the requested values.
func verifyContainedDshDump(raw []byte, model, credentialEnvName, baseURL string) error {
	if len(raw) > maxContainerDshDumpBytes {
		return fmt.Errorf("dsh --dump-config output is %d bytes, above the %d-byte bound", len(raw), maxContainerDshDumpBytes)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("parse the composed dsh configuration: %w", err)
	}
	entries := map[string]map[string]*yaml.Node{}
	collectContainedDshEntries(&document, entries)
	modelEntry, ok := entries["agent-default-model"]
	if !ok {
		return errors.New("the composed dsh configuration has no agent-default-model entry")
	}
	if got := containedDshScalar(modelEntry["provider"]); got != ProviderDshDefault {
		return fmt.Errorf("the composed dsh default provider is %q, not %q", got, ProviderDshDefault)
	}
	if got := containedDshScalar(modelEntry["model"]); got != model {
		return fmt.Errorf("the composed dsh default model is %q, not the resolved model %q", got, model)
	}
	routingEntry, ok := entries["llm-deepseek"]
	if !ok {
		return errors.New("the composed dsh configuration has no llm-deepseek entry")
	}
	if got := containedDshScalar(routingEntry["apiKeyEnv"]); got != credentialEnvName {
		return fmt.Errorf("the composed dsh apiKeyEnv is %q, not the injected credential name %q", got, credentialEnvName)
	}
	gotBase := strings.TrimRight(containedDshScalar(routingEntry["baseURL"]), "/")
	if gotBase != strings.TrimRight(baseURL, "/") {
		return fmt.Errorf("the composed dsh baseURL is %q, not the pinned official endpoint %q", containedDshScalar(routingEntry["baseURL"]), baseURL)
	}
	// The overlay pins bounded public Flash defaults; proving the composed
	// configuration means proving them too, not only provider routing.
	if got := containedDshScalar(routingEntry["reasoningEffort"]); got != containedDshReasoningEffort {
		return fmt.Errorf("the composed dsh reasoningEffort is %q, not the pinned %q", got, containedDshReasoningEffort)
	}
	if got := containedDshScalar(routingEntry["thinking"]); got != containedDshThinking {
		return fmt.Errorf("the composed dsh thinking mode is %q, not the pinned %q", got, containedDshThinking)
	}
	maxTokens, ok := containedDshInt(routingEntry["maxTokens"])
	if !ok {
		return fmt.Errorf("the composed dsh maxTokens is %q, not the pinned integer %d", containedDshScalar(routingEntry["maxTokens"]), containedDshMaxTokens)
	}
	if maxTokens != containedDshMaxTokens {
		return fmt.Errorf("the composed dsh maxTokens is %d, not the pinned output bound %d", maxTokens, containedDshMaxTokens)
	}
	return nil
}

// containedDshScalar reads one scalar leaf. A missing node, a mapping, a
// sequence or an explicitly null value all read as the empty string, so a
// required value is never satisfied by the wrong shape.
func containedDshScalar(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag == "!!null" {
		return ""
	}
	return strings.TrimSpace(node.Value)
}

// containedDshInt reads one integer leaf. It requires an integer-shaped scalar
// so a quoted or otherwise non-integer maxTokens cannot pass as the pinned
// bound.
func containedDshInt(node *yaml.Node) (int, bool) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return 0, false
	}
	value, err := strconv.Atoi(strings.TrimSpace(node.Value))
	if err != nil {
		return 0, false
	}
	return value, true
}

func collectContainedDshEntries(node *yaml.Node, entries map[string]map[string]*yaml.Node) {
	if node == nil {
		return
	}
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range node.Content {
			collectContainedDshEntries(child, entries)
		}
	case yaml.MappingNode:
		values := map[string]*yaml.Node{}
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index].Value
			value := node.Content[index+1]
			values[key] = value
			collectContainedDshEntries(value, entries)
		}
		id := containedDshScalar(values["id"])
		config, ok := values["config"]
		if id == "" || !ok || config.Kind != yaml.MappingNode {
			return
		}
		configValues := map[string]*yaml.Node{}
		for index := 0; index+1 < len(config.Content); index += 2 {
			configValues[config.Content[index].Value] = config.Content[index+1]
		}
		entries[id] = configValues
	}
}
