package worktreebranches

import (
	"context"
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/worktreelanding"
	"github.com/sneat-dev/wb/internal/worktreeproof"
)

func BranchInUseKey(repository, branch string) string { return repository + "|" + branch }

// ClassifyBranch applies the evidence precedence used by both inventory and
// cleanup. All network, Git, and receipt observations come from this instance.
func (service InventoryService) ClassifyBranch(ctx context.Context, repository Repository, sweep InventorySweep, ref BranchRef, scope, targetSHA, canonicalHEAD string, inUse map[string]string, checkedOut map[string]bool, pullRequestCache map[string][]GitHubPullRequest) BranchEntry {
	entry := BranchEntry{Repository: repository.Slug, Branch: ref.Name, Scope: scope,
		SHA: ref.SHA, ShortSHA: ShortSHA(ref.SHA), CommitterDate: ref.CommitterDate,
		Base: sweep.Base, TargetSHA: targetSHA}
	if IsProtectedBranch(ref.Name, sweep.Base, canonicalHEAD) {
		entry.Disposition = BranchProtected
		entry.Evidence = ProtectedEvidence(ref.Name, sweep.Base, canonicalHEAD)
		return entry
	}
	task, claimed := inUse[BranchInUseKey(repository.Slug, ref.Name)]
	if claimed || checkedOut[ref.Name] {
		entry.Disposition = BranchInUse
		entry.Task = task
		if claimed {
			entry.Evidence = fmt.Sprintf("checked out or claimed by WB task %s", task)
			entry.Reason = fmt.Sprintf("owned by wb worktree task %s; use `wb worktree cleanup %s` or `wb worktree abort %s`, never wb branch cleanup", task, task, task)
		} else {
			entry.Evidence = "checked out in a linked worktree"
			entry.Reason = "checked out in a linked worktree; wb branch cleanup never touches a working tree"
		}
		return entry
	}
	if sweep.SupersededBy != "" {
		receipt, rejection := service.Ports.Supersession(ctx, sweep.SupersededBy, repository, ref, sweep.Base, targetSHA)
		if rejection == "" {
			entry.Disposition = BranchSuperseded
			entry.SupersededAtOrigin = true
			entry.SupersessionReceipt, entry.SupersessionReviewer, entry.SupersessionReceiptID = sweep.SupersededBy, receipt.Reviewer, receipt.ReceiptID
			digest, digestErr := service.Ports.SupersessionDigest(sweep.SupersededBy)
			if digestErr != nil {
				entry.Disposition, entry.Evidence = BranchUnreadable, fmt.Sprintf("digest supersession receipt: %v", digestErr)
				return entry
			}
			entry.SupersessionSHA256 = digest
			entry.Evidence = "trusted reviewer receipt binds the exact source, target, replacements, and complete residual inventory"
			return entry
		}
		entry.SupersessionRejection = rejection
	}
	contained, err := service.Ports.IsAncestor(ctx, repository.Path, ref.SHA, targetSHA)
	if err != nil {
		entry.Disposition = BranchUnreadable
		entry.Evidence = fmt.Sprintf("merge-base --is-ancestor: %v", err)
		return entry
	}
	if contained {
		entry.Disposition = BranchContained
		entry.Evidence = fmt.Sprintf("merge-base --is-ancestor %s %s", entry.ShortSHA, ShortSHA(targetSHA))
		return entry
	}
	absorbed, absorbedEvidence, uniqueCount, err := service.ClassifyAbsorbedOrUnique(ctx, repository.Path, targetSHA, ref.SHA)
	if err != nil {
		entry.Disposition = BranchUnreadable
		entry.Evidence = fmt.Sprintf("git cherry: %v", err)
		return entry
	}
	absorbedByNote := ""
	if sweep.AbsorbedBy != "" {
		receipt, rejection, err := service.ClassifyAttestedReceipt(ctx, repository, ref, sweep.Base, targetSHA, sweep.AbsorbedBy)
		if err != nil {
			entry.Disposition = BranchUnreadable
			entry.Evidence = fmt.Sprintf("--absorbed-by verification failed: %v", err)
			return entry
		}
		if receipt != nil {
			return ReceiptedBranch(entry, receipt.LandingSHA, receipt.PullRequest, sweep.AbsorbedBy)
		}
		entry.AbsorbedByRejection = rejection
		absorbedByNote = "; --absorbed-by: " + rejection
	}
	receiptNote := ""
	if sweep.Receipts {
		receipt, note := service.ClassifyLandingReceipt(ctx, repository, ref, sweep.Base, targetSHA, pullRequestCache)
		if receipt != nil {
			return ReceiptedBranch(entry, receipt.MergeSHA, receipt, "")
		}
		receiptNote = "; receipt: " + note
	}
	if absorbed {
		entry.Disposition = BranchAbsorbed
		entry.Evidence = absorbedEvidence + receiptNote + absorbedByNote
		entry.Reason = "absorbed by patch-id or tree equality only; never eligible for --apply. " +
			"If this branch belongs to a WB task, run `wb worktree cleanup <task> --absorbed-by <pr-or-commit>`; " +
			"if it has no worktree, run `wb branch cleanup --absorbed-by <pr-or-commit>`; " +
			"otherwise this requires an explicit human decision"
		entry.Reason += receiptNote + absorbedByNote
		return entry
	}
	entry.Disposition = BranchUnique
	entry.Evidence = fmt.Sprintf("git cherry reports %d unique patch(es) not upstream", uniqueCount) + receiptNote + absorbedByNote
	return entry
}

func (service InventoryService) ClassifyAttestedReceipt(ctx context.Context, repository Repository, ref BranchRef, base, targetSHA, absorbedBy string) (*worktreelanding.VerifiedCandidate, string, error) {
	return service.Ports.AttestedReceipt(ctx, repository, ref, base, targetSHA, absorbedBy)
}

func (service InventoryService) ClassifyLandingReceipt(ctx context.Context, repository Repository, ref BranchRef, base, targetSHA string, cache map[string][]GitHubPullRequest) (*PullRequest, string) {
	pullRequests, ok := cache[ref.SHA]
	if !ok {
		fetched, err := service.Ports.PullRequestsForHead(ctx, repository, ref.SHA)
		if err != nil {
			return nil, fmt.Sprintf("pull-request query failed: %v", err)
		}
		pullRequests = fetched
		if cache != nil {
			cache[ref.SHA] = pullRequests
		}
	}
	pullRequest := service.Ports.AbsorbingPR(pullRequests, base)
	if pullRequest == nil {
		return nil, fmt.Sprintf("no merged pull request into %s names this head", base)
	}
	if !worktreeproof.IsGitObjectID(pullRequest.MergeSHA) {
		return nil, fmt.Sprintf("merged pull request #%d carries no valid merge commit", pullRequest.Number)
	}
	landed, err := service.Ports.IsAncestor(ctx, repository.Path, pullRequest.MergeSHA, targetSHA)
	if err != nil {
		return nil, fmt.Sprintf("landing containment check failed: %v", err)
	}
	if !landed {
		return nil, fmt.Sprintf("landing commit %s of pull request #%d is not contained in the fetched target", ShortSHA(pullRequest.MergeSHA), pullRequest.Number)
	}
	inLanding, err := service.Ports.ContentContained(ctx, repository.Path, ref.SHA, pullRequest.MergeSHA)
	if err != nil {
		return nil, fmt.Sprintf("three-way proof failed to run: %v", err)
	}
	if !inLanding {
		return nil, fmt.Sprintf("landing commit %s of pull request #%d does not carry this branch's work in full; it may have been amended while landing", ShortSHA(pullRequest.MergeSHA), pullRequest.Number)
	}
	inTarget, err := service.Ports.ContentContained(ctx, repository.Path, ref.SHA, targetSHA)
	if err != nil {
		return nil, fmt.Sprintf("three-way proof failed to run: %v", err)
	}
	if !inTarget {
		return nil, fmt.Sprintf("landed in full via pull request #%d, but the target has since diverged from that work — later edits and a revert are indistinguishable here; the content remains recoverable at landing commit %s", pullRequest.Number, ShortSHA(pullRequest.MergeSHA))
	}
	return pullRequest, ""
}

func (service InventoryService) ClassifyAbsorbedOrUnique(ctx context.Context, repositoryPath, targetSHA, branchSHA string) (absorbed bool, evidence string, uniqueCount int, err error) {
	output, err := service.Ports.Git(ctx, repositoryPath, "cherry", targetSHA, branchSHA)
	if err != nil {
		return false, "", 0, err
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "+") {
			uniqueCount++
		}
	}
	if uniqueCount == 0 {
		return true, fmt.Sprintf("git cherry %s %s reports 0 unique patches", ShortSHA(targetSHA), ShortSHA(branchSHA)), 0, nil
	}
	branchTree, err := service.Ports.CommitTree(ctx, repositoryPath, branchSHA)
	if err != nil {
		return false, "", uniqueCount, nil
	}
	targetTree, err := service.Ports.CommitTree(ctx, repositoryPath, targetSHA)
	if err != nil {
		return false, "", uniqueCount, nil
	}
	if branchTree == targetTree {
		return true, fmt.Sprintf("tree %s identical to target tree %s", ShortSHA(branchTree), ShortSHA(targetTree)), 0, nil
	}
	return false, "", uniqueCount, nil
}
