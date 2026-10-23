package worktrees

import (
	"context"

	"github.com/sneat-dev/wb/internal/worktreebranches"
)

// BranchPullRequest describes a PR's relationship to the named branch. This
// is branch history, separate from the immutable merged landing proof.
type BranchPullRequest = worktreebranches.BranchPullRequest

type branchPullRequestEvidence struct {
	requests []BranchPullRequest
	openHead *PullRequest
	openBase *PullRequest
	err      error
}

func facadePullRequestEvidence(evidence worktreebranches.PullRequestEvidence) branchPullRequestEvidence {
	return branchPullRequestEvidence{requests: evidence.Requests, openHead: evidence.OpenHead, openBase: evidence.OpenBase, err: evidence.Err}
}

func openBranchPullRequests(ctx context.Context, worktree, repository, branch string) branchPullRequestEvidence {
	return facadePullRequestEvidence(branchInventoryService().OpenBranchPullRequests(ctx, worktreebranches.Repository{Slug: repository, Path: worktree}, branch))
}
