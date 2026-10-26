package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/githubchecks"
	"path/filepath"
	"strings"
)

// UnpublishedConflictRebatchProof preserves predecessor custody without
// converting a closed superseded publication into an unpublished candidate.
type UnpublishedConflictRebatchProof struct {
	ReceiptPath   string                 `json:"receipt_path"`
	ReceiptSHA256 string                 `json:"receipt_sha256"`
	RebatchPath   string                 `json:"rebatch_path"`
	RebatchSHA256 string                 `json:"rebatch_sha256"`
	RebatchID     string                 `json:"rebatch_id"`
	Candidate     WorktreeMergeCandidate `json:"candidate"`
	TargetSHA     string                 `json:"target_sha"`
	PullRequest   string                 `json:"closed_superseded_pull_request,omitempty"`
}

// Read only immutable lineage. Live assets and publication are independently
// inspected during preview and apply; later scans need no cleanup authority.
func unpublishedConflictRebatchLineage(receipt WorktreeMergeReceipt) ([]UnpublishedConflictRebatchProof, error) {
	return unpublishedConflictRebatchLineageWithPorts(receipt, readWorktreeMergeReceipt, readPreparedWorktreeMergeRebatch, worktreeMergeReceiptSHA256)
}

func unpublishedConflictRebatchLineageWithPorts(receipt WorktreeMergeReceipt, read func(string) (WorktreeMergeReceipt, error), bridge func(string, WorktreeMergeReceipt) (WorktreeMergePreparedRebatch, error), hash func(string) (string, error)) ([]UnpublishedConflictRebatchProof, error) {
	var proofs []UnpublishedConflictRebatchProof
	seen := map[string]bool{receipt.ReceiptPath: true}
	reports := filepath.Dir(receipt.ReceiptPath)
	for receipt.RebatchOf != "" {
		path := receipt.RebatchOf
		if seen[path] || !pathInsideWorktreeMergeReports(reports, path) {
			return nil, errors.New("rebatch predecessor is cyclic or outside the exact report store")
		}
		seen[path] = true
		previous, err := read(path)
		if err != nil {
			return nil, err
		}
		rebatch, err := bridge(rebatchPath(path), previous)
		if err != nil {
			return nil, err
		}
		if !preparedRebatchReplacementMatches(rebatch, receipt) || len(receipt.RebatchedCandidates) != 1 || receipt.RebatchedCandidates[0] != previous.Candidate || previous.Repository != receipt.Repository || previous.Target != receipt.Target || previous.Lane != receipt.Lane || previous.LandingSHA != "" {
			return nil, errors.New("rebatch predecessor does not bind the exact replacement and preserved candidate")
		}
		if previous.PullRequest != "" && (previous.PublishedCandidateSHA != previous.Candidate.SHA || rebatch.ClosedPullRequest != previous.PullRequest || receipt.SupersededPullRequest != previous.PullRequest) {
			return nil, errors.New("published rebatch predecessor lacks exact recorded supersession")
		}
		if previous.PullRequest == "" && previous.PublishedCandidateSHA != "" {
			return nil, errors.New("rebatch predecessor has ambiguous publication metadata")
		}
		digest, err := hash(path)
		if err != nil {
			return nil, err
		}
		ackDigest, err := hash(rebatchPath(path))
		if err != nil {
			return nil, err
		}
		proofs = append(proofs, UnpublishedConflictRebatchProof{ReceiptPath: path, ReceiptSHA256: digest, RebatchPath: rebatchPath(path), RebatchSHA256: ackDigest, RebatchID: rebatch.ID, Candidate: previous.Candidate, TargetSHA: previous.TargetSHA, PullRequest: previous.PullRequest})
		receipt = previous
	}
	if len(receipt.RebatchedCandidates) != 0 {
		return nil, errors.New("receipt has unbound rebatched candidate custody")
	}
	return proofs, nil
}

func proveUnpublishedConflictPredecessorPublication(ctx context.Context, proof UnpublishedConflictRebatchProof) error {
	r, err := readWorktreeMergeReceipt(proof.ReceiptPath)
	if err != nil {
		return err
	}
	if r.Candidate != proof.Candidate || r.PullRequest != proof.PullRequest {
		return errors.New("predecessor identity changed during publication proof")
	}
	if proof.PullRequest != "" {
		return proveClosedUnpublishedConflictPredecessor(ctx, r)
	}
	remote, _, err := runCommand(ctx, defaultRunner, 0, 0, r.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/"+r.Candidate.Branch)
	if err != nil {
		return err
	}
	if remote != "" {
		return fmt.Errorf("unpublished rebatch predecessor %s now has a remote ref", r.Candidate.Branch)
	}
	return requireZeroDeltaCandidateWithoutPullRequest(ctx, r.Candidate.Worktree, r)
}

// State and exact identity come from the same fresh snapshot. The ordinary
// published-candidate adoption helper also allows OPEN and cannot prove this.
func proveClosedUnpublishedConflictPredecessor(ctx context.Context, r WorktreeMergeReceipt) error {
	repository, err := hostedRepositoryForCandidate(ctx, defaultRunner, r)
	if err != nil {
		return err
	}
	view, err := githubchecks.ReadPullRequest(ctx, repository, r.PullRequest)
	if err != nil {
		return err
	}
	if !strings.EqualFold(view.State, "closed") || view.Merged || view.MergedAt != nil || view.MergeCommitSHA != "" {
		return errors.New("superseded predecessor pull request must remain CLOSED and unmerged")
	}
	if view.Base.Ref != r.Target || view.Head.Ref != r.Candidate.Branch || view.Head.SHA != r.Candidate.SHA || view.Head.Repo == nil || view.Head.Repo.FullName != repository || view.Base.Repo == nil || view.Base.Repo.FullName != repository {
		return errors.New("closed predecessor pull request does not match exact repository, target, branch and head")
	}
	remote, _, err := runCommand(ctx, defaultRunner, 0, 0, r.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/"+r.Candidate.Branch)
	if err != nil {
		return err
	}
	if strings.TrimSpace(remote) != r.Candidate.SHA+"\trefs/heads/"+r.Candidate.Branch {
		return errors.New("closed predecessor remote ref differs from the exact superseded candidate")
	}
	return nil
}
