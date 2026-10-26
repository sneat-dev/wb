package orchestrate

import (
	"context"
	"os"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Ports are invocation-scoped fault seams. Production always uses the native
// Guard, Work Log, Git, remote observation, lock and immutable writer owners.
type unpublishedConflictPorts struct {
	resolve func(string, string) (string, error)
	read    func(string) (WorktreeMergeReceipt, error)
	lock    func(string, string, bool) (OperationLock, error)
	inspect func(context.Context, string, WorktreeMergeReceipt, string, string) (WorktreeMergeUnpublishedConflictAcknowledgement, error)
	ack     func(WorktreeMergeReceipt) (WorktreeMergeUnpublishedConflictAcknowledgement, error)
	persist func(string, WorktreeMergeUnpublishedConflictAcknowledgement) error
}

func nativeUnpublishedConflictPorts() unpublishedConflictPorts {
	return unpublishedConflictPorts{resolve: resolveWorktreeMergeReceiptPath, read: readWorktreeMergeReceipt, lock: AcquireOperationLock, inspect: inspectUnpublishedConflict, ack: readUnpublishedConflictAcknowledgement, persist: persistUnpublishedConflictAcknowledgement}
}

func persistUnpublishedConflictAcknowledgement(path string, ack WorktreeMergeUnpublishedConflictAcknowledgement) error {
	return persistMergeAcknowledgement(path, ".unpublished-conflict-*.tmp", ack,
		func(temporary, destination string, inj *filewrite.Injector) error {
			return publishMergeAcknowledgementAndSyncDirectory(temporary, destination, inj, os.Open)
		}, nil)
}

type unpublishedConflictInspectionPorts struct {
	lineage                func(WorktreeMergeReceipt) ([]UnpublishedConflictRebatchProof, error)
	predecessorPublication func(context.Context, UnpublishedConflictRebatchProof) error
	home                   func(string) (string, error)
	lane                   func(string, string, string) (landinglane.Record, bool, error)
	guard                  func(context.Context, string, worktrees.GuardOptions) (worktrees.GuardResult, error)
	canonical              func(string, string, string) (string, bool, error)
	clean                  func(context.Context, string) error
	head                   func(context.Context, string) (string, error)
	view                   func(context.Context, worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error)
	ancestor               func(context.Context, string, string, string) (bool, error)
	hash                   func(string) (string, error)
	remote                 func(context.Context, string, string) (string, error)
	target                 func(context.Context, string, string) (string, error)
	pullRequests           func(context.Context, string, WorktreeMergeReceipt) error
}

func nativeUnpublishedConflictInspectionPorts() unpublishedConflictInspectionPorts {
	return unpublishedConflictInspectionPorts{
		lineage: unpublishedConflictRebatchLineage, predecessorPublication: proveUnpublishedConflictPredecessorPublication,
		home: wbhome.Root, lane: landinglane.Read, guard: worktrees.Guard, canonical: recoveryCanonicalMatches, clean: requireCleanMergeWorktree, view: worktrees.LoadWorkLogView, hash: worktreeMergeReceiptSHA256, pullRequests: requireZeroDeltaCandidateWithoutPullRequest,
		head: func(ctx context.Context, path string) (string, error) {
			return mergeRevision(ctx, defaultRunner, path, "HEAD")
		},
		ancestor: func(ctx context.Context, path, base, head string) (bool, error) {
			return isMergeAncestorWithRunner(ctx, defaultRunner, path, base, head)
		},
		remote: func(ctx context.Context, path, branch string) (string, error) {
			output, _, err := runCommand(ctx, defaultRunner, 0, 0, path, "git", "ls-remote", "--heads", "origin", "refs/heads/"+branch)
			return output, err
		},
		target: func(ctx context.Context, path, target string) (string, error) {
			return fetchExactMergeTargetWithRunner(ctx, defaultRunner, path, target)
		},
	}
}
