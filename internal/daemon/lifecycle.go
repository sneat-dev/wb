// Package daemon owns the durable local lifecycle record for the WB daemon.
//
// The record deliberately contains scheduler ownership, rather than command
// output, so a later ConnectRPC/gRPC transport and the MCP adapter can use the
// same queue handoff contract without reading a dashboard-only file.
package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// StateSchemaVersion is the version this build writes. Load accepts every
	// version in SupportedStateSchemaVersions: a running daemon's record must
	// stay readable across an upgrade, and refusing an older record would make
	// the daemon that wrote it look absent.
	StateSchemaVersion      = 2
	MinSupportedStateSchema = 1
	QueueSchemaVersion      = 1
)

// SupportedStateSchemaVersions lists the record versions this build can read.
func SupportedStateSchemaVersions() []int {
	versions := make([]int, 0, StateSchemaVersion-MinSupportedStateSchema+1)
	for version := MinSupportedStateSchema; version <= StateSchemaVersion; version++ {
		versions = append(versions, version)
	}
	return versions
}

type Status string

const (
	StatusStarting Status = "starting"
	StatusReady    Status = "ready"
	StatusDraining Status = "draining"
	StatusStopped  Status = "stopped"
)

// Provenance identifies the exact executable trusted to own a daemon
// generation. SHA256 is intentionally included even when the released version
// is known: a development binary can otherwise look identical to a release.
type Provenance struct {
	Executable string `json:"executable"`
	SHA256     string `json:"sha256"`
	Version    string `json:"version"`
	Revision   string `json:"revision,omitempty"`
	Built      string `json:"built,omitempty"`
}

func (p Provenance) SameBinary(other Provenance) bool {
	return p.Executable == other.Executable && p.SHA256 == other.SHA256 &&
		p.Version == other.Version && p.Revision == other.Revision
}

// Queue describes durable queue ownership. Operations are added by the async
// scheduler lane; lifecycle code preserves this object byte-for-byte apart
// from a fenced owner/generation transition.
type Queue struct {
	SchemaVersion int         `json:"schema_version"`
	Generation    uint64      `json:"generation"`
	Owner         Provenance  `json:"owner"`
	OwnerToken    string      `json:"owner_token"`
	HandoffFrom   *Provenance `json:"handoff_from,omitempty"`
	HandoffAt     *time.Time  `json:"handoff_at,omitempty"`
}

// State is private local state. It is never served by the dashboard API.
type State struct {
	SchemaVersion int        `json:"schema_version"`
	Status        Status     `json:"status"`
	PID           int        `json:"pid,omitempty"`
	OwnerToken    string     `json:"owner_token,omitempty"`
	Listen        string     `json:"listen"`
	Provenance    Provenance `json:"provenance"`
	Queue         Queue      `json:"queue"`
	StartedAt     time.Time  `json:"started_at,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at"`

	// WBHome and StatePath record *where* this daemon lives, so a reader can
	// tell "the daemon belonging to my home" from "a daemon answering on my
	// endpoint". They are empty in records written before schema 2, which is
	// reported as unknown home identity rather than assumed to match.
	WBHome    string `json:"wb_home,omitempty"`
	StatePath string `json:"state_path,omitempty"`

	// ProcessStartedAt is the recorded process's start time. PID alone is a
	// liveness coordinate that a recycled number can satisfy; the start time is
	// what makes "still running" a claim about *this* process. Zero means the
	// platform could not observe it, which is reported as unknown rather than
	// treated as a match.
	ProcessStartedAt time.Time `json:"process_started_at,omitempty"`

	// StoppedReason explains a stop the daemon did not choose — a runtime
	// directory removed underneath it, or an endpoint it could not bind. It is
	// recorded so the condition survives the process that hit it: a supervisor
	// restarts the daemon, and without this the reason would exist only in a
	// log the same removal may have unlinked.
	StoppedReason string `json:"stopped_reason,omitempty"`
}

func (s State) Valid() error {
	supported := false
	for _, version := range SupportedStateSchemaVersions() {
		if s.SchemaVersion == version {
			supported = true
			break
		}
	}
	if !supported {
		return fmt.Errorf("unsupported daemon state schema %d (supported: %v)", s.SchemaVersion, SupportedStateSchemaVersions())
	}
	if s.Queue.SchemaVersion != QueueSchemaVersion {
		return fmt.Errorf("unsupported daemon queue schema %d", s.Queue.SchemaVersion)
	}
	if s.Listen == "" {
		return errors.New("daemon state has no listener")
	}
	return nil
}

// NewStarting creates the next fenced queue generation. Existing queue jobs
// stay in the durable queue file owned by the scheduler; this record tells the
// replacement scheduler exactly which binary owned the preceding generation.
func NewStarting(previous *State, listen string, provenance Provenance, ownerToken string, now time.Time) State {
	return NewStartingAt(previous, listen, provenance, ownerToken, "", "", now)
}

// NewStartingAt is NewStarting with the daemon's own location recorded, so the
// generation it opens names the home it belongs to.
func NewStartingAt(previous *State, listen string, provenance Provenance, ownerToken, wbHome, statePath string, now time.Time) State {
	state := newStarting(previous, listen, provenance, ownerToken, now)
	state.WBHome = wbHome
	state.StatePath = statePath
	return state
}

func newStarting(previous *State, listen string, provenance Provenance, ownerToken string, now time.Time) State {
	generation := uint64(1)
	queue := Queue{SchemaVersion: QueueSchemaVersion}
	if previous != nil {
		queue = previous.Queue
		if queue.SchemaVersion == 0 {
			queue.SchemaVersion = QueueSchemaVersion
		}
		generation = queue.Generation + 1
		if previous.Provenance.Executable != "" {
			from := previous.Provenance
			queue.HandoffFrom = &from
			handoffAt := now.UTC()
			queue.HandoffAt = &handoffAt
		}
	}
	queue.Generation = generation
	queue.Owner = provenance
	queue.OwnerToken = ownerToken
	return State{
		SchemaVersion: StateSchemaVersion,
		Status:        StatusStarting,
		OwnerToken:    ownerToken,
		Listen:        listen,
		Provenance:    provenance,
		Queue:         queue,
		StartedAt:     now.UTC(),
		UpdatedAt:     now.UTC(),
	}
}

func (s *State) MarkReady(pid int, now time.Time) {
	s.MarkReadyWithProcess(pid, time.Time{}, now)
}

// MarkReadyWithProcess records the process generation that is now serving. A
// zero processStartedAt records that the platform could not observe one.
func (s *State) MarkReadyWithProcess(pid int, processStartedAt time.Time, now time.Time) {
	s.Status = StatusReady
	s.PID = pid
	s.ProcessStartedAt = processStartedAt.UTC()
	s.UpdatedAt = now.UTC()
}

func (s *State) MarkStartingPID(pid int, now time.Time) {
	s.Status = StatusStarting
	s.PID = pid
	s.UpdatedAt = now.UTC()
}

func (s *State) MarkDraining(now time.Time) {
	s.Status = StatusDraining
	s.UpdatedAt = now.UTC()
}

func (s *State) MarkStopped(now time.Time) {
	s.Status = StatusStopped
	s.PID = 0
	s.UpdatedAt = now.UTC()
}

// MarkStoppedWithReason records a stop the daemon did not choose, keeping the
// reason readable after the process is gone.
func (s *State) MarkStoppedWithReason(reason string, now time.Time) {
	s.MarkStopped(now)
	s.StoppedReason = strings.TrimSpace(reason)
}

// Store reads and atomically replaces the local state record. Its directory
// and file permissions keep operation/queue metadata per-user on Unix; the
// Windows service adapter will use the current-user application-data ACL.
type Store struct{ Path string }

func (s Store) Load() (State, bool, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, false, fmt.Errorf("decode daemon state: %w", err)
	}
	if err := state.Valid(); err != nil {
		return State{}, false, err
	}
	return state, true, nil
}

func (s Store) Save(state State) error {
	if err := state.Valid(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.Path), ".daemon-state-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, s.Path)
}

// ProvenanceForExecutable produces exact local evidence for a running binary.
func ProvenanceForExecutable(executable, version, revision, built string) (Provenance, error) {
	path, err := filepath.EvalSymlinks(executable)
	if err == nil {
		executable = path
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		return Provenance{}, fmt.Errorf("read WB executable %s: %w", executable, err)
	}
	digest := sha256.Sum256(data)
	return Provenance{Executable: executable, SHA256: hex.EncodeToString(digest[:]), Version: version, Revision: revision, Built: built}, nil
}
