// Package runlog records privacy-safe telemetry for commands launched through
// `wb run --`. The log lives inside a managed worktree so an agent can write it
// without access to the user's home directory and so lifecycle tooling can
// later publish or aggregate it with the worktree's other local evidence.
package runlog

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
	"golang.org/x/sys/unix"
)

const (
	EventSchemaVersion = 1
	OperationIDEnv     = "WB_OPERATION_ID"
)

// Event is one immutable command lifecycle observation. It deliberately omits
// raw arguments and output; ArgsSHA256 supports correlation without copying
// prompts, commit messages, paths, tokens, or source into the log.
type Event struct {
	SchemaVersion int       `json:"schema_version"`
	Timestamp     time.Time `json:"timestamp"`
	OperationID   string    `json:"operation_id"`
	State         string    `json:"state"`
	Kind          string    `json:"kind"`
	ArgsSHA256    string    `json:"args_sha256"`
	ArgumentCount int       `json:"argument_count"`
	Repository    string    `json:"repository,omitempty"`
	EffortID      string    `json:"effort_id,omitempty"`
	RunID         string    `json:"run_id,omitempty"`
	DurationMS    int64     `json:"duration_ms,omitempty"`
	UserCPUMS     int64     `json:"user_cpu_ms,omitempty"`
	SystemCPUMS   int64     `json:"system_cpu_ms,omitempty"`
	QueueWaitMS   int64     `json:"queue_wait_ms,omitempty"`
	CPUUnits      int       `json:"cpu_units,omitempty"`
	ExitCode      *int      `json:"exit_code,omitempty"`
}

// RecordAdmission adds scheduler evidence to the terminal event without
// exposing command arguments or output.
func (recorder *Recorder) RecordAdmission(units int, wait time.Duration) {
	recorder.event.CPUUnits = units
	recorder.event.QueueWaitMS = wait.Milliseconds()
}

// Recorder owns one operation ID and its optional managed-worktree log.
type Recorder struct {
	OperationID string
	Path        string
	StartedAt   time.Time
	event       Event
}

type Summary struct {
	Since       time.Time     `json:"since"`
	Operations  int           `json:"operations"`
	Running     int           `json:"running"`
	Failed      int           `json:"failed"`
	WallMS      int64         `json:"wall_ms"`
	UserCPUMS   int64         `json:"user_cpu_ms"`
	SystemCPUMS int64         `json:"system_cpu_ms"`
	Kinds       []KindSummary `json:"kinds"`
}

type KindSummary struct {
	Kind       string `json:"kind"`
	Operations int    `json:"operations"`
	Failed     int    `json:"failed"`
	WallMS     int64  `json:"wall_ms"`
	P50MS      int64  `json:"p50_ms"`
	P95MS      int64  `json:"p95_ms"`
}

// Begin records a requested operation when cwd belongs to a managed worktree.
// Unmanaged directories still get an operation ID but no filesystem side
// effect; WB can therefore wrap diagnostics outside a worktree safely.
func Begin(cwd string, argv []string, now time.Time) (Recorder, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	id, err := newOperationID()
	if err != nil {
		return Recorder{}, err
	}
	event := Event{
		SchemaVersion: EventSchemaVersion,
		Timestamp:     now,
		OperationID:   id,
		State:         "requested",
		Kind:          classify(argv),
		ArgsSHA256:    digestArgs(argv),
		ArgumentCount: len(argv),
	}
	recorder := Recorder{OperationID: id, StartedAt: now, event: event}

	root, manifest, found := managedWorktree(cwd)
	if !found {
		return recorder, nil
	}
	recorder.Path = filepath.Join(root, ".wb", "local", "run", "events.jsonl")
	recorder.event.Repository = manifest.Repository
	recorder.event.EffortID = manifest.EffortID
	recorder.event.RunID = manifest.RunID
	if err := Append(recorder.Path, recorder.event); err != nil {
		return recorder, err
	}
	return recorder, nil
}

// Finish records the terminal result. CPU durations come from the child
// process state and exclude WB orchestration overhead.
func (recorder Recorder) Finish(exitCode int, userCPU, systemCPU time.Duration, now time.Time) error {
	if recorder.Path == "" {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	event := recorder.event
	event.Timestamp = now
	event.State = "succeeded"
	if exitCode != 0 {
		event.State = "failed"
	}
	event.DurationMS = now.Sub(recorder.StartedAt).Milliseconds()
	event.UserCPUMS = userCPU.Milliseconds()
	event.SystemCPUMS = systemCPU.Milliseconds()
	event.ExitCode = &exitCode
	return Append(recorder.Path, event)
}

// Append writes one event under an exclusive file lock so concurrent commands
// in the same worktree cannot interleave JSON bytes.
func Append(path string, event Event) error {
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode run event: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create run event directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open run event log: %w", err)
	}
	defer func() { _ = file.Close() }()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock run event log: %w", err)
	}
	defer func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }()
	if _, err := file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append run event: %w", err)
	}
	return nil
}

// Read parses a run event log. Unknown newer schemas are rejected.
func Read(path string) ([]Event, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var events []Event
	for index, line := range strings.Split(string(contents), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("parse run event line %d: %w", index+1, err)
		}
		if event.SchemaVersion > EventSchemaVersion {
			return nil, fmt.Errorf("run event line %d uses schema version %d; this WB supports %d", index+1, event.SchemaVersion, EventSchemaVersion)
		}
		events = append(events, event)
	}
	return events, nil
}

// ReadCurrent returns telemetry for the managed worktree containing cwd.
func ReadCurrent(cwd string) ([]Event, string, error) {
	root, _, found := managedWorktree(cwd)
	if !found {
		return nil, "", fmt.Errorf("%s is not inside a managed WB worktree", cwd)
	}
	path := filepath.Join(root, ".wb", "local", "run", "events.jsonl")
	events, err := Read(path)
	return events, path, err
}

// Summarize computes completed-operation cost and currently unmatched starts.
func Summarize(events []Event, since time.Time) Summary {
	summary := Summary{Since: since.UTC()}
	requested := make(map[string]Event)
	finished := make(map[string]Event)
	for _, event := range events {
		if event.Timestamp.Before(since) {
			continue
		}
		switch event.State {
		case "requested":
			requested[event.OperationID] = event
		case "succeeded", "failed":
			finished[event.OperationID] = event
		}
	}
	type accumulator struct {
		KindSummary
		durations []int64
	}
	byKind := make(map[string]*accumulator)
	for id, event := range finished {
		summary.Operations++
		summary.WallMS += event.DurationMS
		summary.UserCPUMS += event.UserCPUMS
		summary.SystemCPUMS += event.SystemCPUMS
		if event.State == "failed" {
			summary.Failed++
		}
		kind := byKind[event.Kind]
		if kind == nil {
			kind = &accumulator{KindSummary: KindSummary{Kind: event.Kind}}
			byKind[event.Kind] = kind
		}
		kind.Operations++
		kind.WallMS += event.DurationMS
		kind.durations = append(kind.durations, event.DurationMS)
		if event.State == "failed" {
			kind.Failed++
		}
		delete(requested, id)
	}
	summary.Running = len(requested)
	for _, kind := range byKind {
		sort.Slice(kind.durations, func(i, j int) bool { return kind.durations[i] < kind.durations[j] })
		kind.P50MS = percentile(kind.durations, 0.50)
		kind.P95MS = percentile(kind.durations, 0.95)
		summary.Kinds = append(summary.Kinds, kind.KindSummary)
	}
	sort.Slice(summary.Kinds, func(i, j int) bool { return summary.Kinds[i].Kind < summary.Kinds[j].Kind })
	return summary
}

func percentile(values []int64, quantile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	index := int(math.Ceil(float64(len(values))*quantile)) - 1
	if index < 0 {
		index = 0
	}
	return values[index]
}

func managedWorktree(cwd string) (string, worktrees.Manifest, bool) {
	current, err := filepath.Abs(cwd)
	if err != nil {
		return "", worktrees.Manifest{}, false
	}
	for {
		if _, err := os.Stat(filepath.Join(current, ".wb", "local", "manifest.yaml")); err == nil {
			manifest, readErr := worktrees.ReadManifest(current)
			if readErr == nil {
				return current, manifest, true
			}
			return "", worktrees.Manifest{}, false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", worktrees.Manifest{}, false
		}
		current = parent
	}
}

func newOperationID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate operation ID: %w", err)
	}
	return "wbo-" + hex.EncodeToString(value), nil
}

func digestArgs(argv []string) string {
	encoded, _ := json.Marshal(argv)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func classify(argv []string) string {
	if len(argv) == 0 {
		return "unknown"
	}
	tool := strings.ToLower(filepath.Base(argv[0]))
	allowed := map[string]map[string]bool{
		"go":        {"build": true, "fmt": true, "generate": true, "mod": true, "test": true, "vet": true},
		"git":       {"add": true, "branch": true, "commit": true, "diff": true, "fetch": true, "log": true, "merge": true, "push": true, "rebase": true, "status": true, "worktree": true},
		"npm":       {"ci": true, "install": true, "run": true, "test": true},
		"pnpm":      {"exec": true, "install": true, "run": true, "test": true},
		"specscore": {"feature": true, "lesson": true, "spec": true},
	}
	verbs, known := allowed[tool]
	if !known {
		return "other"
	}
	for _, argument := range argv[1:] {
		if strings.HasPrefix(argument, "-") {
			continue
		}
		verb := strings.ToLower(argument)
		if verbs[verb] {
			return tool + "/" + verb
		}
		break
	}
	return tool
}
