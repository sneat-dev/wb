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
)

// retiredPrepareCandidateReceipt is the smallest immutable prepare/conflict
// receipt shape this recovery accepts. A receipt that was ever published,
// landed, or recorded an actual candidate SHA must use its existing lifecycle
// path instead.
type retiredPrepareCandidateReceipt struct {
	ReceiptPath           string                       `json:"receipt_path"`
	ID                    string                       `json:"id"`
	Phase                 string                       `json:"phase"`
	Status                string                       `json:"status"`
	Lane                  string                       `json:"lane"`
	Repository            string                       `json:"repository"`
	Target                string                       `json:"target"`
	TargetSHA             string                       `json:"target_sha"`
	LandingSHA            string                       `json:"landing_sha"`
	PullRequest           string                       `json:"pull_request"`
	PublishedCandidateSHA string                       `json:"published_candidate_sha"`
	Candidate             retiredcandidateack.Source   `json:"candidate"`
	Sources               []retiredcandidateack.Source `json:"sources"`
}

func (receipt retiredPrepareCandidateReceipt) identity() retiredcandidateack.ReceiptIdentity {
	candidate := receipt.Candidate
	candidate.SHA = receipt.TargetSHA
	return retiredcandidateack.ReceiptIdentity{
		Path: receipt.ReceiptPath, ID: receipt.ID, Phase: receipt.Phase,
		Status: receipt.Status, Lane: receipt.Lane, Repository: receipt.Repository,
		Target: receipt.Target, TargetSHA: receipt.TargetSHA, Candidate: candidate,
		Sources: receipt.Sources,
	}
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
	entries, err := os.ReadDir(filepath.Join(home, "reports", "worktree-merge"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read worktree-merge reports: %w", err)
	}
	cleanWorktree := filepath.Clean(worktreeDir)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.Contains(entry.Name(), ".ack.json") {
			continue
		}
		receiptPath := filepath.Join(home, "reports", "worktree-merge", entry.Name())
		bytes, readErr := os.ReadFile(receiptPath)
		if readErr != nil {
			continue
		}
		var receipt retiredPrepareCandidateReceipt
		if json.Unmarshal(bytes, &receipt) != nil || !receipt.validLegacyEmptyCandidate(receiptPath) {
			continue
		}
		identity := receipt.identity()
		if identity.Candidate.Task != task || filepath.Clean(identity.Candidate.Worktree) != cleanWorktree ||
			identity.Candidate.Branch != branch || identity.Candidate.SHA != head {
			continue
		}
		status, statusErr := git(ctx, worktreeDir, "status", "--porcelain")
		if statusErr != nil || strings.TrimSpace(status) != "" {
			return nil, nil
		}
		ack, loadErr := retiredcandidateack.Load(retiredcandidateack.Path(receiptPath), identity)
		if loadErr != nil || ack.DefaultBranch != defaultBranch {
			return nil, nil
		}
		// A later force-push or default-branch replacement cannot reuse a stale
		// acknowledgement: preserve both the acknowledged fetched object and
		// the precise candidate's present containment.
		ackContained, ackErr := isAncestor(ctx, canonicalDir, ack.DefaultSHA, defaultSHA)
		candidateContained, candidateErr := isAncestor(ctx, canonicalDir, head, defaultSHA)
		if ackErr != nil || candidateErr != nil || !ackContained || !candidateContained {
			return nil, nil
		}
		published, publishErr := remoteBranchHead(ctx, canonicalDir, branch)
		if publishErr != nil || published != "" {
			return nil, nil
		}
		return &retiredPrepareCandidateCleanupProof{AcknowledgementPath: retiredcandidateack.Path(receiptPath)}, nil
	}
	return nil, nil
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
	entries, err := os.ReadDir(filepath.Join(home, "reports", "worktree-merge"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.Contains(entry.Name(), ".ack.json") {
			continue
		}
		path := filepath.Join(home, "reports", "worktree-merge", entry.Name())
		bytes, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var receipt retiredPrepareCandidateReceipt
		if json.Unmarshal(bytes, &receipt) != nil || !receipt.validLegacyEmptyCandidate(path) {
			continue
		}
		identity := receipt.identity()
		if identity.Candidate.Task != task || filepath.Clean(identity.Candidate.Worktree) != filepath.Clean(worktree) ||
			identity.Candidate.Branch != branch || identity.Candidate.SHA != head {
			continue
		}
		_, err = retiredcandidateack.Load(retiredcandidateack.Path(path), identity)
		return err == nil
	}
	return false
}

func candidateBranchHead(ctx context.Context, worktree string) string {
	head, err := git(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(head)
}
