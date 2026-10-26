package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const worktreeMergeUnpublishedConflictSuffix = ".unpublished-conflict.ack.json"

// UnpublishedConflictClaim binds the exact immutable claim bytes inspected
// while preserving one checkout. It contains no prompt or credential data.
type UnpublishedConflictClaim struct {
	Task         string `json:"task"`
	Worktree     string `json:"worktree"`
	Branch       string `json:"branch"`
	SHA          string `json:"sha"`
	ClaimPath    string `json:"claim_path"`
	ClaimID      string `json:"claim_id"`
	ClaimSHA256  string `json:"claim_sha256"`
	ClaimBaseSHA string `json:"claim_base_sha"`
}

// WorktreeMergeUnpublishedConflictAcknowledgement releases only a historical
// receipt's lane claim. It grants neither landing nor cleanup authority.
type WorktreeMergeUnpublishedConflictAcknowledgement struct {
	SchemaVersion       int                               `json:"schema_version"`
	ID                  string                            `json:"id"`
	Status              string                            `json:"status"`
	ReceiptPath         string                            `json:"receipt_path"`
	AcknowledgementPath string                            `json:"acknowledgement_path"`
	ReceiptID           string                            `json:"receipt_id"`
	ReceiptSHA256       string                            `json:"receipt_sha256"`
	Lane                string                            `json:"lane"`
	Repository          string                            `json:"repository"`
	Target              string                            `json:"target"`
	ReceiptTargetSHA    string                            `json:"receipt_target_sha"`
	CurrentTargetSHA    string                            `json:"current_target_sha"`
	Candidate           WorktreeMergeCandidate            `json:"candidate"`
	Sources             []WorktreeMergeSource             `json:"sources"`
	Claims              []UnpublishedConflictClaim        `json:"preserved_claims"`
	RebatchOf           string                            `json:"rebatch_of,omitempty"`
	RebatchedCandidates []WorktreeMergeCandidate          `json:"rebatched_candidates,omitempty"`
	RebatchProofs       []UnpublishedConflictRebatchProof `json:"rebatch_proofs,omitempty"`
	Actor               string                            `json:"actor"`
	Reason              string                            `json:"reason"`
	RecordedAt          time.Time                         `json:"recorded_at"`
}

type WorktreeMergeUnpublishedConflictOptions struct {
	ProjectsRoot    string
	Receipt         string
	Apply           bool
	Actor           string
	Reason          string
	ExpectedProofID string
}

// AcknowledgeUnpublishedConflict is preview-only until the caller supplies the
// exact proof ID from a prior preview, actor, reason, and Apply. It never
// rewrites the receipt, claims, branches, worktrees, or remote refs.
func AcknowledgeUnpublishedConflict(ctx context.Context, options WorktreeMergeUnpublishedConflictOptions) (WorktreeMergeUnpublishedConflictAcknowledgement, error) {
	return acknowledgeUnpublishedConflict(ctx, options, nativeUnpublishedConflictPorts())
}

func acknowledgeUnpublishedConflict(ctx context.Context, options WorktreeMergeUnpublishedConflictOptions, p unpublishedConflictPorts) (WorktreeMergeUnpublishedConflictAcknowledgement, error) {
	var zero WorktreeMergeUnpublishedConflictAcknowledgement
	path, err := p.resolve(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return zero, err
	}
	r, err := p.read(path)
	if err != nil {
		return zero, err
	}
	if err := validateUnpublishedConflictReceipt(r); err != nil {
		return zero, err
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "" || options.ExpectedProofID == "") {
		return zero, errors.New("--apply requires --actor, --reason, and --expected-proof-id from a dry-run with the same actor and reason")
	}
	pinnedReceipt := r
	lock, err := p.lock(options.ProjectsRoot, r.Lane, true)
	if err != nil {
		return zero, err
	}
	defer func() { _ = lock.Release() }()
	r, err = p.read(path)
	if err != nil {
		return zero, err
	}
	if !reflect.DeepEqual(r, pinnedReceipt) {
		return zero, errors.New("receipt identity changed across lane-lock acquisition")
	}
	ack, err := p.inspect(ctx, options.ProjectsRoot, r, options.Actor, options.Reason)
	if err != nil {
		return zero, err
	}
	if options.Apply && options.ExpectedProofID != ack.ID {
		return zero, errors.New("unpublished conflict proof changed; rerun the dry-run and inspect the exact receipt, claims, and target")
	}
	if existing, err := p.ack(r); err == nil {
		if existing.ID != ack.ID {
			return zero, errors.New("immutable unpublished-conflict acknowledgement binds different evidence")
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return zero, err
	}
	if !options.Apply {
		return ack, nil
	}
	// Refresh every native proof once more immediately before publication.
	latest, err := p.read(path)
	if err != nil {
		return zero, err
	}
	if !reflect.DeepEqual(latest, pinnedReceipt) {
		return zero, errors.New("receipt identity changed beneath the held lane lock")
	}
	confirmed, err := p.inspect(ctx, options.ProjectsRoot, latest, options.Actor, options.Reason)
	if err != nil {
		return zero, err
	}
	if confirmed.ID != ack.ID {
		return zero, errors.New("unpublished conflict evidence changed before acknowledgement")
	}
	if err := p.persist(ack.AcknowledgementPath, ack); err != nil {
		return zero, err
	}
	return ack, nil
}

func validateUnpublishedConflictReceipt(r WorktreeMergeReceipt) error {
	if r.SchemaVersion != WorktreeMergeSchemaVersion || r.Phase != WorktreeMergePhaseLand || r.Status != WorktreeMergeConflict ||
		r.PullRequest != "" || r.PublishedCandidateSHA != "" || r.LandingSHA != "" || r.Repository == "" || r.Target == "" || r.TargetSHA == "" ||
		r.Lane != worktreeMergeLaneID(r.Repository, r.Target) || !worktreeMergeOperationIDMatchesRecordedSourceSet(r) ||
		r.Candidate.Task != r.ID || r.Candidate.SHA == "" || r.Candidate.Worktree == "" || r.Candidate.Branch == "" || len(r.Sources) == 0 {
		return fmt.Errorf("receipt %s is not an exact unpublished land/conflict receipt with a recorded candidate SHA", r.ReceiptPath)
	}
	seen := map[string]bool{r.Target: true, r.Candidate.Branch: true}
	if r.Target == r.Candidate.Branch {
		return errors.New("candidate branch is the target")
	}
	for _, source := range r.Sources {
		if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" || seen[source.Branch] {
			return errors.New("receipt has incomplete or duplicate source identity")
		}
		seen[source.Branch] = true
	}
	return nil
}

func inspectUnpublishedConflict(ctx context.Context, root string, r WorktreeMergeReceipt, actor, reason string) (WorktreeMergeUnpublishedConflictAcknowledgement, error) {
	return inspectUnpublishedConflictWithPorts(ctx, root, r, actor, reason, nativeUnpublishedConflictInspectionPorts())
}

func inspectUnpublishedConflictWithPorts(ctx context.Context, root string, r WorktreeMergeReceipt, actor, reason string, p unpublishedConflictInspectionPorts) (WorktreeMergeUnpublishedConflictAcknowledgement, error) {
	var zero WorktreeMergeUnpublishedConflictAcknowledgement
	if err := validateUnpublishedConflictReceipt(r); err != nil {
		return zero, err
	}
	lineage, err := p.lineage(r)
	if err != nil {
		return zero, err
	}
	home, err := p.home(root)
	if err != nil {
		return zero, err
	}
	lane, found, err := p.lane(home, r.Repository, r.Target)
	if err != nil {
		return zero, err
	}
	if found {
		owner := lane.Owner
		_, liveSession := session.Lookup(filepath.Join(home, session.DirName), owner.PID)
		if liveSession || session.ProcessAlive(owner.OperationPID) {
			return zero, errors.New("landing lane has an active owner; release it through its owning session before recovery")
		}
	}
	assets := []WorktreeMergeSource{{Task: r.Candidate.Task, Worktree: r.Candidate.Worktree, Branch: r.Candidate.Branch, SHA: r.Candidate.SHA}}
	assets = append(assets, r.Sources...)
	for _, proof := range lineage {
		assets = append(assets, WorktreeMergeSource{Task: proof.Candidate.Task, Worktree: proof.Candidate.Worktree, Branch: proof.Candidate.Branch, SHA: proof.Candidate.SHA})
	}
	claims := make([]UnpublishedConflictClaim, 0, len(assets))
	for index, source := range assets {
		guard, err := p.guard(ctx, source.Worktree, worktrees.GuardOptions{ProjectsRoot: root, Base: r.Target})
		if err != nil {
			return zero, fmt.Errorf("guard preserved checkout: %w", err)
		}
		_, matches, err := p.canonical(root, r.Repository, guard.CanonicalDir)
		if err != nil {
			return zero, err
		}
		if !matches || guard.Kind != "linked" || guard.Transient || guard.Path != source.Worktree || guard.Branch != source.Branch {
			return zero, errors.New("preserved checkout has mismatched repository, root, or branch identity")
		}
		if err := p.clean(ctx, source.Worktree); err != nil {
			return zero, err
		}
		head, err := p.head(ctx, source.Worktree)
		if err != nil {
			return zero, err
		}
		if head != source.SHA {
			return zero, fmt.Errorf("preserved checkout %s HEAD differs from exact receipt SHA", source.Worktree)
		}
		view, err := p.view(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: root, Worktree: source.Worktree})
		if err != nil {
			return zero, err
		}
		if !mergeClaimMatchesIdentity(view.Claim, r.Repository, source.Task, source.Worktree, source.Branch) || view.Claim.Base != r.Target || view.Claim.BaseSHA == "" || view.Claim.ClaimID == "" {
			return zero, errors.New("preserved checkout lacks its exact active immutable claim identity")
		}
		if index == 0 && view.Claim.BaseSHA != r.TargetSHA {
			return zero, errors.New("candidate claim base differs from receipt target SHA")
		}
		if index > len(r.Sources) && view.Claim.BaseSHA != lineage[index-len(r.Sources)-1].TargetSHA {
			return zero, errors.New("rebatch predecessor claim base differs from its receipt target")
		}
		containsBase, err := p.ancestor(ctx, source.Worktree, view.Claim.BaseSHA, head)
		if err != nil {
			return zero, err
		}
		if !containsBase {
			return zero, errors.New("preserved checkout does not contain its immutable claim base")
		}
		for _, owner := range view.Owners {
			if owner.PID > 0 && owner.PIDStatus != "orphaned" {
				return zero, errors.New("preserved checkout has a live or ambiguous owner")
			}
		}
		digest, err := p.hash(view.Claim.ClaimPath)
		if err != nil {
			return zero, err
		}
		claims = append(claims, UnpublishedConflictClaim{Task: source.Task, Worktree: source.Worktree, Branch: source.Branch, SHA: head, ClaimPath: view.Claim.ClaimPath, ClaimID: view.Claim.ClaimID, ClaimSHA256: digest, ClaimBaseSHA: view.Claim.BaseSHA})
	}
	remote, err := p.remote(ctx, r.Candidate.Worktree, r.Candidate.Branch)
	if err != nil {
		return zero, err
	}
	if strings.TrimSpace(remote) != "" {
		return zero, errors.New("candidate branch is published; unpublished conflict recovery is refused")
	}
	if err := p.pullRequests(ctx, r.Candidate.Worktree, r); err != nil {
		return zero, err
	}
	target, err := p.target(ctx, r.Candidate.Worktree, r.Target)
	if err != nil {
		return zero, err
	}
	containsBase, err := p.ancestor(ctx, r.Candidate.Worktree, r.TargetSHA, target)
	if err != nil {
		return zero, err
	}
	if !containsBase {
		return zero, errors.New("remote target no longer contains the receipt target SHA")
	}
	landed, err := p.ancestor(ctx, r.Candidate.Worktree, r.Candidate.SHA, target)
	if err != nil {
		return zero, err
	}
	if landed {
		return zero, errors.New("candidate is reachable from the remote target; use landed recovery")
	}
	for _, proof := range lineage {
		if err := p.predecessorPublication(ctx, proof); err != nil {
			return zero, err
		}
		landed, err := p.ancestor(ctx, proof.Candidate.Worktree, proof.Candidate.SHA, target)
		if err != nil {
			return zero, err
		}
		if landed {
			return zero, errors.New("rebatch predecessor is already reachable from the remote target")
		}
	}
	digest, err := p.hash(r.ReceiptPath)
	if err != nil {
		return zero, err
	}
	ack := WorktreeMergeUnpublishedConflictAcknowledgement{SchemaVersion: 1, Status: "unpublished_conflict_preserved", ReceiptPath: r.ReceiptPath, AcknowledgementPath: r.ReceiptPath + worktreeMergeUnpublishedConflictSuffix, ReceiptID: r.ID, ReceiptSHA256: digest, Lane: r.Lane, Repository: r.Repository, Target: r.Target, ReceiptTargetSHA: r.TargetSHA, CurrentTargetSHA: target, Candidate: r.Candidate, Sources: append([]WorktreeMergeSource(nil), r.Sources...), Claims: claims, Actor: strings.TrimSpace(actor), Reason: strings.TrimSpace(reason), RecordedAt: time.Now().UTC()}
	ack.RebatchOf, ack.RebatchedCandidates, ack.RebatchProofs = r.RebatchOf, append([]WorktreeMergeCandidate(nil), r.RebatchedCandidates...), lineage
	ack.ID = unpublishedConflictProofID(ack)
	return ack, nil
}

func unpublishedConflictProofID(ack WorktreeMergeUnpublishedConflictAcknowledgement) string {
	ack.ID, ack.RecordedAt = "", time.Time{}
	data, _ := json.Marshal(ack)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func readUnpublishedConflictAcknowledgement(r WorktreeMergeReceipt) (WorktreeMergeUnpublishedConflictAcknowledgement, error) {
	var ack WorktreeMergeUnpublishedConflictAcknowledgement
	path := r.ReceiptPath + worktreeMergeUnpublishedConflictSuffix
	if err := readMergeAcknowledgement(path, "unpublished-conflict acknowledgement", &ack); err != nil {
		return ack, err
	}
	if err := validateUnpublishedConflictReceipt(r); err != nil {
		return ack, err
	}
	digest, err := worktreeMergeReceiptSHA256(r.ReceiptPath)
	if err != nil {
		return ack, err
	}
	lineage, err := unpublishedConflictRebatchLineage(r)
	if err != nil {
		return ack, err
	}
	if ack.RebatchOf != r.RebatchOf || !reflect.DeepEqual(ack.RebatchedCandidates, r.RebatchedCandidates) || !reflect.DeepEqual(ack.RebatchProofs, lineage) {
		return ack, errors.New("unpublished-conflict acknowledgement has invalid rebatch custody identity")
	}
	if ack.SchemaVersion != 1 || ack.Status != "unpublished_conflict_preserved" || ack.ID != unpublishedConflictProofID(ack) || ack.ReceiptPath != r.ReceiptPath || ack.AcknowledgementPath != path || ack.ReceiptSHA256 != digest || ack.ReceiptID != r.ID || ack.Lane != r.Lane || ack.Repository != r.Repository || ack.Target != r.Target || ack.ReceiptTargetSHA != r.TargetSHA || ack.Candidate != r.Candidate || !sameWorktreeMergeSources(ack.Sources, r.Sources) || ack.CurrentTargetSHA == "" || ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() || len(ack.Claims) != len(r.Sources)+1+len(lineage) {
		return ack, errors.New("unpublished-conflict acknowledgement has invalid immutable receipt identity")
	}
	assets := []WorktreeMergeSource{{Task: r.Candidate.Task, Worktree: r.Candidate.Worktree, Branch: r.Candidate.Branch, SHA: r.Candidate.SHA}}
	assets = append(assets, r.Sources...)
	for _, proof := range lineage {
		assets = append(assets, WorktreeMergeSource{Task: proof.Candidate.Task, Worktree: proof.Candidate.Worktree, Branch: proof.Candidate.Branch, SHA: proof.Candidate.SHA})
	}
	for i, asset := range assets {
		claim := ack.Claims[i]
		if claim.Task != asset.Task || claim.Worktree != asset.Worktree || claim.Branch != asset.Branch || claim.SHA != asset.SHA || claim.ClaimID == "" || claim.ClaimPath == "" || claim.ClaimBaseSHA == "" || len(claim.ClaimSHA256) != 64 {
			return ack, errors.New("unpublished-conflict acknowledgement has invalid preserved claim identity")
		}
	}
	return ack, nil
}

func hasUnpublishedConflictAcknowledgement(r WorktreeMergeReceipt) (bool, error) {
	_, err := readUnpublishedConflictAcknowledgement(r)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
