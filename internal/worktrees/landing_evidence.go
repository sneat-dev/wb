package worktrees

import (
	"context"
	"github.com/sneat-dev/wb/internal/worktreelanding"
)

const DefaultResidueDepth = worktreelanding.DefaultResidueDepth

type ResidualCommit = worktreelanding.ResidualCommit
type LandingEvidence = worktreelanding.LandingEvidence

func landingEvidence(ctx context.Context, worktree, repository, slug, head, base, target string, depth int) (*LandingEvidence, error) {
	return worktreelanding.LandingEvidenceFor(ctx, worktree, repository, slug, head, base, target, depth, git,
		func(ctx context.Context, worktree, repository, slug, candidate, base, target string) (*worktreelanding.VerifiedCandidate, error) {
			pullRequests, err := githubPullRequests(ctx, worktree, slug, candidate)
			if err != nil {
				return nil, err
			}
			receipt, _, err := absorbedLandingReceipt(ctx, worktree, repository, slug, candidate, base, target, "", pullRequests)
			if err != nil || receipt == nil {
				return nil, err
			}
			return &worktreelanding.VerifiedCandidate{LandingSHA: receipt.LandingSHA, PullRequest: receipt.PullRequest}, nil
		})
}

func detachedRefusal(result ListResult) string {
	return worktreelanding.DetachedRefusal(result.HeadSHA, result.Base, result.HeadUnknownToRemote)
}
func (result ListResult) landedWithResidue() bool {
	return worktreelanding.LandedWithResidue(result.Landing)
}
func (result ListResult) residueReason() string { return worktreelanding.ResidueReason(result.Landing) }
