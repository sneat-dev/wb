package worktrees

import (
	"context"
	"os"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
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

func localJournalPorts() worktreeclaims.LocalJournalPorts {
	return worktreeclaims.LocalJournalPorts{
		EnsureExclude: ensureJournalExclude,
		OpenDirectory: func(worktree string, create bool) (*os.File, error) {
			return openJournalSubdirectory(worktree, worklogDirectory, create)
		},
		ReadEvents:        readLocalEvents,
		ReadBytesAt:       readBytesAt,
		ParseEvents:       parseLocalEvents,
		EnsureCustody:     ensureCustody,
		Lock:              lockLocalWorkLog,
		AppendUnderLock:   appendLocalEventUnderLock,
		RebuildProjection: rebuildLocalProjection,
		ReadManifestIdentity: func(worktree string) (worktreeclaims.LocalJournalIdentity, error) {
			manifest, err := ReadManifest(worktree)
			return worktreeclaims.LocalJournalIdentity{EffortID: manifest.EffortID, RunID: manifest.RunID, ClaimID: manifest.ClaimID}, err
		},
		ReadHybridProjection: func(worktree string) (worktreeclaims.LocalJournalIdentity, error) {
			projection, err := readWorkLogProjection(worktree)
			return worktreeclaims.LocalJournalIdentity{EffortID: projection.EffortID, RunID: projection.RunID, ClaimID: projection.ClaimID, Lifecycle: projection.Lifecycle}, err
		},
		Git: git,
	}
}

func localJournalStore() worktreejournal.Store {
	return worktreejournal.Store{OpenDirectory: openLocalWorkLogDir, Project: projectLocalWorkLog}
}

func openLocalWorkLogDir(worktree string, create bool) (*os.File, error) {
	return localJournalPorts().OpenLocalWorkLogDir(worktree, create)
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
	return localJournalPorts().ReadLocalEventsForInspection(worktree, acceptHistorical)
}

// historicalParkedCompletionShape identifies only the immutable event shape
// produced by the affected receiver release. It is intentionally separate
// from validateLocalEventForSequence: version 0 remains invalid evidence for
// every append, repair, and authoritative Work Log operation.
func parseLocalEvents(content []byte) ([]LocalWorkLogEvent, error) {
	return localJournalStore().ParseLocalEvents(content)
}

func readLocalProjection(worktree string) (LocalWorkLogProjection, error) {
	return localJournalStore().ReadLocalProjection(worktree)
}

func appendLocalEvent(worktree string, event LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
	return localJournalPorts().AppendLocalEvent(worktree, event)
}
func appendLocalEventWithoutCustody(worktree string, event LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
	return localJournalPorts().AppendLocalEventWithoutCustody(worktree, event)
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
	return localJournalPorts().ProjectLocalWorkLog(worktree, events)
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
	return localJournalPorts().ObserveLocalGit(ctx, worktree)
}

func countLocalOutbox(worktree string) (int, error) {
	return localJournalStore().CountLocalOutbox(worktree)
}
