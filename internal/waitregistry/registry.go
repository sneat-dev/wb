// Package waitregistry records outstanding delegated waits so that someone
// other than the waiting process can see them.
//
// A wait that exists only as a background process in one harness's memory has
// moved the "did anyone remember this?" problem rather than solved it: an agent
// that correctly delegates its waiting goes quiet, and a quiet session is
// indistinguishable from a crashed one. The only recourse available to a
// watching human is to interrupt, which destroys the quiet that delegating the
// wait exists to create.
//
// This is deliberately not a watch service. It stores no events, delivers
// nothing, and wakes nobody. It answers one question — what is this session
// waiting for, since when, and how would it resume — and forgets the answer
// when the wait ends.
package waitregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/provenance"
	"github.com/sneat-dev/wb/internal/session"
)

const (
	recordSchema = 1
	directory    = "waits"
)

// Record is one outstanding wait. It holds no provider payloads, no
// credentials, and no prompt text: only what is needed to answer "who is
// waiting for what, and how does it resume".
type Record struct {
	Schema      int       `json:"schema"`
	ID          string    `json:"id"`
	PID         int       `json:"pid"`
	WBSessionID string    `json:"wb_session_id,omitempty"`
	Kind        string    `json:"kind"`
	Targets     []string  `json:"targets"`
	Until       string    `json:"until"`
	StartedAt   time.Time `json:"started_at"`
	Deadline    time.Time `json:"deadline,omitempty"`
	ResumeArgs  []string  `json:"resume_args,omitempty"`
	// Stale marks a record whose process is gone. It is never persisted; List
	// derives it, because a wait that died without clearing its record is
	// exactly the thing worth seeing.
	Stale bool `json:"stale,omitempty"`

	// Provenance fields (wb#631, SDLC logging-gap analysis 2026-09-18): IDs
	// only, stamped by Register from the environment at zero cost, never a
	// prompt or response body. Additive and omitempty; recordSchema did not
	// need to move for a purely additive field.
	HarnessSessionID string `json:"harness_session_id,omitempty"`
	Harness          string `json:"harness,omitempty"`
	EffortLevel      string `json:"effort_level,omitempty"`
	AgentID          string `json:"agent_id,omitempty"`
	ToolUseID        string `json:"tool_use_id,omitempty"`
	WBVersion        string `json:"wb_version,omitempty"`
}

// Alive reports whether a process still exists. It delegates to WB's existing
// per-platform liveness check rather than reimplementing one: an earlier
// hand-rolled version used Process.Signal(nil), which always fails Go's signal
// type assertion, so every live waiter was reported as stale.
//
// It is a variable so tests can decide liveness without spawning processes.
var Alive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	return session.ProcessAlive(pid)
}

func dir(home string) string { return filepath.Join(home, directory) }

// Register writes the record and returns a function that removes it. The
// caller defers that function: a wait that ends normally leaves nothing behind,
// and one that is killed leaves a record List reports as stale.
func Register(home string, record Record) (func(), error) {
	return registerInjected(home, record, nil)
}

// registerInjected is Register's test seam (task-9 PR-8): every production
// call site reaches it only through Register, which always passes a nil
// *filewrite.Injector, so production behaviour is unchanged. A test passes
// its own Injector to reach the write/rename failure branches
// deterministically.
func registerInjected(home string, record Record, inj *filewrite.Injector) (func(), error) {
	if strings.TrimSpace(record.ID) == "" {
		return nil, errors.New("wait record requires an id")
	}
	record.Schema = recordSchema
	fields := provenance.FromEnv()
	if record.HarnessSessionID == "" {
		record.HarnessSessionID = fields.HarnessSessionID
	}
	if record.Harness == "" {
		record.Harness = fields.Harness
	}
	if record.EffortLevel == "" {
		record.EffortLevel = fields.EffortLevel
	}
	if record.AgentID == "" {
		record.AgentID = fields.AgentID
	}
	if record.ToolUseID == "" {
		record.ToolUseID = fields.ToolUseID
	}
	if record.WBVersion == "" {
		record.WBVersion = fields.WBVersion
	}
	root := dir(home)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create wait registry: %w", err)
	}
	path := filepath.Join(root, record.ID+".json")
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode wait record: %w", err)
	}
	// Written whole, then renamed, so a reader never sees half a record.
	temporary := path + ".tmp"
	if err := filewrite.WriteFile(temporary, encoded, 0o600, inj); err != nil {
		return nil, fmt.Errorf("write wait record: %w", err)
	}
	if err := filewrite.Rename(temporary, path, inj); err != nil {
		return nil, fmt.Errorf("commit wait record: %w", err)
	}
	return func() { _ = os.Remove(path) }, nil
}

// List reports every recorded wait, newest first, marking as stale any whose
// process is gone. Listing never deletes: a stale record is evidence, and
// removing it is an explicit Prune.
func List(home string) ([]Record, error) {
	entries, err := os.ReadDir(dir(home))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read wait registry: %w", err)
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(dir(home), entry.Name())) //nolint:gosec // WB's own state directory.
		if readErr != nil {
			continue
		}
		var record Record
		if json.Unmarshal(raw, &record) != nil || record.Schema != recordSchema {
			continue
		}
		record.Stale = !Alive(record.PID)
		records = append(records, record)
	}
	sort.Slice(records, func(one, two int) bool { return records[one].StartedAt.After(records[two].StartedAt) })
	return records, nil
}

// Prune removes records whose process is gone and reports how many went. It is
// separate from List so that seeing a dead waiter is never a side effect of
// asking what is waiting.
func Prune(home string) (int, error) {
	records, err := List(home)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, record := range records {
		if !record.Stale {
			continue
		}
		if os.Remove(filepath.Join(dir(home), record.ID+".json")) == nil {
			removed++
		}
	}
	return removed, nil
}
