// Package session records which agent sessions are running on this machine.
//
// WB is a short-lived command with no daemon, so it cannot observe a session
// starting. A session announces itself once — from a harness start-up hook, or
// by hand — and everything WB writes afterwards can be attributed to it without
// each command being told again.
//
// A record is a claim, not an observation. WB stores what it was told, adds
// only what it can see for itself (its own version and binary path), and
// evaluates liveness at read time from the declared PID.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sneat-dev/wb/internal/buildinfo"
)

// DirName is the directory under WB's home that holds session records.
const DirName = "sessions"

// Record is one agent session's self-declaration.
type Record struct {
	PID     int    `json:"pid"`
	Runtime string `json:"runtime,omitempty"`
	Model   string `json:"model,omitempty"`
	AgentID string `json:"agent_id,omitempty"`

	// WBVersion and WBPath describe the binary that took the registration.
	// Several WB builds can coexist — a release on PATH and a local build
	// under test — and knowing which one a session used is what makes
	// otherwise inexplicable behaviour explicable.
	WBVersion string `json:"wb_version,omitempty"`
	WBPath    string `json:"wb_path,omitempty"`

	StartedAt time.Time `json:"started_at"`
}

// View is a record plus its liveness, evaluated when WB reads it. Liveness is
// never persisted: it is only true of a moment.
type View struct {
	Record
	State string `json:"state"`
}

// Liveness states, matching the vocabulary used for worktree owners.
const (
	StateLive = "live"
	StateGone = "gone"
)

func recordPath(dir string, pid int) string {
	return filepath.Join(dir, strconv.Itoa(pid)+".json")
}

// Register writes a session record, replacing any record for the same PID.
// Re-registering is deliberately allowed: a session that restarts its harness
// or corrects its model should not have to find and delete the old file.
func Register(dir string, record Record) (Record, error) {
	if record.PID <= 0 {
		return Record{}, fmt.Errorf("a session must declare a positive PID")
	}
	record.Runtime = strings.TrimSpace(record.Runtime)
	record.Model = strings.TrimSpace(record.Model)
	record.AgentID = strings.TrimSpace(record.AgentID)
	record.WBVersion = buildinfo.Version()
	if executable, err := os.Executable(); err == nil {
		record.WBPath = executable
	}
	if record.StartedAt.IsZero() {
		record.StartedAt = time.Now().UTC()
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Record{}, fmt.Errorf("create session directory: %w", err)
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return Record{}, err
	}
	if err := os.WriteFile(recordPath(dir, record.PID), append(encoded, '\n'), 0o644); err != nil {
		return Record{}, fmt.Errorf("write session record: %w", err)
	}
	return record, nil
}

// List returns every recorded session with its liveness, newest first. A
// missing directory is not an error: no session has registered yet.
func List(dir string) ([]View, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	views := make([]View, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var record Record
		// A malformed record is skipped rather than failing the listing: one
		// bad file must not hide every other session.
		if err := json.Unmarshal(content, &record); err != nil || record.PID <= 0 {
			continue
		}
		views = append(views, View{Record: record, State: state(record.PID)})
	}
	sort.SliceStable(views, func(i, j int) bool {
		return views[i].StartedAt.After(views[j].StartedAt)
	})
	return views, nil
}

// Prune removes records whose process is gone, and reports how many went.
func Prune(dir string) (int, error) {
	views, err := List(dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, view := range views {
		if view.State != StateGone {
			continue
		}
		if err := os.Remove(recordPath(dir, view.PID)); err == nil {
			removed++
		}
	}
	return removed, nil
}

// Lookup returns the record for one PID, if it registered and is still live.
func Lookup(dir string, pid int) (Record, bool) {
	if pid <= 0 {
		return Record{}, false
	}
	content, err := os.ReadFile(recordPath(dir, pid))
	if err != nil {
		return Record{}, false
	}
	var record Record
	if err := json.Unmarshal(content, &record); err != nil {
		return Record{}, false
	}
	return record, state(record.PID) == StateLive
}

func state(pid int) string {
	if pid <= 0 {
		return StateGone
	}
	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return StateLive
	}
	return StateGone
}

// ResolveForProcess finds the registered session that owns a process, by
// walking up from startPID and returning the first ancestor that registered
// and is still live.
//
// This is not the process-tree guessing WB otherwise refuses to do. It matches
// only against PIDs that explicitly declared themselves, so it confirms a
// declaration rather than inventing one: an unregistered ancestor is never
// treated as an owner. Depth is bounded because a corrupted /proc chain must
// not spin.
func ResolveForProcess(dir string, startPID int) (Record, bool) {
	const maxDepth = 12
	pid := startPID
	for depth := 0; depth < maxDepth && pid > 1; depth++ {
		if record, ok := Lookup(dir, pid); ok {
			return record, true
		}
		parent, ok := parentPID(pid)
		if !ok {
			return Record{}, false
		}
		pid = parent
	}
	return Record{}, false
}
