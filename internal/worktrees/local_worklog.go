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
// from worktreejournal.Store.ValidateLocalEventForSequence: version 0 remains invalid evidence for
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

func readLocalEventsForAppend(directory *os.File) ([]LocalWorkLogEvent, bool, error) {
	return localJournalStore().ReadLocalEventsForAppend(directory)
}

func lockLocalWorkLog(directory *os.File) (func(), error) {
	return localJournalStore().LockLocalWorkLog(directory)
}

func rebuildLocalProjection(events []LocalWorkLogEvent) (LocalWorkLogProjection, error) {
	return localJournalStore().RebuildLocalProjection(events)
}

func observeLocalGit(ctx context.Context, worktree string) LocalGitEvidence {
	return localJournalPorts().ObserveLocalGit(ctx, worktree)
}

func countLocalOutbox(worktree string) (int, error) {
	return localJournalStore().CountLocalOutbox(worktree)
}
