package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// RetiredPrepareTreeProof distinguishes exact content integration from commit ancestry.
// Each cleanup digest follows receipt order: candidate first, then the original sources.
type RetiredPrepareTreeProof struct {
	// DiscardedSourceHead records the final, separately absorbed source in the
	// one-source variant. The candidate's clean merge was explicitly discarded.
	DiscardedSourceHead string   `json:"discarded_source_head,omitempty"`
	ReceiptSHA256       string   `json:"receipt_sha256"`
	Candidate           string   `json:"candidate"`
	Tree                string   `json:"tree"`
	Landing             string   `json:"landing"`
	CleanupDigests      []string `json:"cleanup_digests"`
	SourcePRs           []int    `json:"source_prs"`
	SourceHeads         []string `json:"source_heads"`
	SourceLandings      []string `json:"source_landings"`
}

func applicableRetiredPrepare(r WorktreeMergeReceipt) bool {
	id := r.ValidationIdentity
	if r.Phase != WorktreeMergePhasePrepare || r.Status != WorktreeMergePrepared || r.PullRequest != "" || r.LandingSHA != "" || r.PublishedCandidateSHA != "" || r.ValidationDeferral != nil || len(r.SourceRefreshes) != 0 {
		return false
	}
	if len(r.Sources) != 1 && len(r.Sources) != 2 || !r.Sources[0].Merged {
		return false
	}
	if len(r.Sources) == 2 && (!r.Sources[1].Merged || r.Sources[0].SHA == r.Sources[1].SHA || r.Sources[0].Branch == r.Sources[1].Branch) {
		return false
	}
	for _, sha := range append(sourceSHAs(r.Sources), r.TargetSHA, r.Candidate.SHA) {
		if !fullSHAPattern.MatchString(sha) {
			return false
		}
	}
	return r.Validation.Status == "passed" && r.Validation.WorkspaceClean && r.Validation.Revision == r.Candidate.SHA &&
		id != nil && id.CandidateSHA == r.Candidate.SHA && id.TargetSHA == r.TargetSHA && slices.Equal(id.SourceSHAs, sourceSHAs(r.Sources))
}
func (p *RetiredPrepareTreeProof) matches(r WorktreeMergeReceipt) bool {
	if !applicableRetiredPrepare(r) || p.Candidate != r.Candidate.SHA || !fullSHAPattern.MatchString(p.Tree) || !fullSHAPattern.MatchString(p.Landing) || len(p.CleanupDigests) != len(r.Sources)+1 || len(p.SourcePRs) != len(r.Sources) || p.SourcePRs[0] < 1 || len(p.SourceHeads) != len(r.Sources) || len(p.SourceLandings) != len(r.Sources) {
		return false
	}
	if len(r.Sources) == 1 {
		if !fullSHAPattern.MatchString(p.DiscardedSourceHead) || p.Landing != p.SourceLandings[0] {
			return false
		}
	} else if p.DiscardedSourceHead != "" || p.SourcePRs[1] < 1 || p.SourcePRs[0] == p.SourcePRs[1] {
		return false
	}
	for _, s := range append(slices.Clone(p.CleanupDigests), p.ReceiptSHA256) {
		b, err := hex.DecodeString(s)
		if err != nil || len(b) != sha256.Size {
			return false
		}
	}
	for _, s := range append(slices.Clone(p.SourceHeads), p.SourceLandings...) {
		if !fullSHAPattern.MatchString(s) {
			return false
		}
	}
	return true
}

func retiredPrepareParents(r WorktreeMergeReceipt) (string, string) {
	if len(r.Sources) == 1 {
		return r.TargetSHA, r.Sources[0].SHA
	}
	return r.Sources[0].SHA, r.Sources[1].SHA
}

// The native object hash is checked before these headers are trusted.
func originalPrepareTree(raw []byte, r WorktreeMergeReceipt) (string, error) {
	left, right := retiredPrepareParents(r)
	header, _, ok := strings.Cut(string(raw), "\n\n")
	lines := strings.Split(header, "\n")
	if !ok || len(lines) != 5 || !strings.HasPrefix(lines[0], "tree ") || !strings.HasPrefix(lines[3], "author ") || !strings.HasPrefix(lines[4], "committer ") || lines[1] != "parent "+left || lines[2] != "parent "+right {
		return "", errors.New("candidate is not the original ordered prepare merge")
	}
	tree := strings.TrimPrefix(lines[0], "tree ")
	if !fullSHAPattern.MatchString(tree) {
		return "", errors.New("invalid candidate tree")
	}
	return tree, nil
}

func acknowledgeRetiredPrepareTree(ctx context.Context, run runner.Runner, o WorktreeMergeTargetAbsorbedSweepOptions, now time.Time, readPR func(context.Context, string, ...string) (string, error)) (result WorktreeMergeTargetAbsorbedSweepResult, err error) {
	path, err := resolveWorktreeMergeReceiptPath(o.ProjectsRoot, o.Receipts[0])
	if err != nil {
		return result, err
	}
	r, err := readWorktreeMergeReceipt(path)
	if err != nil {
		return result, err
	}
	lane := worktreeMergeLaneID(r.Repository, r.Target)
	if r.Lane != lane {
		return result, errors.New("wrong historical merge lane")
	}
	lock, err := AcquireOperationLock(o.ProjectsRoot, lane, true)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()
	r, digest, err := readWorktreeMergeReceiptSnapshot(path)
	if err != nil {
		return result, err
	}
	if r.Lane != lane || worktreeMergeOperationID(lane, r.Sources) != r.ID || r.Candidate.Task != r.ID || r.ReceiptPath != path || !applicableRetiredPrepare(r) {
		return result, errors.New("requires one validated unpublished, unrefreshed one- or two-source prepare")
	}
	raw, err := readSecureBoundedRegularFile(o.CandidateCommitFile, 65536)
	if err != nil {
		return result, err
	}
	tree, err := originalPrepareTree(raw, r)
	if err != nil {
		return result, err
	}
	canonical, err := worktrees.CanonicalRepositoryPath(o.ProjectsRoot, r.Repository)
	if err != nil {
		return result, err
	}
	target, err := fetchExactMergeTargetWithRunner(ctx, run, canonical, r.Target)
	if err != nil {
		return result, err
	}
	scratch, err := os.MkdirTemp("", "wb-retired-prepare-")
	if err != nil {
		return result, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	objects := filepath.Join(scratch, "objects.git")
	if err := verifyRetiredPrepareMergeTree(ctx, run, canonical, objects, raw, r, tree); err != nil {
		return result, err
	}
	proof, err := collectRetiredPrepareProof(ctx, run, o.ProjectsRoot, canonical, objects, r, digest, tree, target, readPR)
	if err != nil {
		return result, err
	}
	result = WorktreeMergeTargetAbsorbedSweepResult{Receipt: path, Lane: lane, Status: string(r.Status), TargetSHA: target, RetiredPrepare: proof, Outcome: WorktreeMergeTargetAbsorbedWouldRelease}
	if existing, ackPath, err := readWorktreeMergeTargetAbsorbedAcknowledgement(r); err == nil {
		if !reflect.DeepEqual(existing.RetiredPrepare, proof) {
			return result, errors.New("different historical absorption already recorded")
		}
		result.Outcome = WorktreeMergeTargetAbsorbedAlreadyReleased
		result.AcknowledgementPath = ackPath
		return result, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if !o.Apply {
		return result, nil
	}
	final, err := collectRetiredPrepareProof(ctx, run, o.ProjectsRoot, canonical, objects, r, digest, tree, target, readPR)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(final, proof) {
		return result, errors.New("terminal evidence changed during proof")
	}
	current, err := fetchExactMergeTargetWithRunner(ctx, run, canonical, r.Target)
	if err != nil {
		return result, err
	}
	if current != target {
		return result, errors.New("target moved during historical proof")
	}
	commits := absorptionEvidenceCommits(r, proof)
	recorded, ackPath, err := recordWorktreeMergeAbsorptionEvidence(r, target, commits, o.Actor, o.Reason, now, proof)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(recorded.RetiredPrepare, proof) {
		return result, errors.New("different historical absorption already recorded")
	}
	result.Outcome = WorktreeMergeTargetAbsorbedReleased
	result.AcknowledgementPath = ackPath
	return result, nil
}

func collectRetiredPrepareProof(ctx context.Context, run runner.Runner, root, canonical, objects string, r WorktreeMergeReceipt, digest, tree, target string, readPR func(context.Context, string, ...string) (string, error)) (*RetiredPrepareTreeProof, error) {
	proof := &RetiredPrepareTreeProof{ReceiptSHA256: digest, Candidate: r.Candidate.SHA, Tree: tree}
	members := append([]WorktreeMergeSource{{Task: r.Candidate.Task, Branch: r.Candidate.Branch, Worktree: r.Candidate.Worktree, SHA: r.Candidate.SHA}}, r.Sources...)
	for i, m := range members {
		if err := requireAbsentRetiredSource(ctx, canonical, m, run); err != nil {
			return nil, err
		}
		if err := requireAbsorbedConflictBranchAbsentWithRunner(ctx, run, canonical, m.Branch); err != nil {
			return nil, err
		}
		if i == 0 && len(r.Sources) == 1 {
			if err := requireCandidateWithoutPullRequest(ctx, canonical, r, readPR); err != nil {
				return nil, err
			}
			discard, err := worktrees.FindDiscardedLifecycleBacklogProof(ctx, root, r.Repository, r.Target, m.Task, m.Worktree, m.Branch, m.SHA)
			if err != nil {
				return nil, err
			}
			proof.CleanupDigests = append(proof.CleanupDigests, discard.FileSHA256)
			continue
		}
		cleanup, err := worktrees.FindTerminalCleanupProof(root, r.Repository, r.Target, m.Task, m.Worktree, m.Branch)
		if err != nil {
			return nil, err
		}
		proof.CleanupDigests = append(proof.CleanupDigests, cleanup.ReportSHA256)
		c := cleanup.Result
		if i == 0 {
			if err := requireCandidateWithoutPullRequest(ctx, canonical, r, readPR); err != nil {
				return nil, err
			}
			if c.HeadSHA != m.SHA || !c.AbsorbedAtOrigin || c.AbsorbedBySHA != c.RemoteTargetSHA || !fullSHAPattern.MatchString(c.AbsorbedBySHA) {
				return nil, errors.New("candidate cleanup does not identify an exact absorbed landing")
			}
			proof.Landing = c.AbsorbedBySHA
			landedTree, _, err := runCommand(ctx, run, 0, 0, objects, "git", "--no-replace-objects", "rev-parse", "--verify", proof.Landing+"^{tree}")
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(landedTree) != tree {
				return nil, errors.New("candidate tree differs from observed landing")
			}
			continue
		}
		pr := c.MergedPullRequest
		if pr == nil || pr.State != "MERGED" || pr.Number < 1 || pr.Merged == nil || pr.Repository != r.Repository || pr.Base != r.Target || !fullSHAPattern.MatchString(pr.MergeSHA) || !fullSHAPattern.MatchString(pr.HeadSHA) {
			return nil, errors.New("source cleanup lacks original merged pull request")
		}
		wire, err := readPR(ctx, canonical, "pr", "view", strconv.Itoa(pr.Number), "--repo", r.Repository, "--json", "state,mergedAt,headRefOid,baseRefName,mergeCommit,headRefName")
		if err != nil {
			return nil, err
		}
		var live struct {
			strandedPullRequestLandingView
			HeadRefName string `json:"headRefName"`
		}
		if err := json.Unmarshal([]byte(wire), &live); err != nil {
			return nil, err
		}
		if live.State != "MERGED" || live.MergedAt == "" || live.BaseRefName != r.Target || live.HeadRefOID != pr.HeadSHA || live.MergeCommit.OID != pr.MergeSHA {
			return nil, errors.New("remote pull request differs from terminal cleanup")
		}
		if len(r.Sources) == 1 {
			if !c.AbsorbedAtOrigin || c.AbsorbedBySHA != pr.MergeSHA || c.RemoteTargetSHA != pr.MergeSHA || !fullSHAPattern.MatchString(c.HeadSHA) {
				return nil, errors.New("source cleanup does not identify the original absorbed head and landing")
			}
			for _, pair := range [][2]string{{m.SHA, c.HeadSHA}, {c.HeadSHA, pr.HeadSHA}, {r.TargetSHA, pr.MergeSHA}} {
				if err := requireRetiredPrepareAncestor(ctx, run, objects, pair[0], pair[1]); err != nil {
					return nil, err
				}
			}
			proof.DiscardedSourceHead = c.HeadSHA
			proof.Landing = pr.MergeSHA
		} else if live.HeadRefName != m.Branch || pr.HeadSHA != c.HeadSHA {
			return nil, errors.New("remote pull request differs from terminal cleanup")
		}
		proof.SourcePRs = append(proof.SourcePRs, pr.Number)
		for _, pair := range [][2]string{{m.SHA, pr.HeadSHA}, {pr.HeadSHA, pr.MergeSHA}, {pr.MergeSHA, proof.Landing}} {
			if err := requireRetiredPrepareAncestor(ctx, run, objects, pair[0], pair[1]); err != nil {
				return nil, err
			}
		}
		proof.SourceHeads = append(proof.SourceHeads, pr.HeadSHA)
		proof.SourceLandings = append(proof.SourceLandings, pr.MergeSHA)
	}
	if len(r.Sources) == 2 {
		if err := requireRetiredPrepareAncestor(ctx, run, objects, r.TargetSHA, r.Sources[0].SHA); err != nil {
			return nil, err
		}
	}
	if err := requireRetiredPrepareAncestor(ctx, run, objects, proof.Landing, target); err != nil {
		return nil, err
	}
	tip, err := readPR(ctx, canonical, "api", "repos/"+r.Repository+"/git/ref/heads/"+r.Target, "--jq", ".object.sha")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(tip) != target {
		return nil, errors.New("repository target differs from fetched origin")
	}
	if !proof.matches(r) {
		return nil, errors.New("incomplete retired prepare proof")
	}
	return proof, nil
}
