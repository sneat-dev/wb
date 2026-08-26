// Package sessionpark stores a complete, durable checkpoint for a parked WB
// session. It deliberately contains no Git mutation or transport logic.
package sessionpark

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"golang.org/x/sys/unix"
)

const (
	SchemaVersion        = 1
	MaxContinuationBytes = 64 << 10
)

type Status string

const (
	StatusParked  Status = "parked"
	StatusResumed Status = "resumed"
)

// Worktree is exact local evidence at park time. Dirty content is intentionally
// not captured; it remains in place for local resume and fails closed remotely.
type Worktree struct {
	Repository    string `json:"repository"`
	CanonicalDir  string `json:"canonical_dir,omitempty"`
	WorktreeDir   string `json:"worktree_dir"`
	WorktreesRoot string `json:"worktrees_root,omitempty"`
	Branch        string `json:"branch"`
	Head          string `json:"head"`
	Dirty         bool   `json:"dirty"`
	Status        string `json:"status,omitempty"`
	RemoteHead    string `json:"remote_head,omitempty"`
}

type Bundle struct {
	SchemaVersion   int            `json:"schema_version"`
	ParkedSessionID string         `json:"parked_session_id"`
	Source          session.Record `json:"source"`
	Continuation    string         `json:"continuation"`
	Worktrees       []Worktree     `json:"worktrees"`
	ParkedAt        time.Time      `json:"parked_at"`
}

type Event struct {
	SchemaVersion int             `json:"schema_version"`
	Sequence      uint64          `json:"sequence"`
	Type          string          `json:"type"`
	At            time.Time       `json:"at"`
	Successor     *session.Record `json:"successor,omitempty"`
}

type State struct {
	Bundle    Bundle          `json:"bundle"`
	Events    []Event         `json:"events"`
	Status    Status          `json:"status"`
	Successor *session.Record `json:"successor,omitempty"`
}

type Store struct{ Root string }

func NewStore(root string) Store { return Store{Root: root} }

func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate parked session ID: %w", err)
	}
	return "park-" + hex.EncodeToString(b[:]), nil
}

func (s Store) Create(bundle Bundle) (Bundle, error) {
	if err := validateBundle(bundle); err != nil {
		return Bundle{}, err
	}
	dir := filepath.Join(s.Root, bundle.ParkedSessionID)
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return Bundle{}, err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return Bundle{}, fmt.Errorf("create parked session aggregate: %w", err)
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return Bundle{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "bundle.json"), append(raw, '\n'), 0o600); err != nil {
		return Bundle{}, err
	}
	if err := os.Mkdir(filepath.Join(dir, "events"), 0o755); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

// FindBySource returns the existing aggregate for a source declaration. It is
// used to repair a crash between aggregate publication and lifecycle marking,
// so retry never allocates a second parked identity.
func (s Store) FindBySource(wbSessionID string) (Bundle, bool, error) {
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return Bundle{}, false, nil
	}
	if err != nil {
		return Bundle{}, false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		state, loadErr := s.Load(entry.Name())
		if errors.Is(loadErr, os.ErrNotExist) {
			continue
		}
		if loadErr != nil {
			return Bundle{}, false, loadErr
		}
		if state.Bundle.Source.WBSessionID == wbSessionID {
			return state.Bundle, true, nil
		}
	}
	return Bundle{}, false, nil
}

func (s Store) Load(id string) (State, error) {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, `/\\`) {
		return State{}, fmt.Errorf("invalid parked session ID %q", id)
	}
	dir := filepath.Join(s.Root, id)
	raw, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		return State{}, fmt.Errorf("load parked session %s: %w", id, err)
	}
	var bundle Bundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return State{}, err
	}
	if err := validateBundle(bundle); err != nil {
		return State{}, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "events"))
	if err != nil {
		return State{}, err
	}
	state := State{Bundle: bundle, Status: StatusParked, Events: []Event{}}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		eventRaw, e := os.ReadFile(filepath.Join(dir, "events", entry.Name()))
		if e != nil {
			return State{}, e
		}
		var event Event
		if e = json.Unmarshal(eventRaw, &event); e != nil {
			return State{}, e
		}
		state.Events = append(state.Events, event)
		if event.Type == "resumed" {
			state.Status = StatusResumed
			state.Successor = event.Successor
		}
	}
	return state, nil
}

func (s Store) Resume(id string, successor session.Record, now time.Time) (State, error) {
	state, err := s.Load(id)
	if err != nil {
		return State{}, err
	}
	if state.Status == StatusResumed {
		// The durable first successor wins. A retry returns that immutable
		// lineage even when the caller has since restarted its local process.
		return state, nil
	}
	if successor.PID <= 0 || successor.WBSessionID == "" {
		return State{}, fmt.Errorf("successor must have a stable WB session ID and positive PID")
	}
	if err := os.MkdirAll(filepath.Join(s.Root, id), 0o755); err != nil {
		return State{}, err
	}
	lockPath := filepath.Join(s.Root, id, "resume.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return State{}, fmt.Errorf("open parked session resume fence: %w", err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return State{}, fmt.Errorf("lock parked session resume fence: %w", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	// Re-read under the fence; a concurrent successor may have won while this
	// caller was loading the aggregate above.
	state, err = s.Load(id)
	if err != nil {
		return State{}, err
	}
	if state.Status == StatusResumed {
		return state, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	event := Event{SchemaVersion: SchemaVersion, Sequence: uint64(len(state.Events) + 1), Type: "resumed", At: now.UTC(), Successor: &successor}
	raw, err := json.MarshalIndent(event, "", "  ")
	if err != nil {
		return State{}, err
	}
	eventsDir := filepath.Join(s.Root, id, "events")
	name := fmt.Sprintf("%020d.json", event.Sequence)
	file, err := os.OpenFile(filepath.Join(eventsDir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return s.Load(id)
	}
	if err != nil {
		return State{}, err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		_ = file.Close()
		return State{}, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return State{}, err
	}
	if err := file.Close(); err != nil {
		return State{}, err
	}
	return s.Load(id)
}

func validateBundle(bundle Bundle) error {
	if bundle.SchemaVersion != SchemaVersion {
		return fmt.Errorf("parked session schema_version %d unsupported; want %d", bundle.SchemaVersion, SchemaVersion)
	}
	if bundle.ParkedSessionID == "" || strings.ContainsAny(bundle.ParkedSessionID, `/\\`) {
		return fmt.Errorf("parked session ID is invalid")
	}
	if bundle.Source.PID <= 0 || bundle.Source.WBSessionID == "" {
		return fmt.Errorf("parked session source identity is incomplete")
	}
	if len([]byte(bundle.Continuation)) > MaxContinuationBytes {
		return fmt.Errorf("parked session continuation exceeds %d bytes", MaxContinuationBytes)
	}
	return nil
}

func EncodeBundle(bundle Bundle) ([]byte, error) {
	if err := validateBundle(bundle); err != nil {
		return nil, err
	}
	return json.MarshalIndent(bundle, "", "  ")
}
func EqualBundle(a, b Bundle) bool {
	ar, _ := EncodeBundle(a)
	br, _ := EncodeBundle(b)
	return bytes.Equal(ar, br)
}
