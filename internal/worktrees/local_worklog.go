package worktrees

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

const (
	localWorkLogEventsName     = worktreejournal.EventsName
	localWorkLogProjectionName = worktreejournal.ProjectionName
	localWorkLogOutboxName     = worktreejournal.OutboxName
	localWorkLogLockName       = worktreejournal.LockName
	LocalEventInit             = worktreejournal.LocalEventInit
	LocalEventSteer            = worktreejournal.LocalEventSteer
	LocalEventCheckpoint       = worktreejournal.LocalEventCheckpoint
	LocalEventRefresh          = worktreejournal.LocalEventRefresh
	LocalEventRefreshNeed      = worktreejournal.LocalEventRefreshNeed
	LocalEventIntegrate        = worktreejournal.LocalEventIntegrate
	LocalEventHandoff          = worktreejournal.LocalEventHandoff
	LocalEventRecover          = worktreejournal.LocalEventRecover
	LocalEventBranchReconciled = worktreejournal.LocalEventBranchReconciled
	LocalEventFinalize         = worktreejournal.LocalEventFinalize
	LocalEventSyncAttempt      = worktreejournal.LocalEventSyncAttempt
	LocalEventArchive          = worktreejournal.LocalEventArchive
)

type LocalWorkLogEvent = worktreejournal.LocalWorkLogEvent
type LocalGitEvidence = worktreejournal.LocalGitEvidence
type LocalTargetEvidence = worktreejournal.LocalTargetEvidence
type LocalUsageEvidence = worktreejournal.LocalUsageEvidence
type LocalWorkLogProjection = worktreejournal.LocalWorkLogProjection

func localJournalStore() worktreejournal.Store {
	return worktreejournal.Store{OpenDirectory: openLocalWorkLogDir, Project: projectLocalWorkLog}
}

func openLocalWorkLogDir(worktree string, create bool) (*os.File, error) {
	if err := ensureJournalExclude(worktree); err != nil {
		return nil, err
	}
	return openJournalSubdirectory(worktree, worklogDirectory, create)
}

func readLocalEvents(worktree string) ([]LocalWorkLogEvent, error) {
	return localJournalStore().ReadLocalEvents(worktree)
}

func readLocalWorkLogBytes(worktree, name string) ([]byte, error) {
	return localJournalStore().ReadLocalWorkLogBytes(worktree, name)
}

// readLocalEventsForInspection preserves the strict append/validation path
// while allowing read-only lifecycle inspection to explain one historical
// parked-session receipt emitted with event version 0. The malformed record is
// never returned as valid evidence and is never rewritten; callers receive the
// valid prefix so owner/cleanup inspection can continue, plus a compatibility
// marker for diagnostics.
func readLocalEventsForInspection(worktree string, acceptHistorical func(LocalWorkLogEvent) bool) ([]LocalWorkLogEvent, bool, error) {
	events, err := readLocalEvents(worktree)
	if err == nil {
		return events, false, nil
	}
	directory, openErr := openLocalWorkLogDir(worktree, false)
	if openErr != nil {
		return nil, false, err
	}
	defer func() { _ = directory.Close() }()
	content, readErr := readBytesAt(directory, localWorkLogEventsName)
	if readErr != nil {
		return nil, false, err
	}
	parts := bytes.Split(content, []byte{'\n'})
	if len(parts) < 2 || len(content) == 0 || content[len(content)-1] != '\n' {
		return nil, false, err
	}
	last := bytes.TrimSpace(parts[len(parts)-2])
	var historical LocalWorkLogEvent
	if json.Unmarshal(last, &historical) != nil || !historicalParkedCompletionShape(historical) ||
		acceptHistorical == nil || !acceptHistorical(historical) {
		return nil, false, err
	}
	validPrefix, prefixErr := parseLocalEvents(bytes.Join(parts[:len(parts)-2], []byte{'\n'}))
	if prefixErr != nil {
		return nil, false, err
	}
	return validPrefix, true, nil
}

// historicalParkedCompletionShape identifies only the immutable event shape
// produced by the affected receiver release. It is intentionally separate
// from validateLocalEventForSequence: version 0 remains invalid evidence for
// every append, repair, and authoritative Work Log operation.
func historicalParkedCompletionShape(event LocalWorkLogEvent) bool {
	if event.Version != 0 || event.Type != LocalEventHandoff || event.Result != "completed" ||
		event.Message != "parked successor proved live; target member custody completed" || len(event.Extra) == 0 {
		return false
	}
	for _, key := range []string{
		"resume_id", "parked_session_id", "member_id", "repository",
		"predecessor_wb_session_id", "successor_wb_session_id",
		"source_work_log_reference", "target_work_log_reference", "attempt_id",
	} {
		value, ok := event.Extra[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func parseLocalEvents(content []byte) ([]LocalWorkLogEvent, error) {
	return localJournalStore().ParseLocalEvents(content)
}

func readLocalProjection(worktree string) (LocalWorkLogProjection, error) {
	return localJournalStore().ReadLocalProjection(worktree)
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
	return localJournalStore().AppendLocalEventUnderLock(worktree, directory, event)
}

func sameLocalEvent(first, second LocalWorkLogEvent) bool {
	return localJournalStore().SameLocalEvent(first, second)
}

// repairLocalEventDerivatives makes the journal event the sole source of
// truth. A crash after events.jsonl but before either derived write is
// therefore repaired by replaying the exact explicit event ID.
func repairLocalEventDerivatives(worktree string, directory *os.File, events []LocalWorkLogEvent) (LocalWorkLogProjection, error) {
	return localJournalStore().RepairLocalEventDerivatives(worktree, directory, events)
}

func repairLocalOutbox(directory *os.File, events []LocalWorkLogEvent) error {
	return localJournalStore().RepairLocalOutbox(directory, events)
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
	return localJournalStore().RepairCurrentLocalProjection(worktree)
}

func rewriteLocalEventJournal(directory *os.File, events []LocalWorkLogEvent) error {
	return localJournalStore().RewriteLocalEventJournal(directory, events)
}

func readLocalEventsForAppend(directory *os.File) ([]LocalWorkLogEvent, bool, error) {
	return localJournalStore().ReadLocalEventsForAppend(directory)
}

// parseLocalEventsForRepair accepts only one crash shape: an unterminated
// final record. Malformed completed lines and every non-final corruption are
// immutable-evidence conflicts and remain hard failures.
func parseLocalEventsForRepair(content []byte) ([]LocalWorkLogEvent, bool, error) {
	return localJournalStore().ParseLocalEventsForRepair(content)
}

func validateLocalEventForSequence(event LocalWorkLogEvent, existing []LocalWorkLogEvent) error {
	return localJournalStore().ValidateLocalEventForSequence(event, existing)
}

func encodeLocalEvents(events []LocalWorkLogEvent) ([]byte, error) {
	return localJournalStore().EncodeLocalEvents(events)
}

func lockLocalWorkLog(directory *os.File) (func(), error) {
	return localJournalStore().LockLocalWorkLog(directory)
}

func rebuildLocalProjection(events []LocalWorkLogEvent) (LocalWorkLogProjection, error) {
	return localJournalStore().RebuildLocalProjection(events)
}

func localEventID(existing []LocalWorkLogEvent, event LocalWorkLogEvent) string {
	return localJournalStore().LocalEventID(existing, event)
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
	return localJournalStore().CountLocalOutbox(worktree)
}
