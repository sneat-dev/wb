package orchestrate

import (
	"context"

	"github.com/sneat-dev/wb/internal/repopath"
)

// Helpers kept for tests only: no production caller remains.

// canonicalClonePath is where a repository's canonical clone belongs below
// githubDir: <githubDir>/{host}/{owner}/{repository} when the clone URL names a
// literal forge hostname, and the legacy <githubDir>/{owner}/{repository} when
// it does not. The host comes from the same URL EnsureCanonical would clone
// from, so it is never invented and the two can never disagree.
func canonicalClonePath(githubDir, owner, name, cloneURL string) string {
	return repopath.FromCloneURL(cloneURL, owner, name).Path(githubDir)
}

func persistReceiptCollisionAcknowledgement(path string, ack WorktreeMergeReceiptCollisionAcknowledgement) error {
	return persistReceiptCollisionAcknowledgementInjected(path, ack, nil)
}

func persistSelfSupersessionCorrection(path string, correction WorktreeMergeSelfSupersessionCorrection) error {
	return persistSelfSupersessionCorrectionInjected(path, correction, nil)
}

func reconcileAbsorbedSourcePullRequestsWith(
	ctx context.Context,
	receipt *WorktreeMergeReceipt,
	heads []string,
	remote sourcePullRequestRemote,
	persist func(WorktreeMergeReceipt) error,
) error {
	return reconcileAbsorbedSourcePullRequestsWithProgress(ctx, receipt, heads, remote, persist, nil)
}
