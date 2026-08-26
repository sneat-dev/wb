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
	"crypto/rand"
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
	PID                    int    `json:"pid"`
	WBSessionID            string `json:"wb_session_id,omitempty"`
	Machine                string `json:"machine,omitempty"`
	Runtime                string `json:"runtime,omitempty"`
	Model                  string `json:"model,omitempty"`
	NativeHarnessID        string `json:"native_harness_id,omitempty"`
	TmuxName               string `json:"tmux_name,omitempty"`
	PredecessorWBSessionID string `json:"predecessor_wb_session_id,omitempty"`
	HandoffID              string `json:"handoff_id,omitempty"`

	// AgentID is the legacy spelling for a harness-native session ID. It stays
	// readable and writable so existing hooks and PID records continue to
	// work; new integrations should use NativeHarnessID.
	AgentID string `json:"agent_id,omitempty"`

	// WBVersion and WBPath describe the binary that took the registration.
	// Several WB builds can coexist — a release on PATH and a local build
	// under test — and knowing which one a session used is what makes
	// otherwise inexplicable behaviour explicable.
	WBVersion string `json:"wb_version,omitempty"`
	WBPath    string `json:"wb_path,omitempty"`

	StartedAt time.Time `json:"started_at"`
	// Lifecycle is the local registry projection. Parked sessions remain
	// addressable but are never considered live/claimable.
	Lifecycle       string `json:"lifecycle,omitempty"`
	ParkedSessionID string `json:"parked_session_id,omitempty"`
}

// View is a record plus its liveness, evaluated when WB reads it. Liveness is
// never persisted: it is only true of a moment.
type View struct {
	Record
	State string `json:"state"`
}

// Liveness states, matching the vocabulary used for worktree owners.
const (
	StateLive   = "live"
	StateGone   = "gone"
	StateParked = "parked"
)

func recordPath(dir string, pid int) string {
	return filepath.Join(dir, strconv.Itoa(pid)+".json")
}

// NewID returns an opaque WB session identity. It is independent of every
// runtime-specific identifier and safe to carry in file and tmux names.
func NewID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate WB session ID: %w", err)
	}
	return fmt.Sprintf("wbs-%x", random[:]), nil
}

// Register writes a session record, replacing any record for the same PID.
// Re-registering is deliberately allowed: a session that restarts its harness
// or corrects its model should not have to find and delete the old file.
func Register(dir string, record Record) (Record, error) {
	if record.PID <= 0 {
		return Record{}, fmt.Errorf("a session must declare a positive PID")
	}
	record.WBSessionID = strings.TrimSpace(record.WBSessionID)
	record.Machine = strings.TrimSpace(record.Machine)
	record.Runtime = strings.TrimSpace(record.Runtime)
	record.Model = strings.TrimSpace(record.Model)
	record.NativeHarnessID = strings.TrimSpace(record.NativeHarnessID)
	record.TmuxName = strings.TrimSpace(record.TmuxName)
	record.PredecessorWBSessionID = strings.TrimSpace(record.PredecessorWBSessionID)
	record.HandoffID = strings.TrimSpace(record.HandoffID)
	record.Lifecycle = strings.TrimSpace(record.Lifecycle)
	record.ParkedSessionID = strings.TrimSpace(record.ParkedSessionID)
	record.AgentID = strings.TrimSpace(record.AgentID)
	if record.NativeHarnessID != "" && record.AgentID != "" && record.NativeHarnessID != record.AgentID {
		return Record{}, fmt.Errorf("native harness ID %q conflicts with legacy agent ID %q", record.NativeHarnessID, record.AgentID)
	}
	if record.NativeHarnessID == "" {
		record.NativeHarnessID = record.AgentID
	}

	// Re-registering a PID is an update to the same declared session unless a
	// caller supplies a preallocated identity explicitly. Preserve the stable
	// fields that cannot be rediscovered from the process itself.
	if previous, ok := readRecord(recordPath(dir, record.PID)); ok {
		sameSession := record.WBSessionID == "" || record.WBSessionID == previous.WBSessionID
		if record.WBSessionID == "" {
			record.WBSessionID = previous.WBSessionID
		}
		if sameSession {
			if record.Lifecycle == "" {
				record.Lifecycle = previous.Lifecycle
			}
			if record.ParkedSessionID == "" {
				record.ParkedSessionID = previous.ParkedSessionID
			}
			if record.Machine == "" {
				record.Machine = previous.Machine
			}
			if record.NativeHarnessID == "" {
				record.NativeHarnessID = previous.NativeHarnessID
			}
			if record.TmuxName == "" {
				record.TmuxName = previous.TmuxName
			}
			if record.PredecessorWBSessionID == "" {
				record.PredecessorWBSessionID = previous.PredecessorWBSessionID
			}
			if record.HandoffID == "" {
				record.HandoffID = previous.HandoffID
			}
			if record.StartedAt.IsZero() {
				record.StartedAt = previous.StartedAt
			}
		}
	}
	if record.WBSessionID == "" {
		id, err := NewID()
		if err != nil {
			return Record{}, err
		}
		record.WBSessionID = id
	}
	if record.Machine == "" {
		hostname, err := os.Hostname()
		if err != nil {
			return Record{}, fmt.Errorf("resolve session machine: %w", err)
		}
		record.Machine = strings.TrimSpace(hostname)
		if record.Machine == "" {
			return Record{}, fmt.Errorf("resolve session machine: hostname is empty")
		}
	}
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

// MarkParked records the non-live registry projection for a session. The
// original declaration remains untouched in the PID index. A no-replace
// lifecycle marker changes the live projection while keeping the source
// auditable and ensuring session resolution cannot treat a parked owner as active.
func MarkParked(dir string, pid int, parkedID string) (Record, error) {
	record, ok := readRecord(recordPath(dir, pid))
	if !ok {
		return Record{}, fmt.Errorf("session with pid %d is not registered", pid)
	}
	if record.Lifecycle == "parked" {
		if record.ParkedSessionID == parkedID {
			return record, nil
		}
		return Record{}, fmt.Errorf("session with pid %d is already parked as %s", pid, record.ParkedSessionID)
	}
	if existing, err := os.ReadFile(parkedMarkerPath(dir, record.WBSessionID)); err == nil {
		var marker struct {
			ParkedSessionID string `json:"parked_session_id"`
		}
		if json.Unmarshal(existing, &marker) == nil && marker.ParkedSessionID == parkedID {
			record.Lifecycle, record.ParkedSessionID = "parked", parkedID
			return record, nil
		}
		return Record{}, fmt.Errorf("session with pid %d is already parked", pid)
	}
	if record.Lifecycle == "resumed" {
		return Record{}, fmt.Errorf("session with pid %d has already resumed", pid)
	}
	if strings.TrimSpace(parkedID) == "" {
		return Record{}, fmt.Errorf("parked session ID is required")
	}
	// The PID registration is immutable history. A separate no-replace marker
	// changes the live projection without rewriting the declaration itself.
	markerDir := filepath.Join(dir, "lifecycle")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		return Record{}, err
	}
	marker := struct {
		SchemaVersion   int       `json:"schema_version"`
		WBSessionID     string    `json:"wb_session_id"`
		ParkedSessionID string    `json:"parked_session_id"`
		At              time.Time `json:"at"`
	}{1, record.WBSessionID, parkedID, time.Now().UTC()}
	raw, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return Record{}, err
	}
	path := filepath.Join(markerDir, record.WBSessionID+".parked.json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return Record{}, fmt.Errorf("session with pid %d is already parked", pid)
	}
	if err != nil {
		return Record{}, fmt.Errorf("record parked session lifecycle: %w", err)
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		_ = file.Close()
		return Record{}, err
	}
	if err := file.Close(); err != nil {
		return Record{}, err
	}
	record.Lifecycle = "parked"
	record.ParkedSessionID = parkedID
	return record, nil
}

func parkedMarkerPath(dir, wbSessionID string) string {
	return filepath.Join(dir, "lifecycle", wbSessionID+".parked.json")
}
func parked(dir, wbSessionID string) bool {
	if wbSessionID == "" {
		return false
	}
	_, err := os.Stat(parkedMarkerPath(dir, wbSessionID))
	return err == nil
}

func readRecord(path string) (Record, bool) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Record{}, false
	}
	var record Record
	if err := json.Unmarshal(content, &record); err != nil || record.PID <= 0 {
		return Record{}, false
	}
	if record.NativeHarnessID == "" {
		record.NativeHarnessID = record.AgentID
	}
	return record, true
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
		// A malformed record is skipped rather than failing the listing: one
		// bad file must not hide every other session.
		record, ok := readRecord(filepath.Join(dir, entry.Name()))
		if !ok {
			continue
		}
		viewState := state(record.PID)
		if record.Lifecycle == "parked" || parked(dir, record.WBSessionID) {
			viewState = StateParked
		}
		views = append(views, View{Record: record, State: viewState})
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
	record, ok := readRecord(recordPath(dir, pid))
	if !ok {
		return Record{}, false
	}
	return record, record.Lifecycle != "parked" && record.Lifecycle != "resumed" && !parked(dir, record.WBSessionID) && state(record.PID) == StateLive
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
