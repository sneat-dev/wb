package orchestrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// MergeLaneClaim reports that a branch is currently a source of a
// non-terminal merger-lane receipt: some merger lane has already selected it
// as an input to a batch it may land at any time. See the lesson
// merger-lane-branch-race (spec/lessons/merger-lane-branch-race in
// sneat-co/backstage): a branch with an open PR is not a private workspace
// once a lane owns it, and neither PR state, reviews, nor CI surface that
// fact to a main agent deciding whether to push.
type MergeLaneClaim struct {
	// Lane identifies the exclusive (repository, target) merger lane that
	// claimed the branch.
	Lane string `json:"lane"`
	// Target is the branch the lane is draining toward.
	Target string `json:"target"`
	// Status is the receipt's current lifecycle status (see
	// WorktreeMergeStatus), e.g. "prepared" or "checks_pending".
	Status string `json:"status"`
	// ReceiptPath is the exact local receipt backing the claim, so a caller
	// can inspect the full batch (wb worktree merge land <receipt>).
	ReceiptPath string `json:"receipt_path"`
}

// ActiveMergeLaneClaim reports whether branch in repository is a source of an
// active (non-terminal, non-superseded) merger-lane receipt, regardless of
// which target that lane is draining toward — a main agent about to push
// does not necessarily know the target a lane already chose. It returns a
// nil claim, not an error, when no active receipt claims the branch or when
// no merger has ever run for this WB home (a fresh reports directory).
func ActiveMergeLaneClaim(projectsRoot, repository, branch string) (*MergeLaneClaim, error) {
	repository = strings.TrimSpace(repository)
	branch = strings.TrimSpace(branch)
	if repository == "" || branch == "" {
		return nil, nil
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return nil, err
	}
	reportsDir := filepath.Join(home, "reports", "worktree-merge")
	entries, err := os.ReadDir(reportsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if strings.HasSuffix(entry.Name(), worktreeMergeLandedFailureAcknowledgementSuffix) ||
			strings.HasSuffix(entry.Name(), worktreeMergeValidationFailureSupersessionSuffix) ||
			strings.HasSuffix(entry.Name(), worktreeMergePreparedRebatchSuffix) {
			continue
		}
		path := filepath.Join(reportsDir, entry.Name())
		receipt, readErr := readWorktreeMergeReceipt(path)
		if readErr != nil {
			// A malformed or unrelated file under the reports directory must
			// not hide a real claim recorded in a sibling receipt.
			continue
		}
		if receipt.Repository != repository || receipt.Status == WorktreeMergeComplete {
			continue
		}
		if !worktreeMergeReceiptClaimsBranch(receipt, branch) {
			continue
		}
		acknowledged, ackErr := hasLandedFailureAcknowledgement(receipt)
		if ackErr != nil {
			return nil, ackErr
		}
		if acknowledged {
			continue
		}
		superseded, supersessionErr := hasValidationFailureSupersession(receipt)
		if supersessionErr != nil {
			return nil, supersessionErr
		}
		if superseded {
			continue
		}
		rebatched, rebatchErr := hasPreparedWorktreeMergeRebatch(receipt)
		if rebatchErr != nil {
			return nil, rebatchErr
		}
		if rebatched {
			continue
		}
		lane := receipt.Lane
		if lane == "" {
			lane = worktreeMergeLaneID(receipt.Repository, receipt.Target)
		}
		return &MergeLaneClaim{Lane: lane, Target: receipt.Target, Status: string(receipt.Status), ReceiptPath: path}, nil
	}
	return nil, nil
}

// worktreeMergeReceiptClaimsBranch reports whether branch is one of the
// receipt's original sources or its integrated candidate branch (a rebased
// or rebatched receipt still carries the original source branches in
// Sources, but the checked-out candidate itself is also claimed while a
// batch is in flight).
func worktreeMergeReceiptClaimsBranch(receipt WorktreeMergeReceipt, branch string) bool {
	if receipt.Candidate.Branch == branch {
		return true
	}
	for _, source := range receipt.Sources {
		if source.Branch == branch {
			return true
		}
	}
	for _, rebatched := range receipt.RebatchedCandidates {
		if rebatched.Branch == branch {
			return true
		}
	}
	return false
}
