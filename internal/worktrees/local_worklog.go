package worktrees

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	localWorkLogEventsName     = "events.jsonl"
	localWorkLogProjectionName = "projection.json"
	localWorkLogOutboxName     = "outbox.jsonl"
	localWorkLogLockName       = ".journal.lock"

	LocalEventInit        = "init"
	LocalEventSteer       = "steer"
	LocalEventCheckpoint  = "checkpoint"
	LocalEventRefresh     = "refresh"
	LocalEventRefreshNeed = "refresh_required"
	LocalEventIntegrate   = "integrate"
	LocalEventHandoff     = "handoff"
	LocalEventRecover     = "recover"
	LocalEventFinalize    = "finalize"
	LocalEventSyncAttempt = "sync_attempt"
	LocalEventArchive     = "archive"
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

func openLocalWorkLogDir(worktree string, create bool) (*os.File, error) {
	if err := ensureJournalExclude(worktree); err != nil {
		return nil, err
	}
	return openJournalSubdirectory(worktree, worklogDirectory, create)
}

func readLocalEvents(worktree string) ([]LocalWorkLogEvent, error) {
	directory, err := openLocalWorkLogDir(worktree, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	content, err := readBytesAt(directory, localWorkLogEventsName)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseLocalEvents(content)
}

func parseLocalEvents(content []byte) ([]LocalWorkLogEvent, error) {
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
		if err := validateLocalEventForSequence(event, events); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func readLocalProjection(worktree string) (LocalWorkLogProjection, error) {
	directory, err := openLocalWorkLogDir(worktree, false)
	if errors.Is(err, os.ErrNotExist) {
		return LocalWorkLogProjection{}, os.ErrNotExist
	}
	if err != nil {
		return LocalWorkLogProjection{}, err
	}
	defer func() { _ = directory.Close() }()
	var projection LocalWorkLogProjection
	if err := readJSONAt(directory, localWorkLogProjectionName, &projection); err != nil {
		return LocalWorkLogProjection{}, err
	}
	return projection, nil
}

func appendLocalEvent(worktree string, event LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
	return appendLocalEventWithCustody(worktree, event, true)
}

// appendLocalEventWithoutCustody is reserved for evidence whose owner was
// explicitly authenticated by a higher-level custody transaction. Recording
// the short-lived wb receiver process as an ambient owner would overwrite the
// successor/predecessor proof that transaction just established.
func appendLocalEventWithoutCustody(worktree string, event LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
	return appendLocalEventWithCustody(worktree, event, false)
}

func appendLocalEventWithCustody(worktree string, event LocalWorkLogEvent, recordAmbientCustody bool) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
	if event.Version == 0 {
		event.Version = 1
	}
	if event.Version != 1 {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, fmt.Errorf("unsupported local work-log event version %d", event.Version)
	}
	if strings.TrimSpace(event.Type) == "" {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, fmt.Errorf("local work-log event type is required")
	}
	if !event.At.IsZero() {
		event.At = event.At.UTC()
	}

	// Every worktree write funnels through here, which makes it the one place
	// that can keep the owner chain honest. Owner events are excluded, both to
	// avoid recursing and because they are the custody record itself.
	if recordAmbientCustody && event.Type != LocalEventOwner {
		ensureCustody(worktree)
	}

	directory, err := openLocalWorkLogDir(worktree, true)
	if err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}
	defer func() { _ = directory.Close() }()
	unlock, err := lockLocalWorkLog(directory)
	if err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}
	defer unlock()
	return appendLocalEventUnderLock(worktree, directory, event)
}

func appendLocalEventUnderLock(worktree string, directory *os.File, event LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
	existing, journalRepair, err := readLocalEventsForAppend(directory)
	if err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}
	if journalRepair {
		encoded, encodeErr := encodeLocalEvents(existing)
		if encodeErr != nil {
			return LocalWorkLogEvent{}, LocalWorkLogProjection{}, encodeErr
		}
		if err := writeBytesAtomicAt(directory, localWorkLogEventsName, encoded, 0o600); err != nil {
			return LocalWorkLogEvent{}, LocalWorkLogProjection{}, fmt.Errorf("repair torn local work-log journal: %w", err)
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
			if !sameLocalEvent(prior, event) {
				return LocalWorkLogEvent{}, LocalWorkLogProjection{}, fmt.Errorf("local work-log event ID %q already denotes different immutable evidence", requestedID)
			}
			projection, repairErr := repairLocalEventDerivatives(worktree, directory, existing, prior)
			return prior, projection, repairErr
		}
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	event.Seq = len(existing)
	if requestedID == "" {
		event.ID = localEventID(existing, event)
	} else {
		event.ID = requestedID
	}

	all := append(existing, event)
	journal, err := encodeLocalEvents(all)
	if err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}
	// The descriptor lock serializes the read/modify/rename transaction. A
	// whole-file atomic replacement means a crash can expose either the old
	// journal or the complete new event, never bytes appended behind a torn
	// suffix and never a lost concurrently appended neighbour.
	if err := writeBytesAtomicAt(directory, localWorkLogEventsName, journal, 0o600); err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}
	projection, err := repairLocalEventDerivatives(worktree, directory, all, event)
	if err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}
	return event, projection, nil
}

func sameLocalEvent(first, second LocalWorkLogEvent) bool {
	firstRaw, firstErr := json.Marshal(first)
	secondRaw, secondErr := json.Marshal(second)
	return firstErr == nil && secondErr == nil && bytes.Equal(firstRaw, secondRaw)
}

// repairLocalEventDerivatives makes the journal event the sole source of
// truth. A crash after events.jsonl but before either derived write is
// therefore repaired by replaying the exact explicit event ID.
func repairLocalEventDerivatives(worktree string, directory *os.File, events []LocalWorkLogEvent, event LocalWorkLogEvent) (LocalWorkLogProjection, error) {
	if err := repairLocalOutbox(directory, events); err != nil {
		return LocalWorkLogProjection{}, err
	}
	projection, err := projectLocalWorkLog(worktree, events)
	if err != nil {
		return LocalWorkLogProjection{}, err
	}
	if err := writeJSONAtomicAt(directory, localWorkLogProjectionName, projection, 0o600); err != nil {
		return LocalWorkLogProjection{}, err
	}
	return projection, nil
}

func repairLocalOutbox(directory *os.File, events []LocalWorkLogEvent) error {
	content, err := readBytesAt(directory, localWorkLogOutboxName)
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
	seen := make(map[string]struct{}, len(events))
	if len(content) != 0 {
		outbox, _, parseErr := parseLocalEventsForRepair(content)
		if parseErr != nil {
			return fmt.Errorf("parse local work-log outbox: %w", parseErr)
		}
		for _, existing := range outbox {
			journalEvent, found := journalByID[existing.ID]
			if !found {
				return fmt.Errorf("local work-log outbox event ID %q has no journal authority", existing.ID)
			}
			if !sameLocalEvent(existing, journalEvent) {
				return fmt.Errorf("local work-log outbox event ID %q denotes different immutable evidence", existing.ID)
			}
			if _, duplicate := seen[existing.ID]; duplicate {
				return fmt.Errorf("local work-log outbox event ID %q occurs more than once", existing.ID)
			}
			seen[existing.ID] = struct{}{}
		}
	}
	encoded, err := encodeLocalEvents(events)
	if err != nil {
		return fmt.Errorf("encode local work-log outbox: %w", err)
	}
	if bytes.Equal(content, encoded) {
		return nil
	}
	return writeBytesAtomicAt(directory, localWorkLogOutboxName, encoded, 0o600)
}

func projectLocalWorkLog(worktree string, events []LocalWorkLogEvent) (LocalWorkLogProjection, error) {
	projection, err := rebuildLocalProjection(events)
	if err != nil {
		return LocalWorkLogProjection{}, err
	}
	manifest, manifestErr := ReadManifest(worktree)
	if manifestErr == nil {
		projection.EffortID = manifest.EffortID
		projection.RunID = manifest.RunID
		projection.ClaimID = manifest.ClaimID
	}
	if hybrid, err := readWorkLogProjection(worktree); err == nil {
		projection.EffortID = hybrid.EffortID
		projection.RunID = hybrid.RunID
		projection.ClaimID = hybrid.ClaimID
		if hybrid.Lifecycle != "" {
			projection.Lifecycle = hybrid.Lifecycle
		}
	}
	return projection, nil
}

// repairCurrentLocalProjection replays the authoritative local journal after
// another custody layer changes the hybrid Work Log projection. Session
// handoff preparation writes the hybrid pointer last, then calls this helper
// so the user-facing local cache cannot remain identity-poor after a crash.
func repairCurrentLocalProjection(worktree string) (LocalWorkLogProjection, error) {
	directory, err := openLocalWorkLogDir(worktree, true)
	if err != nil {
		return LocalWorkLogProjection{}, err
	}
	defer func() { _ = directory.Close() }()
	unlock, err := lockLocalWorkLog(directory)
	if err != nil {
		return LocalWorkLogProjection{}, err
	}
	defer unlock()
	events, repair, err := readLocalEventsForAppend(directory)
	if err != nil {
		return LocalWorkLogProjection{}, err
	}
	encoded, err := encodeLocalEvents(events)
	if err != nil {
		return LocalWorkLogProjection{}, err
	}
	if repair {
		if err := writeBytesAtomicAt(directory, localWorkLogEventsName, encoded, 0o600); err != nil {
			return LocalWorkLogProjection{}, err
		}
	}
	if err := repairLocalOutbox(directory, events); err != nil {
		return LocalWorkLogProjection{}, err
	}
	projection, err := projectLocalWorkLog(worktree, events)
	if err != nil {
		return LocalWorkLogProjection{}, err
	}
	if err := writeJSONAtomicAt(directory, localWorkLogProjectionName, projection, 0o600); err != nil {
		return LocalWorkLogProjection{}, err
	}
	return projection, nil
}

func readLocalEventsForAppend(directory *os.File) ([]LocalWorkLogEvent, bool, error) {
	content, err := readBytesAt(directory, localWorkLogEventsName)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return parseLocalEventsForRepair(content)
}

// parseLocalEventsForRepair accepts only one crash shape: an unterminated
// final record. Malformed completed lines and every non-final corruption are
// immutable-evidence conflicts and remain hard failures.
func parseLocalEventsForRepair(content []byte) ([]LocalWorkLogEvent, bool, error) {
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
		if validationErr := validateLocalEventForSequence(event, events); validationErr != nil {
			return nil, false, validationErr
		}
		events = append(events, event)
	}
	return events, len(content) > 0 && !terminated, nil
}

func validateLocalEventForSequence(event LocalWorkLogEvent, existing []LocalWorkLogEvent) error {
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

func encodeLocalEvents(events []LocalWorkLogEvent) ([]byte, error) {
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

func lockLocalWorkLog(directory *os.File) (func(), error) {
	fd, err := unix.Openat(int(directory.Fd()), localWorkLogLockName, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(directory.Fd()), localWorkLogLockName, unix.O_RDWR|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("open local work-log journal lock: %w", err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("lock local work-log journal: %w", err)
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}, nil
}

func rebuildLocalProjection(events []LocalWorkLogEvent) (LocalWorkLogProjection, error) {
	projection := LocalWorkLogProjection{
		Version:   1,
		Lifecycle: "active",
		UpdatedAt: time.Now().UTC(),
	}
	if len(events) == 0 {
		return projection, nil
	}
	last := events[len(events)-1]
	projection.LastSeq = last.Seq
	projection.LastEventID = last.ID
	projection.LastType = last.Type
	projection.LastMessage = last.Message
	projection.LastNextAction = last.NextAction
	projection.UpdatedAt = last.At
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
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
		if projection.Conflict == "" && event.Conflict != "" {
			projection.Conflict = event.Conflict
		}
	}
	return projection, nil
}

func localEventID(existing []LocalWorkLogEvent, event LocalWorkLogEvent) string {
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

func observeLocalGit(ctx context.Context, worktree string) LocalGitEvidence {
	evidence := LocalGitEvidence{}
	if branch, err := git(ctx, worktree, "branch", "--show-current"); err == nil {
		evidence.Branch = strings.TrimSpace(branch)
	}
	if head, err := git(ctx, worktree, "rev-parse", "HEAD"); err == nil {
		evidence.Head = strings.TrimSpace(head)
	}
	if status, err := git(ctx, worktree, "status", "--porcelain"); err == nil {
		trimmed := strings.TrimSpace(status)
		evidence.Status = trimmed
		evidence.Dirty = trimmed != ""
		sum := sha256.Sum256([]byte(trimmed))
		evidence.StatusSHA = hex.EncodeToString(sum[:])
	}
	return evidence
}

func countLocalOutbox(worktree string) (int, error) {
	directory, err := openLocalWorkLogDir(worktree, false)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = directory.Close() }()
	content, err := readBytesAt(directory, localWorkLogOutboxName)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
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
