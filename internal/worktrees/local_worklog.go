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
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	events := make([]LocalWorkLogEvent, 0)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event LocalWorkLogEvent
		if err := json.Unmarshal(line, &event); err != nil {
			// Spec allows discarding only a torn final line.
			if scanner.Err() == nil && !scanner.Scan() {
				break
			}
			return nil, fmt.Errorf("parse local work-log event: %w", err)
		}
		if event.Version != 1 || event.Seq < 0 || event.Type == "" || event.ID == "" {
			return nil, fmt.Errorf("invalid local work-log event at seq %d", event.Seq)
		}
		if len(events) > 0 && event.Seq != events[len(events)-1].Seq+1 {
			return nil, fmt.Errorf("local work-log event sequence gap: saw %d after %d", event.Seq, events[len(events)-1].Seq)
		}
		if len(events) == 0 && event.Seq != 0 {
			return nil, fmt.Errorf("local work-log events must start at seq 0")
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read local work-log events: %w", err)
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
	if event.Version == 0 {
		event.Version = 1
	}
	if event.Version != 1 {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, fmt.Errorf("unsupported local work-log event version %d", event.Version)
	}
	if strings.TrimSpace(event.Type) == "" {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, fmt.Errorf("local work-log event type is required")
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	} else {
		event.At = event.At.UTC()
	}

	directory, err := openLocalWorkLogDir(worktree, true)
	if err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}
	defer func() { _ = directory.Close() }()

	existing, err := readLocalEventsAt(directory)
	if err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}
	event.Seq = len(existing)
	if event.ID == "" {
		event.ID = localEventID(existing, event)
	}
	for _, prior := range existing {
		if prior.ID == event.ID {
			projection, err := rebuildLocalProjection(existing)
			return prior, projection, err
		}
		if prior.Seq == event.Seq {
			return LocalWorkLogEvent{}, LocalWorkLogProjection{}, fmt.Errorf("duplicate local work-log sequence %d", event.Seq)
		}
	}

	line, err := json.Marshal(event)
	if err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, fmt.Errorf("encode local work-log event: %w", err)
	}
	line = append(line, '\n')
	if err := appendBytesAt(directory, localWorkLogEventsName, line, 0o600); err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}
	if err := appendBytesAt(directory, localWorkLogOutboxName, line, 0o600); err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}

	all := append(existing, event)
	projection, err := rebuildLocalProjection(all)
	if err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
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
	if err := writeJSONAtomicAt(directory, localWorkLogProjectionName, projection, 0o600); err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}
	return event, projection, nil
}

func readLocalEventsAt(directory *os.File) ([]LocalWorkLogEvent, error) {
	content, err := readBytesAt(directory, localWorkLogEventsName)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseLocalEvents(content)
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

func appendBytesAt(directory *os.File, name string, content []byte, mode os.FileMode) error {
	if directory == nil || strings.Contains(name, "/") || name == "" || name == "." || name == ".." {
		return fmt.Errorf("unsafe append filename %q", name)
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_APPEND|unix.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return fmt.Errorf("open %s for append: %w", name, err)
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { _ = file.Close() }()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return directory.Sync()
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
