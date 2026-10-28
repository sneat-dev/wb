package agents

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/session"
)

// DirName is the directory under WB's home that holds dispatched agent runs.
const DirName = "agents"

// IDPrefix begins every agent run ID. The shape follows WB's existing random
// session-ID convention rather than a timestamp, so an ID never encodes — and
// therefore never leaks — when or where the work happened.
const IDPrefix = "agt-"

const (
	recordFileName      = "run.json"
	logFileName         = "events.jsonl"
	lastMessageFileName = "last-message.txt"
	stderrFileName      = "stderr.log"
	harnessHomeDirName  = "harness-home"
	schemaVersion       = 1
	// maxResultBytes bounds the worker's own final message, which is the one
	// free-text artefact from the transcript that may travel back to a caller.
	maxResultBytes = 4096
	// maxChangedFiles bounds the changed-file list so a sweeping diff cannot
	// turn a result document into a transcript.
	maxChangedFiles = 50
	// admissionGrace is how long a running record may show no process at all
	// before WB calls it abandoned. It covers only the interval between the
	// dispatcher persisting the run and the owner recording itself, which is
	// milliseconds; it can never mislabel a slow worker, because a slow worker
	// still has a live owner.
	admissionGrace = time.Minute
)

// processAlive answers the one liveness question this package asks, through the
// single WB-wide implementation so every platform agrees.
func processAlive(pid int) bool { return session.ProcessAlive(pid) }

// State is the closed lifecycle vocabulary of one dispatched run. It is the
// legacy spelling kept for records an older WB wrote and read; new records also
// carry ExecutionStatus, which distinguishes cancellation from failure without
// adding a value an older build would not recognise as terminal.
type State string

const (
	StateRunning   State = "running"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateTimeout   State = "timeout"
	StateAbandoned State = "abandoned"
)

// ExecutionStatus is the richer, additive status of one execution. It never
// replaces State: an older build must still be able to list and await a record
// this build wrote.
type ExecutionStatus string

const (
	StatusQueued    ExecutionStatus = "queued"
	StatusRunning   ExecutionStatus = "running"
	StatusCompleted ExecutionStatus = "completed"
	StatusFailed    ExecutionStatus = "failed"
	StatusCancelled ExecutionStatus = "cancelled"
	StatusTimedOut  ExecutionStatus = "timed_out"
	// StatusAbandoned is derived at read time when the owner process vanished
	// without recording a terminal status. It is never stored.
	StatusAbandoned ExecutionStatus = "abandoned"
)

// Failure categories are a closed vocabulary: an orchestration decision can
// branch on the category without parsing a message.
const (
	FailureUsage        = "usage"
	FailureConfig       = "config"
	FailureCredential   = "credential"
	FailureProvider     = "provider"
	FailureHarnessMiss  = "harness_missing"
	FailureHarness      = "harness_failed"
	FailureValidation   = "validation_failed"
	FailureTimeout      = "timeout"
	FailureCancelled    = "cancelled"
	FailureBaseMismatch = "base_mismatch"
	FailureWorktree     = "worktree"
	FailureInternal     = "internal"
)

// Validation statuses. Validation is its own axis: a process can exit zero and
// still fail the objective checks.
const (
	ValidationPending = "pending"
	ValidationRunning = "running"
	ValidationPassed  = "passed"
	ValidationFailed  = "failed"
	ValidationSkipped = "skipped"
)

// Review statuses. Review is the third axis and is written only by an explicit
// review command.
const (
	ReviewPending     = "pending"
	ReviewAccepted    = "accepted"
	ReviewRejected    = "rejected"
	ReviewNeedsChange = "needs_changes"
)

// Terminal reports whether a state is final. Exactly one state — running — is
// not terminal, so a caller can never mistake a stalled run for a finished one.
func (state State) Terminal() bool {
	switch state {
	case StateCompleted, StateFailed, StateTimeout, StateAbandoned:
		return true
	default:
		return false
	}
}

// Terminal reports whether an execution status is final.
func (status ExecutionStatus) Terminal() bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusTimedOut, StatusAbandoned:
		return true
	default:
		return false
	}
}

// Usage is what the harness reported about the run. Every field is copied
// verbatim from the harness; a field the harness did not report stays absent
// rather than being estimated.
type Usage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens,omitempty"`
	CacheWriteInputTokens int `json:"cache_write_input_tokens,omitempty"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens,omitempty"`
}

// ChangeSummary is a cheap, deterministic description of what the worker left
// behind in its worktree.
type ChangeSummary struct {
	FilesChanged int      `json:"files_changed"`
	Files        []string `json:"files,omitempty"`
	Insertions   int      `json:"insertions,omitempty"`
	Deletions    int      `json:"deletions,omitempty"`
	Commits      int      `json:"commits,omitempty"`
	Truncated    bool     `json:"files_truncated,omitempty"`
	// Untracked lists paths Git does not include in a diff, so a reader knows
	// the patch is incomplete rather than assuming a small change.
	Untracked []string `json:"untracked,omitempty"`
}

// Telemetry is what the harness reported about usage, with its provenance.
// Available is explicit: a nil Tokens means "not reported", never zero.
type Telemetry struct {
	Available         bool            `json:"available"`
	Source            string          `json:"source"`
	UnavailableReason string          `json:"unavailable_reason,omitempty"`
	Tokens            *Tokens         `json:"tokens,omitempty"`
	Raw               json.RawMessage `json:"raw,omitempty"`
}

// CostRecord is an API-equivalent estimate, never a billed amount. A nil USD
// means no per-run price was observed.
type CostRecord struct {
	USD            *float64   `json:"usd,omitempty"`
	Kind           string     `json:"kind"`
	Source         string     `json:"source"`
	PricingVersion string     `json:"pricing_version,omitempty"`
	EstimatedAt    *time.Time `json:"estimated_at,omitempty"`
	Note           string     `json:"note,omitempty"`
}

// CallerSession identifies the session that invoked WB to dispatch this run.
// The native ID wins when a harness exposes one: several Codex chats can share
// one app-server PID and therefore one PID-based WB registration.
type CallerSession struct {
	ID              string `json:"session_id,omitempty"`
	WBSessionID     string `json:"wb_session_id,omitempty"`
	NativeHarnessID string `json:"native_harness_id,omitempty"`
	Name            string `json:"name,omitempty"`
	Runtime         string `json:"runtime,omitempty"`
	Source          string `json:"source,omitempty"`
}

// ValidationStepResult is one executed objective check.
type ValidationStepResult struct {
	Name       string   `json:"name"`
	Argv       []string `json:"argv"`
	Timeout    string   `json:"timeout,omitempty"`
	Status     string   `json:"status"`
	ExitCode   *int     `json:"exit_code,omitempty"`
	DurationMS int64    `json:"duration_ms,omitempty"`
	LogPath    string   `json:"log_path,omitempty"`
}

// ValidationRecord is the objective-validation axis of one execution.
type ValidationRecord struct {
	Status     string                 `json:"status"`
	Steps      []ValidationStepResult `json:"steps,omitempty"`
	Reason     string                 `json:"reason,omitempty"`
	StartedAt  *time.Time             `json:"started_at,omitempty"`
	FinishedAt *time.Time             `json:"finished_at,omitempty"`
}

// Record is the durable description of one dispatched run. It is private: it
// holds the original task, which never travels back out through status or
// await.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	AgentID       string `json:"agent_id"`
	State         State  `json:"state"`
	// ExecutionStatus is the additive status; empty on a record written before
	// this field existed, in which case it is derived from State.
	ExecutionStatus ExecutionStatus `json:"execution_status,omitempty"`
	FailureCategory string          `json:"failure_category,omitempty"`

	// RequestedProfile is what the caller asked for. Resolved is what actually
	// ran; keeping both is what makes a later profile edit harmless.
	RequestedProfile string   `json:"requested_profile"`
	Resolved         Resolved `json:"resolved"`

	// Routing provenance: every resolved value says where it came from.
	TaskType       string `json:"task_type,omitempty"`
	TaskTypeSource string `json:"task_type_source,omitempty"`
	ProfileSource  string `json:"profile_source,omitempty"`
	RoutingReason  string `json:"routing_reason,omitempty"`

	Complexity           string `json:"complexity,omitempty"`
	ComplexitySource     string `json:"complexity_source,omitempty"`
	ComplexityOverridden bool   `json:"complexity_overridden,omitempty"`

	EffortRequested string `json:"effort_requested,omitempty"`
	EffortSource    string `json:"effort_source,omitempty"`
	EffortEffective string `json:"effort_effective,omitempty"`
	EffortApplied   bool   `json:"effort_applied,omitempty"`
	EffortNote      string `json:"effort_note,omitempty"`

	WorktreeSource string `json:"worktree_source,omitempty"`
	TimeoutSource  string `json:"timeout_source,omitempty"`

	HarnessVersion      string              `json:"harness_version,omitempty"`
	HarnessCapabilities HarnessCapabilities `json:"harness_capabilities"`

	// Execution is the mandatory execution policy snapshotted at admission, so
	// a later edit to the user's configuration cannot rewrite what this run was
	// required to respect. Absent when the operator declared no sandbox.
	Execution *ExecutionPolicy `json:"execution,omitempty"`

	// Task is the exact originating request. It is private: it is never
	// rendered by status or await, only persisted here.
	Task string `json:"task"`
	// TaskSummary is a bounded, single-line form of Task for a human reading
	// the record. It is derived from the task, so it is private for exactly the
	// same reason Task is, and is likewise never rendered.
	TaskSummary string `json:"task_summary,omitempty"`
	// TaskSHA256 is the identity of the exact task bytes, so two executions can
	// be proven comparable without republishing the task.
	TaskSHA256 string `json:"task_sha256,omitempty"`
	TaskBytes  int    `json:"task_bytes,omitempty"`

	Repository   string        `json:"repository"`
	Caller       CallerSession `json:"caller_session,omitzero"`
	WorktreeMode string        `json:"worktree_mode"`
	Worktree     string        `json:"worktree"`
	WorktreeDir  string        `json:"worktree_dir,omitempty"`
	Branch       string        `json:"branch,omitempty"`
	Base         string        `json:"base,omitempty"`
	BaseSHA      string        `json:"base_sha,omitempty"`

	// SourceSHA is the commit the writable checkout was at when the run
	// started, and SnapshotSHA the stash commit a dirty checkout produced, so a
	// later diff separates the worker's change from what was already there.
	SourceSHA   string   `json:"source_sha,omitempty"`
	SnapshotSHA string   `json:"snapshot_sha,omitempty"`
	SourceDirty bool     `json:"source_dirty,omitempty"`
	DirtyFiles  []string `json:"source_dirty_files,omitempty"`

	// WorkLogClaimPath links this run to the immutable Work Log claim the
	// worktree service already publishes, so one dispatch never produces two
	// unreferenced records of the same piece of work.
	WorkLogClaimPath string `json:"work_log_claim_path,omitempty"`
	WorkLogRunID     string `json:"work_log_run_id,omitempty"`

	// LeasePath is the retained writer-lease file this run holds, if any.
	LeasePath string `json:"lease_path,omitempty"`

	// ExperimentID and RepeatIndex place this execution in a comparison. Both
	// are empty for a standalone run.
	ExperimentID string `json:"experiment_id,omitempty"`
	RepeatIndex  int    `json:"repeat_index,omitempty"`

	// RetryOf is the execution this one replaces after an infrastructure
	// failover, and FallbackFrom/FallbackReason say which profile failed and why.
	// A retry is a new execution with its own worktree, never a resumed one.
	RetryOf        string `json:"retry_of,omitempty"`
	FallbackFrom   string `json:"fallback_from,omitempty"`
	FallbackReason string `json:"fallback_reason,omitempty"`

	// TimeoutMS bounds the run. It is persisted because the owner, not the
	// dispatcher, enforces it.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`

	// StartedAt is when the run was admitted. ExecutionStartedAt is when the
	// harness itself started, so queue time never inflates wall time.
	StartedAt          time.Time `json:"started_at"`
	ExecutionStartedAt time.Time `json:"execution_started_at,omitempty"`
	FinishedAt         time.Time `json:"finished_at,omitempty"`
	DurationMS         int64     `json:"duration_ms,omitempty"`
	// QueueDurationMS is how long an execution waited behind a concurrency
	// bound before its harness started.
	QueueDurationMS int64 `json:"queue_duration_ms,omitempty"`
	// TimeToFirstOutputMS is how long the harness ran before its first byte.
	TimeToFirstOutputMS int64 `json:"time_to_first_output_ms,omitempty"`

	// QueueOwnerPID is the dispatching process while a run is queued behind a
	// concurrency bound. It is the only liveness evidence a queued run has.
	QueueOwnerPID int            `json:"queue_owner_pid,omitempty"`
	OwnerPID      int            `json:"owner_pid,omitempty"`
	WorkerPID     int            `json:"worker_pid,omitempty"`
	ExitCode      *int           `json:"exit_code,omitempty"`
	Failure       string         `json:"failure,omitempty"`
	Result        string         `json:"result,omitempty"`
	ToolCalls     int            `json:"tool_calls,omitempty"`
	Usage         *Usage         `json:"usage,omitempty"`
	Changes       *ChangeSummary `json:"changes,omitempty"`
	HarnessEvents []string       `json:"harness_diagnostics,omitempty"`

	// Telemetry and Cost carry the provenance the legacy Usage cannot express.
	Telemetry  Telemetry        `json:"telemetry"`
	Cost       CostRecord       `json:"cost"`
	Validation ValidationRecord `json:"validation"`

	// DiffPath is the private patch artefact for this run, when one was written.
	DiffPath string `json:"diff_path,omitempty"`

	// AcceptanceRequired records the task type's policy at dispatch time, so a
	// later configuration change cannot make an unreviewed run look complete.
	AcceptanceRequired bool `json:"acceptance_required,omitempty"`
	// Retries counts automatic retries attempted. This build performs none.
	Retries int `json:"retries"`

	// TaskVisibility states how the task reached the harness. `stdin` keeps it
	// off the process table; `argv` does not.
	TaskVisibility string `json:"task_visibility,omitempty"`
	TaskNormalized bool   `json:"task_normalized,omitempty"`

	LogPath string `json:"log_path,omitempty"`
}

// Result is the rendered projection of a Record, and the machine-readable
// document `status` and `await` emit. It is written out field by field rather
// than derived by embedding, so the private task can never reach a caller by
// accident: adding a field to Record does not add it here.
type Result struct {
	AgentID string `json:"agent_id"`
	// Machine names the machine that holds this run's record and worktree. It
	// is empty for a run dispatched by this machine and is stamped by the caller
	// that asked another machine, so a result always says where its artefact
	// lives.
	Machine             string              `json:"machine,omitempty"`
	State               State               `json:"state"`
	Status              ExecutionStatus     `json:"execution_status"`
	Terminal            bool                `json:"terminal"`
	FailureCategory     string              `json:"failure_category,omitempty"`
	RequestedProfile    string              `json:"profile"`
	Resolved            Resolved            `json:"resolved"`
	TaskType            string              `json:"task_type,omitempty"`
	TaskTypeSource      string              `json:"task_type_source,omitempty"`
	ProfileSource       string              `json:"profile_source,omitempty"`
	Complexity          string              `json:"complexity,omitempty"`
	ComplexitySource    string              `json:"complexity_source,omitempty"`
	EffortRequested     string              `json:"effort_requested,omitempty"`
	EffortEffective     string              `json:"effort_effective,omitempty"`
	EffortApplied       bool                `json:"effort_applied"`
	EffortNote          string              `json:"effort_note,omitempty"`
	WorktreeSource      string              `json:"worktree_source,omitempty"`
	TimeoutSource       string              `json:"timeout_source,omitempty"`
	HarnessVersion      string              `json:"harness_version,omitempty"`
	HarnessCapabilities HarnessCapabilities `json:"harness_capabilities"`
	Execution           *ExecutionPolicy    `json:"execution,omitempty"`
	Repository          string              `json:"repository"`
	Caller              CallerSession       `json:"caller_session,omitzero"`
	WorktreeMode        string              `json:"worktree_mode"`
	Worktree            string              `json:"worktree"`
	WorktreeDir         string              `json:"worktree_dir,omitempty"`
	Branch              string              `json:"branch,omitempty"`
	Base                string              `json:"base,omitempty"`
	BaseSHA             string              `json:"base_sha,omitempty"`
	SourceSHA           string              `json:"source_sha,omitempty"`
	SnapshotSHA         string              `json:"snapshot_sha,omitempty"`
	SourceDirty         bool                `json:"source_dirty,omitempty"`
	ExperimentID        string              `json:"experiment_id,omitempty"`
	RepeatIndex         int                 `json:"repeat_index,omitempty"`
	RetryOf             string              `json:"retry_of,omitempty"`
	FallbackFrom        string              `json:"fallback_from,omitempty"`
	FallbackReason      string              `json:"fallback_reason,omitempty"`
	StartedAt           time.Time           `json:"started_at"`
	ExecutionStartedAt  *time.Time          `json:"execution_started_at,omitempty"`
	FinishedAt          *time.Time          `json:"finished_at,omitempty"`
	DurationMS          int64               `json:"duration_ms,omitempty"`
	QueueDurationMS     int64               `json:"queue_duration_ms,omitempty"`
	TimeToFirstOutputMS int64               `json:"time_to_first_output_ms,omitempty"`
	QueueOwnerPID       int                 `json:"queue_owner_pid,omitempty"`
	OwnerPID            int                 `json:"owner_pid,omitempty"`
	WorkerPID           int                 `json:"worker_pid,omitempty"`
	OwnerAlive          bool                `json:"owner_alive"`
	WorkerAlive         bool                `json:"worker_alive"`
	ExitCode            *int                `json:"exit_code,omitempty"`
	Failure             string              `json:"failure,omitempty"`
	Result              string              `json:"result,omitempty"`
	ToolCalls           int                 `json:"tool_calls,omitempty"`
	Usage               *Usage              `json:"usage,omitempty"`
	Telemetry           Telemetry           `json:"telemetry"`
	Cost                CostRecord          `json:"cost"`
	Validation          ValidationRecord    `json:"validation"`
	Review              ReviewRecord        `json:"review"`
	Changes             *ChangeSummary      `json:"changes,omitempty"`
	HarnessDiagnostics  []string            `json:"harness_diagnostics,omitempty"`
	DiffPath            string              `json:"diff_path,omitempty"`
	AcceptanceRequired  bool                `json:"acceptance_required"`
	Retries             int                 `json:"retries"`
	TaskVisibility      string              `json:"task_visibility,omitempty"`
	TaskNormalized      bool                `json:"task_normalized,omitempty"`
	LogPath             string              `json:"log_path,omitempty"`
}

// NormalizeState makes this process's closed state vocabulary authoritative for
// a result that arrived from somewhere else. A remote answer carries a state;
// whether that state is terminal is WB's rule, so it is derived here rather
// than trusted from the wire, where a version skew could disagree.
func NormalizeState(result *Result) {
	if result == nil {
		return
	}
	result.Terminal = result.State.Terminal()
	if result.Status == "" {
		result.Status = executionStatusFromState(result.State)
	}
}

// executionStatusFromState derives the additive status for a record written
// before ExecutionStatus existed.
func executionStatusFromState(state State) ExecutionStatus {
	switch state {
	case StateCompleted:
		return StatusCompleted
	case StateFailed:
		return StatusFailed
	case StateTimeout:
		return StatusTimedOut
	case StateAbandoned:
		return StatusAbandoned
	default:
		return StatusRunning
	}
}

// Render projects a record for output, resolving a run whose owner vanished
// without recording a terminal state to the honest answer: abandoned. A run is
// never reported as completed merely because nothing contradicted it.
//
// Abandoned is derived from the owner, not the worker: the owner is the only
// process that will ever record a terminal state, so once it is gone the run
// has no outcome and reporting it "running" indefinitely would be a lie. A
// worker that outlived its owner is still surfaced, through WorkerAlive, so a
// caller knows a stray process exists and can stop it.
func (store Store) Render(record Record) Result {
	state := record.State
	status := record.ExecutionStatus
	if status == "" {
		status = executionStatusFromState(state)
	}
	ownerAlive := processAlive(record.OwnerPID)
	workerAlive := processAlive(record.WorkerPID)
	switch status {
	case StatusQueued:
		// A prepared attempt waits behind a concurrency bound with nothing
		// running. Its dispatcher is the only thing that can still start it, so
		// its liveness decides whether the attempt is pending or abandoned. The
		// grace window covers a single run whose dispatcher exits just before the
		// owner records itself.
		if !processAlive(record.QueueOwnerPID) && time.Since(record.StartedAt) >= admissionGrace {
			state = StateAbandoned
			status = StatusAbandoned
		}
	case StatusRunning:
		switch {
		case ownerAlive:
			// The run is genuinely in progress.
		case record.OwnerPID == 0 && record.WorkerPID == 0 && time.Since(record.StartedAt) < admissionGrace:
			// Admission is still in flight: the run is persisted and the owner
			// has not yet recorded itself. This window is milliseconds wide.
		default:
			state = StateAbandoned
			status = StatusAbandoned
		}
	}
	result := Result{
		AgentID: record.AgentID, State: state, Status: status, Terminal: status.Terminal(),
		FailureCategory:  record.FailureCategory,
		RequestedProfile: record.RequestedProfile, Resolved: record.Resolved,
		TaskType: record.TaskType, TaskTypeSource: record.TaskTypeSource,
		ProfileSource: record.ProfileSource, Complexity: record.Complexity,
		ComplexitySource: record.ComplexitySource,
		EffortRequested:  record.EffortRequested, EffortEffective: record.EffortEffective,
		EffortApplied: record.EffortApplied, EffortNote: record.EffortNote,
		WorktreeSource: record.WorktreeSource, TimeoutSource: record.TimeoutSource,
		HarnessVersion: record.HarnessVersion, HarnessCapabilities: record.HarnessCapabilities,
		Execution:  SnapshotExecution(record.Execution),
		Repository: record.Repository, Caller: record.Caller,
		WorktreeMode: record.WorktreeMode, Worktree: record.Worktree,
		WorktreeDir: record.WorktreeDir, Branch: record.Branch,
		Base: record.Base, BaseSHA: record.BaseSHA,
		SourceSHA: record.SourceSHA, SnapshotSHA: record.SnapshotSHA, SourceDirty: record.SourceDirty,
		ExperimentID: record.ExperimentID, RepeatIndex: record.RepeatIndex,
		RetryOf: record.RetryOf, FallbackFrom: record.FallbackFrom, FallbackReason: record.FallbackReason,
		StartedAt: record.StartedAt, DurationMS: record.DurationMS,
		QueueDurationMS: record.QueueDurationMS, TimeToFirstOutputMS: record.TimeToFirstOutputMS,
		QueueOwnerPID: record.QueueOwnerPID,
		OwnerPID:      record.OwnerPID, WorkerPID: record.WorkerPID,
		OwnerAlive: ownerAlive, WorkerAlive: workerAlive,
		ExitCode: record.ExitCode, Failure: record.Failure, Result: record.Result,
		ToolCalls: record.ToolCalls, Usage: record.Usage, Changes: record.Changes,
		Telemetry: record.Telemetry, Cost: record.Cost, Validation: record.Validation,
		HarnessDiagnostics: record.HarnessEvents, DiffPath: record.DiffPath,
		AcceptanceRequired: record.AcceptanceRequired, Retries: record.Retries,
		TaskVisibility: record.TaskVisibility, TaskNormalized: record.TaskNormalized,
		LogPath: record.LogPath,
	}
	if !record.ExecutionStartedAt.IsZero() {
		started := record.ExecutionStartedAt
		result.ExecutionStartedAt = &started
	}
	if !record.FinishedAt.IsZero() {
		finished := record.FinishedAt
		result.FinishedAt = &finished
	}
	// The review sidecar is best-effort: a run whose review cannot be read is
	// still a truthful execution result. An absent sidecar means "not reviewed".
	if review, err := store.LoadReview(record.AgentID); err == nil {
		result.Review = review.Latest()
	} else {
		result.Review = ReviewRecord{Status: ReviewPending}
	}
	return result
}

// NewID mints a fresh agent run ID.
func NewID() (string, error) {
	var random [16]byte
	// Go 1.27 crypto/rand.Read fills the buffer or terminates the process;
	// it never returns an error. Keep this public signature for callers.
	_, _ = rand.Read(random[:])
	return IDPrefix + hex.EncodeToString(random[:]), nil
}

// Store persists agent run records under one root directory.
type Store struct {
	Root string
}

// NewStore returns the store for a WB home directory.
func NewStore(home string) Store {
	return Store{Root: filepath.Join(home, DirName)}
}

// Dir is the private directory holding one run's record and artefacts.
func (store Store) Dir(agentID string) string {
	return filepath.Join(store.Root, agentID)
}

// RecordPath is the persisted run record for one agent ID.
func (store Store) RecordPath(agentID string) string {
	return filepath.Join(store.Dir(agentID), recordFileName)
}

// LogPath is the captured harness event stream for one agent ID.
func (store Store) LogPath(agentID string) string {
	return filepath.Join(store.Dir(agentID), logFileName)
}

// LastMessagePath is where the harness is asked to write its final message.
func (store Store) LastMessagePath(agentID string) string {
	return filepath.Join(store.Dir(agentID), lastMessageFileName)
}

// HarnessHomePath is the private per-run harness home. Pointing the harness at
// its own home is what keeps a dispatched worker from reading or writing the
// user's harness state while the parent harness keeps running normally.
func (store Store) HarnessHomePath(agentID string) string {
	return filepath.Join(store.Dir(agentID), harnessHomeDirName)
}

// Create prepares the run directory and persists the initial record before any
// process is started, so a launch that fails still leaves something to
// diagnose.
func (store Store) Create(record Record) error {
	if err := validateAgentID(record.AgentID); err != nil {
		return err
	}
	dir := store.Dir(record.AgentID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create agent run directory %s: %w", dir, err)
	}
	return store.Save(record)
}

// Save atomically replaces the record, so a reader never observes a partially
// written run.
func (store Store) Save(record Record) error {
	return store.saveInjected(record, nil)
}

// saveInjected is Save's test seam (task-9 PR-7): every production call site
// reaches it only through Save, which always passes a nil
// *filewrite.Injector, so production behaviour is unchanged. A test passes
// its own Injector to reach the stage/chmod/write/sync/close/rename failure
// branches deterministically.
func (store Store) saveInjected(record Record, inj *filewrite.Injector) error {
	if err := validateAgentID(record.AgentID); err != nil {
		return err
	}
	record.SchemaVersion = schemaVersion
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent run record: %w", err)
	}
	raw = append(raw, '\n')
	dir := store.Dir(record.AgentID)
	temporary, err := filewrite.CreateTemp(dir, ".run-*.json", inj)
	if err != nil {
		return fmt.Errorf("stage agent run record: %w", err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := filewrite.ChmodFile(temporary, 0o600, name, inj); err != nil {
		_ = filewrite.Close(temporary, name, inj)
		return fmt.Errorf("protect staged agent run record: %w", err)
	}
	if err := filewrite.Write(temporary, raw, name, inj); err != nil {
		_ = filewrite.Close(temporary, name, inj)
		return fmt.Errorf("write agent run record: %w", err)
	}
	if err := filewrite.Sync(temporary, name, inj); err != nil {
		_ = filewrite.Close(temporary, name, inj)
		return fmt.Errorf("sync agent run record: %w", err)
	}
	if err := filewrite.Close(temporary, name, inj); err != nil {
		return fmt.Errorf("close agent run record: %w", err)
	}
	if err := filewrite.Rename(name, filepath.Join(dir, recordFileName), inj); err != nil {
		return fmt.Errorf("replace agent run record: %w", err)
	}
	return nil
}

// Load reads one run record, failing actionably when it does not exist.
func (store Store) Load(agentID string) (Record, error) {
	if err := validateAgentID(agentID); err != nil {
		return Record{}, err
	}
	raw, err := os.ReadFile(store.RecordPath(agentID))
	if err != nil {
		if os.IsNotExist(err) {
			return Record{}, &UnknownAgentError{AgentID: agentID, Root: store.Root}
		}
		return Record{}, fmt.Errorf("read agent run %s: %w", agentID, err)
	}
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return Record{}, fmt.Errorf("parse agent run %s: %w", agentID, err)
	}
	if record.SchemaVersion != schemaVersion {
		return Record{}, fmt.Errorf("agent run %s has schema_version %d; this wb supports %d", agentID, record.SchemaVersion, schemaVersion)
	}
	return record, nil
}

// List returns every run record, newest first. A directory that cannot be read
// is reported rather than silently skipped.
func (store Store) List() ([]Record, error) {
	return store.listSince(time.Time{})
}

// ListSince avoids decoding run records in directories that have not changed
// since the requested window. It still checks StartedAt after loading because
// an older run may have a recently updated record.
func (store Store) ListSince(since time.Time) ([]Record, error) {
	return store.listSince(since)
}

func (store Store) listSince(since time.Time) ([]Record, error) {
	return store.listSinceWithInfo(since, os.DirEntry.Info)
}

func (store Store) listSinceWithInfo(since time.Time, entryInfo func(os.DirEntry) (os.FileInfo, error)) ([]Record, error) {
	entries, err := os.ReadDir(store.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list agent runs under %s: %w", store.Root, err)
	}
	capacity := 0
	if since.IsZero() {
		capacity = len(entries)
	}
	records := make([]Record, 0, capacity)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), IDPrefix) {
			continue
		}
		if !since.IsZero() {
			info, infoErr := entryInfo(entry)
			if infoErr != nil {
				return nil, fmt.Errorf("inspect agent run %s: %w", entry.Name(), infoErr)
			}
			if info.ModTime().Before(since) {
				continue
			}
		}
		record, loadErr := store.Load(entry.Name())
		if loadErr != nil {
			return nil, loadErr
		}
		if !since.IsZero() && record.StartedAt.Before(since) {
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if !records[i].StartedAt.Equal(records[j].StartedAt) {
			return records[i].StartedAt.After(records[j].StartedAt)
		}
		return records[i].AgentID < records[j].AgentID
	})
	return records, nil
}

// SummaryLine is the one-line non-sensitive description stored beside the
// private task so listings and cross-machine checks can name the work without
// republishing it.
func SummaryLine(task string) string {
	const limit = 200
	line := strings.TrimSpace(task)
	if index := strings.IndexAny(line, "\r\n"); index >= 0 {
		line = line[:index]
	}
	line = strings.Join(strings.Fields(line), " ")
	if len(line) > limit {
		line = strings.TrimSpace(line[:limit]) + "…"
	}
	return line
}

// BoundResult trims the worker's final message so the one free-text artefact
// of a run cannot become the transcript it exists to replace.
func BoundResult(message string) string {
	trimmed := strings.TrimSpace(message)
	if len(trimmed) <= maxResultBytes {
		return trimmed
	}
	return trimmed[:maxResultBytes] + "\n… (truncated; see the run log for the full message)"
}

func validateAgentID(agentID string) error {
	if !strings.HasPrefix(agentID, IDPrefix) {
		return fmt.Errorf("invalid agent run ID %q; expected the %s prefix", agentID, IDPrefix)
	}
	suffix := strings.TrimPrefix(agentID, IDPrefix)
	// An empty or short suffix must be rejected explicitly: hex.DecodeString
	// accepts both, which would let a truncated ID address the wrong run.
	if len(suffix) != 32 {
		return fmt.Errorf("invalid agent run ID %q; expected %d hex characters after %q", agentID, 32, IDPrefix)
	}
	if _, err := hex.DecodeString(suffix); err != nil {
		return fmt.Errorf("invalid agent run ID %q", agentID)
	}
	return nil
}

// IsUnknownAgent reports whether an error is a lookup miss rather than a
// corrupt record. A miss is a finding — the invocation was admitted and the
// named run does not exist — so the CLI can use the findings exit code instead
// of pretending the caller mistyped.
func IsUnknownAgent(err error) bool {
	var notFound *UnknownAgentError
	return errors.As(err, &notFound)
}

// UnknownAgentError is returned when a named run does not exist.
type UnknownAgentError struct {
	AgentID string
	Root    string
}

func (err *UnknownAgentError) Error() string {
	return fmt.Sprintf("no dispatched agent run %s under %s", err.AgentID, err.Root)
}
