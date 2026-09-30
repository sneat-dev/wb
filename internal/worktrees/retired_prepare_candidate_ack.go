package worktrees

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/retiredcandidateack"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreeproof"
)

// retiredPrepareCandidateReceipt is the smallest immutable prepare/conflict
// receipt shape this recovery accepts. A receipt that was ever published,
// landed, or recorded an actual candidate SHA must use its existing lifecycle
// path instead.
type retiredPrepareCandidateReceipt worktreeproof.RetiredPrepareCandidateReceipt

func (receipt retiredPrepareCandidateReceipt) identity() retiredcandidateack.ReceiptIdentity {
	return worktreeproof.RetiredPrepareCandidateReceipt(receipt).Identity()
}

func (receipt retiredPrepareCandidateReceipt) validLegacyEmptyCandidate(path string) bool {
	candidate := receipt.Candidate
	candidate.SHA = receipt.TargetSHA
	return receipt.ReceiptPath == path && receipt.ID != "" && receipt.Phase == "prepare" &&
		receipt.Status == "conflict" && receipt.Lane != "" && receipt.Repository != "" &&
		receipt.Target != "" && receipt.TargetSHA != "" && receipt.LandingSHA == "" &&
		receipt.PullRequest == "" && receipt.PublishedCandidateSHA == "" &&
		receipt.Candidate.SHA == "" && retiredcandidateack.DistinctCandidate(candidate, receipt.Sources)
}

type retiredPrepareCandidateCleanupProof struct {
	AcknowledgementPath string
}

// findRetiredPrepareCandidateAcknowledgement finds a sidecar only when it is
// bound to this exact unchanged receipt and the live worktree is still that
// receipt's clean, unpublished candidate at the historical target SHA. The
// default target is fetched by the caller; both the sidecar's observation and
// the candidate head must still be ancestors of that fresh target.
func findRetiredPrepareCandidateAcknowledgement(
	ctx context.Context, home, canonicalDir, task, worktreeDir, branch, head, defaultBranch, defaultSHA string,
) (*retiredPrepareCandidateCleanupProof, error) {
	if home == "" || defaultBranch == "" || defaultSHA == "" {
		return nil, nil
	}
	cleanWorktree := filepath.Clean(worktreeDir)
	var proof *retiredPrepareCandidateCleanupProof
	err := forEachWorktreeMergeReceipt(home, func(receiptPath string, bytes []byte) bool {
		var receipt retiredPrepareCandidateReceipt
		if json.Unmarshal(bytes, &receipt) != nil || !receipt.validLegacyEmptyCandidate(receiptPath) {
			return false
		}
		identity := receipt.identity()
		if identity.Candidate.Task != task || filepath.Clean(identity.Candidate.Worktree) != cleanWorktree ||
			identity.Candidate.Branch != branch || identity.Candidate.SHA != head {
			return false
		}
		status, statusErr := git(ctx, worktreeDir, "status", "--porcelain")
		if statusErr != nil || strings.TrimSpace(status) != "" {
			return true
		}
		ack, loadErr := retiredcandidateack.Load(retiredcandidateack.Path(receiptPath), identity)
		if loadErr != nil || ack.DefaultBranch != defaultBranch {
			return true
		}
		// A later force-push or default-branch replacement cannot reuse a stale
		// acknowledgement: preserve both the acknowledged fetched object and
		// the precise candidate's present containment.
		ackContained, ackErr := isAncestor(ctx, canonicalDir, ack.DefaultSHA, defaultSHA)
		candidateContained, candidateErr := isAncestor(ctx, canonicalDir, head, defaultSHA)
		if ackErr != nil || candidateErr != nil || !ackContained || !candidateContained {
			return true
		}
		published, publishErr := remoteBranchHead(ctx, canonicalDir, branch)
		if publishErr != nil || published != "" {
			return true
		}
		proof = &retiredPrepareCandidateCleanupProof{AcknowledgementPath: retiredcandidateack.Path(receiptPath)}
		return true
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read worktree-merge reports: %w", err)
	}
	return proof, nil
}

// hasRetiredPrepareCandidateAcknowledgement is intentionally independent of
// orchestrate. It is the admission boundary for current-layout adoption. It
// validates only immutable sidecar/receipt identity; destructive lifecycle
// later repeats the fresh-default and unpublished-candidate proof above.
func hasRetiredPrepareCandidateAcknowledgement(projectsRoot, task, worktree, branch, head string) bool {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return false
	}
	cleanWorktree := filepath.Clean(worktree)
	var found bool
	err = forEachWorktreeMergeReceipt(home, func(path string, bytes []byte) bool {
		var receipt retiredPrepareCandidateReceipt
		if json.Unmarshal(bytes, &receipt) != nil || !receipt.validLegacyEmptyCandidate(path) {
			return false
		}
		identity := receipt.identity()
		if identity.Candidate.Task != task || filepath.Clean(identity.Candidate.Worktree) != cleanWorktree ||
			identity.Candidate.Branch != branch || identity.Candidate.SHA != head {
			return false
		}
		_, loadErr := retiredcandidateack.Load(retiredcandidateack.Path(path), identity)
		found = loadErr == nil
		return true
	})
	if err != nil {
		return false
	}
	return found
}

func candidateBranchHead(ctx context.Context, worktree string) string {
	head, err := git(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(head)
}
