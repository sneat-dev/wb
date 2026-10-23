package worktrees

import (
	"context"
	"os"
	"strings"

	"github.com/sneat-dev/wb/internal/worktreebranches"
)

// Public receipt DTOs keep their facade identity while branch policy owns
// parsing, validation, and deterministic audit rendering.
type SupersessionReceipt = worktreebranches.SupersessionReceipt
type SupersessionDependencyDelta = worktreebranches.SupersessionDependencyDelta
type SupersessionReplacement = worktreebranches.SupersessionReplacement
type SupersessionResidual = worktreebranches.SupersessionResidual
type SupersessionApproval = worktreebranches.SupersessionApproval
type SupersessionWorkflowAdoption = worktreebranches.SupersessionWorkflowAdoption

func supersessionEntry(entry ListResult) worktreebranches.SupersessionEntry {
	return worktreebranches.SupersessionEntry{
		Task: entry.Task, Repository: entry.Repository, Branch: entry.Branch,
		Base: entry.Base, HeadSHA: entry.HeadSHA, RemoteTargetSHA: entry.RemoteTargetSHA,
		CanonicalDir: entry.CanonicalDir, WorktreeDir: entry.WorktreeDir,
		OpenPullRequest: entry.OpenPullRequest,
	}
}

func supersessionService() worktreebranches.SupersessionService {
	return worktreebranches.SupersessionService{Ports: worktreebranches.SupersessionPorts{
		Git: git,
		ReadGitFileBytes: func(ctx context.Context, repository, revision, file string) ([]byte, error) {
			return heartbeatPorts().GitRaw(ctx, repository, "cat-file", "blob", revision+":"+file)
		},
		IsAncestor:  isAncestor,
		ReadReceipt: os.ReadFile,
		ReadCampaignMarker: func(worktree string) (bool, error) {
			manifest, err := ReadManifest(worktree)
			if err != nil {
				return false, err
			}
			return manifest.DependencyCampaign, nil
		},
	}}
}

func supersessionReceiptForEntry(ctx context.Context, path string, entry ListResult) (*SupersessionReceipt, string) {
	return supersessionService().SupersessionReceiptForEntry(ctx, path, supersessionEntry(entry))
}

// The facade alone updates lifecycle state and retains the receipt for the
// terminal claim transaction; the branch leaf only verifies its evidence.
func applySupersessionReceipt(ctx context.Context, path string, entry *ListResult) {
	if strings.TrimSpace(path) == "" {
		return
	}
	receipt, rejection := supersessionReceiptForEntry(ctx, path, *entry)
	if rejection != "" {
		entry.SupersessionRejection = rejection
		entry.SupersededAtOrigin = false
		return
	}
	entry.SupersededAtOrigin = true
	entry.SupersessionReceipt = path
	entry.SupersessionReviewer = receipt.Approval.Actor
	entry.SupersessionReceiptID = receipt.Approval.ReceiptID
	entry.SupersessionRejection = ""
	entry.supersessionReceipt = receipt
}

func ValidateDependencyDeltas(ctx context.Context, receipt SupersessionReceipt, entry ListResult) error {
	return supersessionService().ValidateDependencyDeltas(ctx, receipt, supersessionEntry(entry))
}
