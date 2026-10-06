package orchestrate

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/runner"
	"strings"
)

func advancePublishedWorktreeMergeCandidate(ctx context.Context, git Git, run runner.Runner, receipt *WorktreeMergeReceipt) (bool, error) {
	head, err := mergeRevision(ctx, run, receipt.Candidate.Worktree, "HEAD")
	if err != nil {
		return false, fmt.Errorf("read published candidate HEAD: %w", err)
	}
	if head == receipt.Candidate.SHA {
		// Red-team finding M6: HEAD already matches the recorded candidate,
		// so any earlier fast-forward note - a failure this resume has since
		// been overtaken by, or a stale success message from a previous
		// advance - no longer describes anything live. Clear it rather than
		// leaving a stale receipt.LocalSync note to confuse the next read.
		receipt.LocalSync = ""
		return false, nil
	}
	// Red-team finding B1: a server-side update-branch advance can persist
	// its new Candidate.SHA (M3's persist-first ordering) and then have its
	// best-effort local fast-forward fail or never run at all (a crash, a
	// kill, a dirty/missing worktree at the time). The worktree is then
	// left at the OLD candidate the update replaced - behind, not diverged
	// - and every later resume must recover that fast-forward rather than
	// judge the worktree's stale HEAD as a conflicting advance.
	//
	// The proof that HEAD is an ancestor of the recorded candidate comes
	// from the receipt's own append-only TargetRefreshes history
	// (worktreeMergeCandidateAdvanceRecorded), not a local `git merge-base`:
	// the recorded candidate's object may never have been fetched into this
	// worktree at all when the advance was only recorded server side, so a
	// local ancestry check would fail on a missing object even though the
	// descent genuinely holds. fastForwardWorktreeToUpdatedHead
	// (pr_land_local_sync.go, shared with the plain `wb pr land` route)
	// fetches the candidate branch before fast-forwarding onto it, and is
	// itself best-effort: the receipt is the durable record here, and the
	// worktree is a convenience the next resume can still repair.
	if worktreeMergeCandidateAdvanceRecorded(*receipt, head, receipt.Candidate.SHA) {
		if note := fastForwardWorktreeToUpdatedHead(ctx, git, run, receipt.Candidate.Worktree, receipt.Candidate.Branch, receipt.Candidate.SHA); note != "" {
			receipt.LocalSync = note
		}
		return false, nil
	}
	if receipt.PublishedCandidateSHA == "" {
		return false, worktreeMergeDriftError(receipt,
			fmt.Errorf("candidate head drifted from %s to %s without an exact published predecessor", receipt.Candidate.SHA, head))
	}
	publishedContainsRecorded, err := isMergeAncestorWithRunner(ctx, run, receipt.Candidate.Worktree, receipt.PublishedCandidateSHA, receipt.Candidate.SHA)
	if err != nil {
		return false, fmt.Errorf("verify published candidate predecessor: %w", err)
	}
	if !publishedContainsRecorded {
		return false, worktreeMergeDriftError(receipt,
			fmt.Errorf("recorded candidate %s does not descend from published candidate %s", receipt.Candidate.SHA, receipt.PublishedCandidateSHA))
	}
	contains, err := isMergeAncestorWithRunner(ctx, run, receipt.Candidate.Worktree, receipt.Candidate.SHA, head)
	if err != nil {
		return false, fmt.Errorf("verify published candidate descendant: %w", err)
	}
	if !contains {
		return false, worktreeMergeDriftError(receipt,
			fmt.Errorf("candidate HEAD %s is not a descendant of published candidate %s", head, receipt.Candidate.SHA))
	}
	receipt.Candidate.SHA = head
	return true, nil
}

// worktreeMergeDriftError closes red-team finding M6's second half: a
// generic candidate-drift error, on its own, drops whatever
// receipt.LocalSync already recorded about why the local worktree fell out
// of step (an earlier fast-forward failure's exact reason). Append the note
// when one is present, so the operator reading the error sees the reason
// alongside the drift, not just the drift.
func worktreeMergeDriftError(receipt *WorktreeMergeReceipt, base error) error {
	note := strings.TrimSpace(receipt.LocalSync)
	if note == "" {
		return base
	}
	return fmt.Errorf("%w (%s)", base, note)
}
