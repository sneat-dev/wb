package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// WorktreeMergeRetiredSourceProof records independent landing, never live
// preservation. The original failed candidate receipt remains unchanged.
type WorktreeMergeRetiredSourceProof struct {
	Source           WorktreeMergeSource `json:"source"`
	ClaimBaseSHA     string              `json:"claim_base_sha"`
	CleanupReport    string              `json:"cleanup_report"`
	CleanupSHA256    string              `json:"cleanup_sha256"`
	PullRequest      string              `json:"pull_request"`
	LandingSHA       string              `json:"landing_sha"`
	CurrentTargetSHA string              `json:"current_target_sha"`
	Kind             string              `json:"kind"`
	TreeSHA          string              `json:"tree_sha,omitempty"`
}

func proveRetiredUnpublishedFailureSource(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, source WorktreeMergeSource, canonical string, run runner.Runner) (WorktreeMergeRetiredSourceProof, error) {
	var empty WorktreeMergeRetiredSourceProof
	if err := requireAbsentRetiredSource(ctx, canonical, source, run); err != nil {
		return empty, err
	}
	cleanup, err := worktrees.FindTerminalCleanupProof(projectsRoot, receipt.Repository, receipt.Target, source.Task, source.Worktree, source.Branch)
	if err != nil {
		return empty, err
	}
	result := cleanup.Result
	pr := result.MergedPullRequest
	if result.HeadSHA != source.SHA || filepath.Clean(result.CanonicalDir) != filepath.Clean(canonical) || !result.Applied || !result.WorktreeGone || !result.BranchDeleted || pr == nil ||
		pr.Repository != receipt.Repository || pr.Base != receipt.Target || pr.HeadSHA != source.SHA || pr.State != "MERGED" || pr.URL == "" || pr.MergeSHA == "" || pr.Number <= 0 || pr.Merged == nil {
		return empty, errors.New("retired source lacks exact successful cleanup and merged pull-request identity")
	}
	expectation := worktrees.TerminalWorkLogExpectation{Task: source.Task, Repository: receipt.Repository, Worktree: source.Worktree, Branch: source.Branch, Base: receipt.Target, FinalCommit: source.SHA}
	base, err := worktrees.ReadRemovedTerminalWorkLogClaimBase(projectsRoot, expectation)
	if err != nil {
		return empty, fmt.Errorf("authenticate retired source cleanup terminal: %w", err)
	}
	synthetic := receipt
	synthetic.Candidate = WorktreeMergeCandidate{Task: source.Task, Worktree: source.Worktree, Branch: source.Branch, SHA: source.SHA}
	synthetic.PullRequest = fmt.Sprint(pr.Number)
	synthetic.TargetSHA = base
	landing, target, changedHead, kind, tree, err := proveStrandedPullRequestLanding(ctx, synthetic)
	if err != nil {
		return empty, err
	}
	if changedHead != "" || landing != pr.MergeSHA {
		return empty, errors.New("retired source pull request head or landing changed")
	}
	digest, err := worktreeMergeReceiptSHA256(cleanup.ReportPath)
	if err != nil {
		return empty, err
	}
	if err := requireAbsentRetiredSource(ctx, canonical, source, run); err != nil {
		return empty, err
	}
	return WorktreeMergeRetiredSourceProof{Source: source, ClaimBaseSHA: base, CleanupReport: cleanup.ReportPath, CleanupSHA256: digest, PullRequest: pr.URL, LandingSHA: landing, CurrentTargetSHA: target, Kind: kind, TreeSHA: tree}, nil
}

func requireAbsentRetiredSource(ctx context.Context, canonical string, source WorktreeMergeSource, run runner.Runner) error {
	if _, err := os.Lstat(source.Worktree); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("retired source checkout must remain absent: %v", err)
	}
	registered, _, err := runCommand(ctx, run, 0, 0, canonical, "git", "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return err
	}
	for _, field := range strings.Split(registered, "\x00") {
		if strings.HasPrefix(field, "worktree ") && filepath.Clean(strings.TrimPrefix(field, "worktree ")) == filepath.Clean(source.Worktree) {
			return errors.New("retired source remains registered")
		}
	}
	branch, _, err := runCommand(ctx, run, 0, 0, canonical, "git", "for-each-ref", "--format=%(refname)", "refs/heads/"+source.Branch)
	if err != nil {
		return err
	}
	if strings.TrimSpace(branch) != "" {
		return errors.New("retired source local branch exists")
	}
	return nil
}

func sameRetiredSourceProofs(left, right []WorktreeMergeRetiredSourceProof) bool {
	return len(left) == len(right) && (len(left) == 0 || reflect.DeepEqual(left, right))
}

func validateUnpublishedSourceProofCoverage(ack WorktreeMergeUnpublishedValidationFailureAcknowledgement, receipt WorktreeMergeReceipt) error {
	if len(ack.PreservedSources)+len(ack.LandedSources) != len(receipt.Sources) {
		return errors.New("incomplete source proof set")
	}
	seen := make(map[string]bool)
	for _, source := range ack.PreservedSources {
		key := source.Task + "\x00" + source.Worktree
		if seen[key] {
			return errors.New("duplicate source proof")
		}
		seen[key] = true
		matched := false
		for _, original := range receipt.Sources {
			if source.Task == original.Task && source.Worktree == original.Worktree && source.Branch == original.Branch && source.SHA != "" {
				matched = true
			}
		}
		if !matched {
			return errors.New("unknown preserved source")
		}
	}
	for _, proof := range ack.LandedSources {
		key := proof.Source.Task + "\x00" + proof.Source.Worktree
		if seen[key] {
			return errors.New("duplicate source proof")
		}
		seen[key] = true
		matched := false
		for _, original := range receipt.Sources {
			if proof.Source == original {
				matched = true
			}
		}
		if !matched || receipt.Phase != WorktreeMergePhaseLand || proof.ClaimBaseSHA == "" || proof.CleanupReport == "" || proof.CleanupSHA256 == "" || proof.PullRequest == "" || proof.LandingSHA == "" || proof.CurrentTargetSHA != ack.CurrentTargetSHA || (proof.Kind != "ancestor" && proof.Kind != "tree-identical") || (proof.Kind == "tree-identical" && proof.TreeSHA == "") {
			return errors.New("invalid independently landed source proof")
		}
	}
	return nil
}
