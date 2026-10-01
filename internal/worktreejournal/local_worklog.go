// Package worktreejournal owns the local append-only event journal and its repairable derivatives.
package worktreejournal

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// Store binds a local journal to facade-owned directory setup and projection enrichment.
// A Store is a value so failure seams stay scoped to one operation or test.
type Store struct {
	OpenDirectory   func(worktree string, create bool) (*os.File, error)
	Project         func(worktree string, events []LocalWorkLogEvent) (LocalWorkLogProjection, error)
	WriteJournal    func(directory *os.File, name string, content []byte, mode os.FileMode) error
	ReadFile        func(directory *os.File, name string) ([]byte, error)
	WriteFile       func(directory *os.File, name string, content []byte, mode os.FileMode) error
	WriteProjection func(directory *os.File, name string, value any, mode os.FileMode) error
	Flock           func(fd int, how int) error
}

func (s Store) readFile(directory *os.File, name string) ([]byte, error) {
	if s.ReadFile != nil {
		return s.ReadFile(directory, name)
	}
	return filewrite.ReadAt(directory, name)
}
func (s Store) writeFile(directory *os.File, name string, content []byte, mode os.FileMode) error {
	if s.WriteFile != nil {
		return s.WriteFile(directory, name, content, mode)
	}
	return filewrite.WriteBytesAtomicAt(directory, name, content, mode)
}
func (s Store) writeProjection(directory *os.File, name string, value any, mode os.FileMode) error {
	if s.WriteProjection != nil {
		return s.WriteProjection(directory, name, value, mode)
	}
	return filewrite.WriteJSONAtomicAt(directory, name, value, mode)
}
func (s Store) flock(fd int, how int) error {
	if s.Flock != nil {
		return s.Flock(fd, how)
	}
	return unix.Flock(fd, how)
}

func (s Store) writeJournal(directory *os.File, name string, content []byte, mode os.FileMode) error {
	if s.WriteJournal != nil {
		return s.WriteJournal(directory, name, content, mode)
	}
	return filewrite.WriteBytesAtomicAt(directory, name, content, mode)
}

const (
	EventsName     = "events.jsonl"
	ProjectionName = "projection.json"
	OutboxName     = "outbox.jsonl"
	LockName       = ".journal.lock"

	LocalEventInit             = "init"
	LocalEventSteer            = "steer"
	LocalEventCheckpoint       = "checkpoint"
	LocalEventRefresh          = "refresh"
	LocalEventRefreshNeed      = "refresh_required"
	LocalEventIntegrate        = "integrate"
	LocalEventHandoff          = "handoff"
	LocalEventRecover          = "recover"
	LocalEventBranchReconciled = "branch_reconciled"
	LocalEventFinalize         = "finalize"
	LocalEventSyncAttempt      = "sync_attempt"
	LocalEventArchive          = "archive"
)

// LocalWorkLogEvent is one append-only journal record under .wb/local/worklog/.
type LocalWorkLogEvent struct {
	Version    int                  `json:"version"`
	Seq        int                  `json:"seq"`
	ID         string               `json:"id"`
	Type       string               `json:"type"`
	At         time.Time            `json:"at"`
	Message    string               `json:"message,omitempty"`
	NextAction string               `json:"next_action,omitempty"`
	Prompt     string               `json:"prompt,omitempty"`
	PromptSHA  string               `json:"prompt_sha256,omitempty"`
	Git        *LocalGitEvidence    `json:"git,omitempty"`
	Target     *LocalTargetEvidence `json:"target,omitempty"`
	Usage      *LocalUsageEvidence  `json:"usage,omitempty"`
	Result     string               `json:"result,omitempty"`
	Conflict   string               `json:"conflict,omitempty"`
	Owner      *OwnerRegistration   `json:"owner,omitempty"`
	Extra      map[string]any       `json:"extra,omitempty"`
}

// LocalGitEvidence is the public Git fingerprint recorded on checkpoints.
type LocalGitEvidence struct {
	Branch    string `json:"branch,omitempty"`
	Head      string `json:"head,omitempty"`
	Dirty     bool   `json:"dirty"`
	StatusSHA string `json:"status_sha256,omitempty"`
	Status    string `json:"status,omitempty"`
}

// LocalTargetEvidence records refresh/integrate observations.
type LocalTargetEvidence struct {
	Ref       string    `json:"ref,omitempty"`
	SHA       string    `json:"sha,omitempty"`
	FetchedAt time.Time `json:"fetched_at,omitempty"`
	Ahead     int       `json:"ahead"`
	Behind    int       `json:"behind"`
	Strategy  string    `json:"strategy,omitempty"`
}

// LocalUsageEvidence is optional nullable token usage.
type LocalUsageEvidence struct {
	Discriminator string   `json:"discriminator"`
	InputTokens   *int64   `json:"input_tokens,omitempty"`
	OutputTokens  *int64   `json:"output_tokens,omitempty"`
	TotalTokens   *int64   `json:"total_tokens,omitempty"`
	EstimatedCost *float64 `json:"estimated_cost,omitempty"`
	Currency      string   `json:"currency,omitempty"`
	ProviderRef   string   `json:"provider_ref,omitempty"`
}

// LocalWorkLogProjection is the derived current-state cache for the local journal.
type LocalWorkLogProjection struct {
	Version        int                  `json:"version"`
	EffortID       string               `json:"effort_id,omitempty"`
	RunID          string               `json:"run_id,omitempty"`
	ClaimID        string               `json:"claim_id,omitempty"`
	Lifecycle      string               `json:"lifecycle"`
	LastSeq        int                  `json:"last_seq"`
	LastEventID    string               `json:"last_event_id,omitempty"`
	LastType       string               `json:"last_type,omitempty"`
	LastMessage    string               `json:"last_message,omitempty"`
	LastNextAction string               `json:"last_next_action,omitempty"`
	LastCheckpoint *LocalGitEvidence    `json:"last_checkpoint,omitempty"`
	LastTarget     *LocalTargetEvidence `json:"last_target,omitempty"`
	Conflict       string               `json:"conflict,omitempty"`
	UpdatedAt      time.Time            `json:"updated_at"`
}

type OwnerRegistration struct {
	Agent     string `json:"agent,omitempty"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Initiator string `json:"initiator,omitempty"`
	// PID is the declared agent session's process, never WB's own. WB is a
	// short-lived CLI: its PID is dead moments after it would be written, and
	// once recycled it would report an abandoned worktree as active. An
	// absent PID therefore reads as unknown, which is the honest answer.
	PID int `json:"pid,omitempty"`
	// WBVersion and Command are always populated, because WB always knows
	// them. They give a worktree provenance even when no agent identity was
	// declared.
	WBVersion string    `json:"wb_version,omitempty"`
	Command   string    `json:"command,omitempty"`
	At        time.Time `json:"at"`
}

func (s Store) ReadLocalEvents(worktree string) ([]LocalWorkLogEvent, error) {
	content, err := s.ReadLocalWorkLogBytes(worktree, EventsName)
	if err != nil || content == nil {
		return nil, err
	}
	return s.ParseLocalEvents(content)
}

func (s Store) ReadLocalWorkLogBytes(worktree, name string) ([]byte, error) {
	directory, err := s.OpenDirectory(worktree, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	content, err := s.readFile(directory, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return content, nil
}

func (s Store) ParseLocalEvents(content []byte) ([]LocalWorkLogEvent, error) {
	events := make([]LocalWorkLogEvent, 0)
	for _, raw := range bytes.Split(content, []byte{'\n'}) {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		var event LocalWorkLogEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("parse local work-log event: %w", err)
		}
		if err := s.ValidateLocalEventForSequence(event, events); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (s Store) ReadLocalProjection(worktree string) (LocalWorkLogProjection, error) {
	directory, err := s.OpenDirectory(worktree, false)
	if errors.Is(err, os.ErrNotExist) {
		return LocalWorkLogProjection{}, os.ErrNotExist
	}
	if err != nil {
		return LocalWorkLogProjection{}, err
	}
	defer func() { _ = directory.Close() }()
	var projection LocalWorkLogProjection
	if err := filewrite.ReadJSONAt(directory, ProjectionName, &projection); err != nil {
		return LocalWorkLogProjection{}, err
	}
	return projection, nil
}

func (s Store) AppendLocalEventUnderLock(worktree string, directory *os.File, event LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
	existing, journalRepair, err := s.ReadLocalEventsForAppend(directory)
	if err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}
	if journalRepair {
		if err := s.RewriteLocalEventJournal(directory, existing); err != nil {
			return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
		}
	}
	requestedID := strings.TrimSpace(event.ID)
	for _, prior := range existing {
		if requestedID != "" && prior.ID == requestedID {
			event.ID = requestedID
			event.Seq = prior.Seq
			if event.At.IsZero() {
				event.At = prior.At
			}
			if !s.SameLocalEvent(prior, event) {
				return LocalWorkLogEvent{}, LocalWorkLogProjection{}, fmt.Errorf("local work-log event ID %q already denotes different immutable evidence", requestedID)
			}
			projection, repairErr := s.RepairLocalEventDerivatives(worktree, directory, existing)
			return prior, projection, repairErr
		}
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	event.Seq = len(existing)
	if requestedID == "" {
		event.ID = s.LocalEventID(existing, event)
	} else {
		event.ID = requestedID
	}

	all := append(existing, event)
	journal, err := s.EncodeLocalEvents(all)
	if err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}
	// The descriptor lock serializes the read/modify/rename transaction. A
	// whole-file atomic replacement means a crash can expose either the old
	// journal or the complete new event, never bytes appended behind a torn
	// suffix and never a lost concurrently appended neighbour.
	if err := s.writeFile(directory, EventsName, journal, 0o600); err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}
	projection, err := s.RepairLocalEventDerivatives(worktree, directory, all)
	if err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}
	return event, projection, nil
}

func (s Store) SameLocalEvent(first, second LocalWorkLogEvent) bool {
	firstRaw, firstErr := json.Marshal(first)
	secondRaw, secondErr := json.Marshal(second)
	return firstErr == nil && secondErr == nil && bytes.Equal(firstRaw, secondRaw)
}

func (s Store) RepairLocalEventDerivatives(worktree string, directory *os.File, events []LocalWorkLogEvent) (LocalWorkLogProjection, error) {
	if err := s.RepairLocalOutbox(directory, events); err != nil {
		return LocalWorkLogProjection{}, err
	}
	projection, err := s.Project(worktree, events)
	if err != nil {
		return LocalWorkLogProjection{}, err
	}
	if err := s.writeProjection(directory, ProjectionName, projection, 0o600); err != nil {
		return LocalWorkLogProjection{}, err
	}
	return projection, nil
}

func (s Store) RepairLocalOutbox(directory *os.File, events []LocalWorkLogEvent) error {
	content, err := s.readFile(directory, OutboxName)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	journalByID := make(map[string]LocalWorkLogEvent, len(events))
	for _, event := range events {
		if _, duplicate := journalByID[event.ID]; duplicate {
			return fmt.Errorf("local work-log journal event ID %q occurs more than once", event.ID)
		}
		journalByID[event.ID] = event
	}
	if len(content) != 0 {
		outbox, _, parseErr := s.ParseLocalEventsForRepair(content)
		if parseErr != nil {
			return fmt.Errorf("parse local work-log outbox: %w", parseErr)
		}
		for _, existing := range outbox {
			journalEvent, found := journalByID[existing.ID]
			if !found {
				return fmt.Errorf("local work-log outbox event ID %q has no journal authority", existing.ID)
			}
			if !s.SameLocalEvent(existing, journalEvent) {
				return fmt.Errorf("local work-log outbox event ID %q denotes different immutable evidence", existing.ID)
			}
		}
	}
	encoded, err := s.EncodeLocalEvents(events)
	if err != nil {
		return fmt.Errorf("encode local work-log outbox: %w", err)
	}
	if bytes.Equal(content, encoded) {
		return nil
	}
	return s.writeFile(directory, OutboxName, encoded, 0o600)
}

func (s Store) RepairCurrentLocalProjection(worktree string) (LocalWorkLogProjection, error) {
	directory, err := s.OpenDirectory(worktree, true)
	if err != nil {
		return LocalWorkLogProjection{}, err
	}
	defer func() { _ = directory.Close() }()
	unlock, err := s.LockLocalWorkLog(directory)
	if err != nil {
		return LocalWorkLogProjection{}, err
	}
	defer unlock()
	events, repair, err := s.ReadLocalEventsForAppend(directory)
	if err != nil {
		return LocalWorkLogProjection{}, err
	}
	if repair {
		if err := s.RewriteLocalEventJournal(directory, events); err != nil {
			return LocalWorkLogProjection{}, err
		}
	}
	return s.RepairLocalEventDerivatives(worktree, directory, events)
}

func (s Store) RewriteLocalEventJournal(directory *os.File, events []LocalWorkLogEvent) error {
	encoded, err := s.EncodeLocalEvents(events)
	if err != nil {
		return err
	}
	if err := s.writeJournal(directory, EventsName, encoded, 0o600); err != nil {
		return fmt.Errorf("repair torn local work-log journal: %w", err)
	}
	return nil
}

func (s Store) ReadLocalEventsForAppend(directory *os.File) ([]LocalWorkLogEvent, bool, error) {
	content, err := s.readFile(directory, EventsName)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return s.ParseLocalEventsForRepair(content)
}

func (s Store) ParseLocalEventsForRepair(content []byte) ([]LocalWorkLogEvent, bool, error) {
	parts := bytes.Split(content, []byte{'\n'})
	terminated := len(content) == 0 || content[len(content)-1] == '\n'
	events := make([]LocalWorkLogEvent, 0, len(parts))
	for index, raw := range parts {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		var event LocalWorkLogEvent
		decodeErr := json.Unmarshal(line, &event)
		if decodeErr != nil {
			if index == len(parts)-1 && !terminated {
				return events, true, nil
			}
			return nil, false, fmt.Errorf("parse local work-log event: %w", decodeErr)
		}
		if validationErr := s.ValidateLocalEventForSequence(event, events); validationErr != nil {
			return nil, false, validationErr
		}
		events = append(events, event)
	}
	return events, len(content) > 0 && !terminated, nil
}

func (s Store) ValidateLocalEventForSequence(event LocalWorkLogEvent, existing []LocalWorkLogEvent) error {
	if event.Version != 1 || event.Seq < 0 || event.Type == "" || event.ID == "" {
		return fmt.Errorf("invalid local work-log event at seq %d", event.Seq)
	}
	wantSeq := len(existing)
	if event.Seq != wantSeq {
		if wantSeq == 0 {
			return fmt.Errorf("local work-log events must start at seq 0")
		}
		return fmt.Errorf("local work-log event sequence gap: saw %d after %d", event.Seq, existing[len(existing)-1].Seq)
	}
	return nil
}

func (s Store) EncodeLocalEvents(events []LocalWorkLogEvent) ([]byte, error) {
	var encoded bytes.Buffer
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			return nil, fmt.Errorf("encode local work-log event: %w", err)
		}
		encoded.Write(line)
		encoded.WriteByte('\n')
	}
	return encoded.Bytes(), nil
}

func (s Store) LockLocalWorkLog(directory *os.File) (func(), error) {
	fd, err := unix.Openat(int(directory.Fd()), LockName, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(directory.Fd()), LockName, unix.O_RDWR|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("open local work-log journal lock: %w", err)
	}
	if err := s.flock(fd, unix.LOCK_EX); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("lock local work-log journal: %w", err)
	}
	return func() {
		_ = s.flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}, nil
}

func (s Store) RebuildLocalProjection(events []LocalWorkLogEvent) (LocalWorkLogProjection, error) {
	return ProjectionFromEvents(events), nil
}

// ProjectionFromEvents folds local events without I/O or validation. Storage
// and identity enrichment remain the responsibility of its callers.
func ProjectionFromEvents(events []LocalWorkLogEvent) LocalWorkLogProjection {
	projection := LocalWorkLogProjection{
		Version:   1,
		Lifecycle: "active",
		UpdatedAt: time.Now().UTC(),
	}
	if len(events) == 0 {
		return projection
	}
	last := events[len(events)-1]
	projection.LastSeq = last.Seq
	projection.LastEventID = last.ID
	projection.LastType = last.Type
	projection.LastMessage = last.Message
	projection.LastNextAction = last.NextAction
	projection.UpdatedAt = last.At
	resolvedFetchFailure := false
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if (event.Type == LocalEventRefresh || event.Type == LocalEventRefreshNeed) && event.Conflict == "" && event.Target != nil && event.Target.SHA != "" {
			resolvedFetchFailure = true
		}
		switch event.Type {
		case LocalEventCheckpoint:
			if projection.LastCheckpoint == nil && event.Git != nil {
				copyGit := *event.Git
				projection.LastCheckpoint = &copyGit
			}
		case LocalEventRefresh, LocalEventIntegrate:
			if projection.LastTarget == nil && event.Target != nil {
				copyTarget := *event.Target
				projection.LastTarget = &copyTarget
			}
		case LocalEventFinalize, LocalEventArchive:
			projection.Lifecycle = "terminal"
		case LocalEventHandoff:
			if event.Result == "offered" {
				projection.Lifecycle = "handoff"
			}
		}
		if projection.Conflict == "" && event.Conflict != "" && (event.Conflict != "fetch_failed" || !resolvedFetchFailure) {
			projection.Conflict = event.Conflict
		}
	}
	return projection
}

func (s Store) LocalEventID(existing []LocalWorkLogEvent, event LocalWorkLogEvent) string {
	payload := struct {
		Type    string `json:"type"`
		Message string `json:"message,omitempty"`
		Prompt  string `json:"prompt_sha256,omitempty"`
		Git     any    `json:"git,omitempty"`
		Target  any    `json:"target,omitempty"`
		Result  string `json:"result,omitempty"`
	}{
		Type: event.Type, Message: event.Message, Prompt: event.PromptSHA,
		Git: event.Git, Target: event.Target, Result: event.Result,
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\n%s\n%s", len(existing), event.Type, encoded)))
	return hex.EncodeToString(sum[:])
}

func (s Store) CountLocalOutbox(worktree string) (int, error) {
	content, err := s.ReadLocalWorkLogBytes(worktree, OutboxName)
	if err != nil || content == nil {
		return 0, err
	}
	count := 0
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) > 0 {
			count++
		}
	}
	return count, scanner.Err()
}
