package worktreeclaims

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sneat-dev/wb/internal/worktreejournal"
)

type LocalJournalIdentity struct {
	EffortID, RunID, ClaimID, Lifecycle string
}

// LocalJournalPorts binds custody, journal storage and identity lookup to one
// operation. The journal Store remains the authority for append and replay.
type LocalJournalPorts struct {
	EnsureExclude        func(string) error
	OpenDirectory        func(string, bool) (*os.File, error)
	ReadEvents           func(string) ([]worktreejournal.LocalWorkLogEvent, error)
	ReadBytesAt          func(*os.File, string) ([]byte, error)
	ParseEvents          func([]byte) ([]worktreejournal.LocalWorkLogEvent, error)
	EnsureCustody        func(string)
	Lock                 func(*os.File) (func(), error)
	AppendUnderLock      func(string, *os.File, worktreejournal.LocalWorkLogEvent) (worktreejournal.LocalWorkLogEvent, worktreejournal.LocalWorkLogProjection, error)
	RebuildProjection    func([]worktreejournal.LocalWorkLogEvent) (worktreejournal.LocalWorkLogProjection, error)
	ReadManifestIdentity func(string) (LocalJournalIdentity, error)
	ReadHybridProjection func(string) (LocalJournalIdentity, error)
	Git                  func(context.Context, string, ...string) (string, error)
}

func (p LocalJournalPorts) OpenLocalWorkLogDir(worktree string, create bool) (*os.File, error) {
	if err := p.EnsureExclude(worktree); err != nil {
		return nil, err
	}
	return p.OpenDirectory(worktree, create)
}

// ReadLocalEventsForInspection accepts only the known historical terminal
// version-zero handoff after a valid journal prefix; append remains strict.
func (p LocalJournalPorts) ReadLocalEventsForInspection(worktree string, acceptHistorical func(worktreejournal.LocalWorkLogEvent) bool) ([]worktreejournal.LocalWorkLogEvent, bool, error) {
	events, err := p.ReadEvents(worktree)
	if err == nil {
		return events, false, nil
	}
	directory, openErr := p.OpenLocalWorkLogDir(worktree, false)
	if openErr != nil {
		return nil, false, err
	}
	defer func() { _ = directory.Close() }()
	content, readErr := p.ReadBytesAt(directory, worktreejournal.EventsName)
	if readErr != nil {
		return nil, false, err
	}
	parts := bytes.Split(content, []byte{'\n'})
	if len(parts) < 2 || len(content) == 0 || content[len(content)-1] != '\n' {
		return nil, false, err
	}
	last := bytes.TrimSpace(parts[len(parts)-2])
	var historical worktreejournal.LocalWorkLogEvent
	if json.Unmarshal(last, &historical) != nil || !HistoricalParkedCompletionShape(historical) ||
		acceptHistorical == nil || !acceptHistorical(historical) {
		return nil, false, err
	}
	validPrefix, prefixErr := p.ParseEvents(bytes.Join(parts[:len(parts)-2], []byte{'\n'}))
	if prefixErr != nil {
		return nil, false, err
	}
	return validPrefix, true, nil
}

func HistoricalParkedCompletionShape(event worktreejournal.LocalWorkLogEvent) bool {
	if event.Version != 0 || event.Type != worktreejournal.LocalEventHandoff || event.Result != "completed" ||
		event.Message != "parked successor proved live; target member custody completed" || len(event.Extra) == 0 {
		return false
	}
	for _, key := range []string{"resume_id", "parked_session_id", "member_id", "repository", "predecessor_wb_session_id", "successor_wb_session_id", "source_work_log_reference", "target_work_log_reference", "attempt_id"} {
		value, ok := event.Extra[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func (p LocalJournalPorts) AppendLocalEvent(worktree string, event worktreejournal.LocalWorkLogEvent) (worktreejournal.LocalWorkLogEvent, worktreejournal.LocalWorkLogProjection, error) {
	return p.AppendLocalEventWithCustody(worktree, event, true)
}
func (p LocalJournalPorts) AppendLocalEventWithoutCustody(worktree string, event worktreejournal.LocalWorkLogEvent) (worktreejournal.LocalWorkLogEvent, worktreejournal.LocalWorkLogProjection, error) {
	return p.AppendLocalEventWithCustody(worktree, event, false)
}
func (p LocalJournalPorts) AppendLocalEventWithCustody(worktree string, event worktreejournal.LocalWorkLogEvent, recordAmbientCustody bool) (worktreejournal.LocalWorkLogEvent, worktreejournal.LocalWorkLogProjection, error) {
	if event.Version == 0 {
		event.Version = 1
	}
	if event.Version != 1 {
		return worktreejournal.LocalWorkLogEvent{}, worktreejournal.LocalWorkLogProjection{}, fmt.Errorf("unsupported local work-log event version %d", event.Version)
	}
	if strings.TrimSpace(event.Type) == "" {
		return worktreejournal.LocalWorkLogEvent{}, worktreejournal.LocalWorkLogProjection{}, fmt.Errorf("local work-log event type is required")
	}
	if !event.At.IsZero() {
		event.At = event.At.UTC()
	}
	if recordAmbientCustody && event.Type != LocalEventOwner {
		p.EnsureCustody(worktree)
	}
	directory, err := p.OpenLocalWorkLogDir(worktree, true)
	if err != nil {
		return worktreejournal.LocalWorkLogEvent{}, worktreejournal.LocalWorkLogProjection{}, err
	}
	defer func() { _ = directory.Close() }()
	unlock, err := p.Lock(directory)
	if err != nil {
		return worktreejournal.LocalWorkLogEvent{}, worktreejournal.LocalWorkLogProjection{}, err
	}
	defer unlock()
	return p.AppendUnderLock(worktree, directory, event)
}

func (p LocalJournalPorts) ProjectLocalWorkLog(worktree string, events []worktreejournal.LocalWorkLogEvent) (worktreejournal.LocalWorkLogProjection, error) {
	projection, err := p.RebuildProjection(events)
	if err != nil {
		return worktreejournal.LocalWorkLogProjection{}, err
	}
	manifest, manifestErr := p.ReadManifestIdentity(worktree)
	if manifestErr == nil {
		projection.EffortID, projection.RunID, projection.ClaimID = manifest.EffortID, manifest.RunID, manifest.ClaimID
	}
	if hybrid, err := p.ReadHybridProjection(worktree); err == nil {
		projection.EffortID, projection.RunID, projection.ClaimID = hybrid.EffortID, hybrid.RunID, hybrid.ClaimID
		if hybrid.Lifecycle != "" {
			projection.Lifecycle = hybrid.Lifecycle
		}
	}
	return projection, nil
}

func (p LocalJournalPorts) ObserveLocalGit(ctx context.Context, worktree string) worktreejournal.LocalGitEvidence {
	evidence := worktreejournal.LocalGitEvidence{}
	if branch, err := p.Git(ctx, worktree, "branch", "--show-current"); err == nil {
		evidence.Branch = strings.TrimSpace(branch)
	}
	if head, err := p.Git(ctx, worktree, "rev-parse", "HEAD"); err == nil {
		evidence.Head = strings.TrimSpace(head)
	}
	if status, err := p.Git(ctx, worktree, "status", "--porcelain"); err == nil {
		trimmed := strings.TrimSpace(status)
		evidence.Status = trimmed
		evidence.Dirty = trimmed != ""
		sum := sha256.Sum256([]byte(trimmed))
		evidence.StatusSHA = hex.EncodeToString(sum[:])
	}
	return evidence
}
