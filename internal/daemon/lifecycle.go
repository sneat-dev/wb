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
	"time"
)

const (
	StateSchemaVersion = 1
	QueueSchemaVersion = 1
)

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
}

func (s State) Valid() error {
	if s.SchemaVersion != StateSchemaVersion {
		return fmt.Errorf("unsupported daemon state schema %d", s.SchemaVersion)
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
	s.Status = StatusReady
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
	defer os.Remove(temporaryName)
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
