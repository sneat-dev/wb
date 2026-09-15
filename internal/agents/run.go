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
	harnessHomeDirName  = "harness-home"
	schemaVersion       = 1
	// maxResultBytes bounds the worker's own final message, which is the one
	// free-text artefact from the transcript that may travel back to a caller.
	maxResultBytes = 4096
	// maxChangedFiles bounds the changed-file list so a sweeping diff cannot
	// turn a result document into a transcript.
	maxChangedFiles = 50
)

// State is the closed lifecycle vocabulary of one dispatched run.
type State string

const (
	StateRunning   State = "running"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateTimeout   State = "timeout"
	StateAbandoned State = "abandoned"
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
}

// Record is the durable description of one dispatched run. It is private: it
// holds the original task, which never travels back out through status or
// await.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	AgentID       string `json:"agent_id"`
	State         State  `json:"state"`

	// RequestedProfile is what the caller asked for. Resolved is what actually
	// ran; keeping both is what makes a later profile edit harmless.
	RequestedProfile string   `json:"requested_profile"`
	Resolved         Resolved `json:"resolved"`

	// Task is the exact originating request. It is private: it is never
	// rendered by status or await, only persisted here.
	Task string `json:"task"`
	// TaskSummary is a bounded, single-line form of Task for a human reading
	// the record. It is derived from the task, so it is private for exactly the
	// same reason Task is, and is likewise never rendered.
	TaskSummary string `json:"task_summary,omitempty"`

	Repository   string `json:"repository"`
	WorktreeMode string `json:"worktree_mode"`
	Worktree     string `json:"worktree"`
	WorktreeDir  string `json:"worktree_dir,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Base         string `json:"base,omitempty"`
	BaseSHA      string `json:"base_sha,omitempty"`

	// WorkLogClaimPath links this run to the immutable Work Log claim the
	// worktree service already publishes, so one dispatch never produces two
	// unreferenced records of the same piece of work.
	WorkLogClaimPath string `json:"work_log_claim_path,omitempty"`
	WorkLogRunID     string `json:"work_log_run_id,omitempty"`

	// TimeoutMS bounds the run. It is persisted because the owner, not the
	// dispatcher, enforces it.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`

	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`

	OwnerPID      int            `json:"owner_pid,omitempty"`
	WorkerPID     int            `json:"worker_pid,omitempty"`
	ExitCode      *int           `json:"exit_code,omitempty"`
	Failure       string         `json:"failure,omitempty"`
	Result        string         `json:"result,omitempty"`
	ToolCalls     int            `json:"tool_calls,omitempty"`
	Usage         *Usage         `json:"usage,omitempty"`
	Changes       *ChangeSummary `json:"changes,omitempty"`
	HarnessEvents []string       `json:"harness_diagnostics,omitempty"`

	LogPath string `json:"log_path,omitempty"`
}

// Result is the rendered projection of a Record, and the machine-readable
// document `status` and `await` emit. It is written out field by field rather
// than derived by embedding, so the private task can never reach a caller by
// accident: adding a field to Record does not add it here.
type Result struct {
	AgentID            string         `json:"agent_id"`
	State              State          `json:"state"`
	Terminal           bool           `json:"terminal"`
	RequestedProfile   string         `json:"profile"`
	Resolved           Resolved       `json:"resolved"`
	TaskSummary        string         `json:"task_summary,omitempty"`
	Repository         string         `json:"repository"`
	WorktreeMode       string         `json:"worktree_mode"`
	Worktree           string         `json:"worktree"`
	WorktreeDir        string         `json:"worktree_dir,omitempty"`
	Branch             string         `json:"branch,omitempty"`
	Base               string         `json:"base,omitempty"`
	BaseSHA            string         `json:"base_sha,omitempty"`
	StartedAt          time.Time      `json:"started_at"`
	FinishedAt         *time.Time     `json:"finished_at,omitempty"`
	DurationMS         int64          `json:"duration_ms,omitempty"`
	OwnerPID           int            `json:"owner_pid,omitempty"`
	WorkerPID          int            `json:"worker_pid,omitempty"`
	OwnerAlive         bool           `json:"owner_alive"`
	ExitCode           *int           `json:"exit_code,omitempty"`
	Failure            string         `json:"failure,omitempty"`
	Result             string         `json:"result,omitempty"`
	ToolCalls          int            `json:"tool_calls,omitempty"`
	Usage              *Usage         `json:"usage,omitempty"`
	Changes            *ChangeSummary `json:"changes,omitempty"`
	HarnessDiagnostics []string       `json:"harness_diagnostics,omitempty"`
	LogPath            string         `json:"log_path,omitempty"`
}

// Render projects a record for output, resolving a run whose owner vanished
// without recording a terminal state to the honest answer: abandoned. A run is
// never reported as completed merely because nothing contradicted it.
func (store Store) Render(record Record) Result {
	state := record.State
	alive := false
	if state == StateRunning {
		// The worker is checked as well as the owner so a run whose owner was
		// killed while its harness kept working is not called abandoned.
		alive = processAlive(record.OwnerPID) || processAlive(record.WorkerPID)
		if !alive {
			state = StateAbandoned
		}
	}
	result := Result{
		AgentID: record.AgentID, State: state, Terminal: state.Terminal(),
		RequestedProfile: record.RequestedProfile, Resolved: record.Resolved,
		Repository:   record.Repository,
		WorktreeMode: record.WorktreeMode, Worktree: record.Worktree,
		WorktreeDir: record.WorktreeDir, Branch: record.Branch,
		Base: record.Base, BaseSHA: record.BaseSHA,
		StartedAt: record.StartedAt, DurationMS: record.DurationMS,
		OwnerPID: record.OwnerPID, WorkerPID: record.WorkerPID, OwnerAlive: alive,
		ExitCode: record.ExitCode, Failure: record.Failure, Result: record.Result,
		ToolCalls: record.ToolCalls, Usage: record.Usage, Changes: record.Changes,
		HarnessDiagnostics: record.HarnessEvents, LogPath: record.LogPath,
	}
	if !record.FinishedAt.IsZero() {
		finished := record.FinishedAt
		result.FinishedAt = &finished
	}
	return result
}

// NewID mints a fresh agent run ID.
func NewID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate agent run ID: %w", err)
	}
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
	temporary, err := os.CreateTemp(dir, ".run-*.json")
	if err != nil {
		return fmt.Errorf("stage agent run record: %w", err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect staged agent run record: %w", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write agent run record: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync agent run record: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close agent run record: %w", err)
	}
	if err := os.Rename(name, filepath.Join(dir, recordFileName)); err != nil {
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
	entries, err := os.ReadDir(store.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list agent runs under %s: %w", store.Root, err)
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), IDPrefix) {
			continue
		}
		record, loadErr := store.Load(entry.Name())
		if loadErr != nil {
			return nil, loadErr
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
