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

// exactBranchPullRequests asks GitHub for both roles by branch name. Filtering
// the returned identities matters: a commit can be shared by several branches,
// and GitHub's head selector alone does not prove same-repository ownership.
func exactBranchPullRequests(ctx context.Context, worktree, repository, branch string) branchPullRequestEvidence {
	return facadePullRequestEvidence(branchInventoryService().ExactBranchPullRequests(ctx, worktreebranches.Repository{Slug: repository, Path: worktree}, branch))
}

func openBranchPullRequests(ctx context.Context, worktree, repository, branch string) branchPullRequestEvidence {
	return facadePullRequestEvidence(branchInventoryService().OpenBranchPullRequests(ctx, worktreebranches.Repository{Slug: repository, Path: worktree}, branch))
}
