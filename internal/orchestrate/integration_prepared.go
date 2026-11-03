package orchestrate

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const preparedRecordPrefix = "WB prepared merge provenance (CI deferred; not review approval)\n"

// PreparedProvenance never represents completed validation or historical approval.
// PublishedHead and PullRequest are the exact adoption-time observation, not a future CI pass.
type PreparedDeferral struct {
	Route        WorktreeMergeRoute `json:"route"`
	CandidateSHA string             `json:"candidate_sha"`
	RecordedAt   time.Time          `json:"recorded_at"`
	Policy       string             `json:"policy"`
}
type PreparedProvenance struct {
	Status        string           `json:"status"`
	PublishedHead string           `json:"published_head"`
	PullRequest   string           `json:"pull_request"`
	Deferral      PreparedDeferral `json:"deferral"`
}

func validatePreparedRecord(r LegacyProvenance) error {
	p := r.Prepared
	if p == nil || r.Version != 2 || p.Status != "prepared-ci-deferred" || !legacySHA.MatchString(p.PublishedHead) ||
		!legacyRepositoryPattern.MatchString(r.Repository) || !validBranchName(r.Integration) || !validBranchName(r.To) || r.Integration == r.To ||
		!legacyAuditAtom.MatchString(r.ReceiptID) || !legacyDigest.MatchString(r.ReceiptDigest) || !safeLegacyIdentities(r.Adopter, r.OriginalMerger) || !identityComplete(r.Adopter) ||
		!legacySHA.MatchString(r.TargetSHA) || !legacySHA.MatchString(r.CandidateSHA) || len(r.Sources) != 1 || len(r.Merges) != 1 ||
		r.WB != "" || len(r.PassedChecks) != 0 || r.ValidationDigest != "" || legacyJSON(r.Validation) != legacyJSON(WorktreeMergeValidationIdentity{}) {
		return fmt.Errorf("invalid prepared provenance identity or fabricated validation")
	}
	m := r.Merges[0]
	_, canonical, prErr := canonicalLegacyRemovedPullRequestURL(p.PullRequest, r.Repository)
	if prErr != nil || canonical != p.PullRequest || m.SHA != r.CandidateSHA || m.FirstParent != r.TargetSHA || m.SecondParent != r.Sources[0] ||
		!legacySHA.MatchString(m.SecondParent) || m.FirstParent == m.SecondParent || m.SHA == m.FirstParent || m.SHA == m.SecondParent ||
		p.Deferral.Route != WorktreeMergeRoutePullRequest || p.Deferral.CandidateSHA != r.CandidateSHA || p.Deferral.RecordedAt.IsZero() ||
		p.Deferral.Policy != "authoritative-required-checks" {
		return fmt.Errorf("invalid prepared merge/PR/deferral identity")
	}
	return nil
}

func adoptPreparedIntegration(ctx context.Context, o LegacyAdoptOptions) (LegacyAdoptResult, error) {
	return adoptPreparedIntegrationWithSnapshot(ctx, o, readWorktreeMergeReceiptSnapshot)
}
func adoptPreparedIntegrationWithSnapshot(ctx context.Context, o LegacyAdoptOptions, read func(string) (WorktreeMergeReceipt, string, error)) (LegacyAdoptResult, error) {
	var empty LegacyAdoptResult
	root, err := worktrees.AbsoluteProjectsRoot(o.ProjectsRoot)
	if err != nil {
		return empty, err
	}
	path, err := resolveWorktreeMergeReceiptPath(root, o.Receipt)
	if err != nil {
		return empty, err
	}
	receipt, _, err := read(path)
	if err != nil {
		return empty, err
	}
	lock, err := AcquireOperationLock(root, receipt.Lane, true)
	if err != nil {
		return empty, err
	}
	defer func() { _ = lock.Release() }()
	receipt, digest, err := read(path)
	if err != nil {
		return empty, err
	}
	actor := o.actor
	if actor == (ReviewerIdentity{}) {
		actor = currentSessionIdentity()
	}
	if err = validatePreparedReceipt(receipt, path, o, actor); err != nil {
		return empty, err
	}
	git := o.checkoutGit()
	head, err := git.out(ctx, "rev-parse", "HEAD")
	if err != nil {
		return empty, err
	}
	record := LegacyProvenance{Version: 2, Repository: o.Repository, Integration: o.Integration, To: o.To, ReceiptID: receipt.ID, ReceiptDigest: "sha256:" + digest, TargetSHA: receipt.TargetSHA, CandidateSHA: receipt.Candidate.SHA, Sources: []string{receipt.Sources[0].SHA}, Merges: []LegacyMerge{{receipt.Candidate.SHA, receipt.TargetSHA, receipt.Sources[0].SHA}}, Adopter: actor, OriginalMerger: receipt.LaneOwner.Owner.WBSessionID}
	// Prove all mutable custody/publication/policy facts twice around archive discovery.
	prove := func() error {
		_, now, readErr := read(path)
		if readErr != nil {
			return readErr
		}
		if "sha256:"+now != record.ReceiptDigest {
			return fmt.Errorf("prepared receipt changed")
		}
		candidate := receipt.Candidate
		candidate.SHA = head
		claim, proofErr := validateMergeAcknowledgementCandidateWithRunner(ctx, git.run, root, receipt, candidate)
		if proofErr != nil {
			return proofErr
		}
		identity, registered := worktrees.RegisteredIdentity()
		if !registered || identity.AgentID == "" || identity.WBSessionID == "" || identity.WBSessionID != actor.Session || identity.Runtime != actor.Harness || identity.Model != actor.Model ||
			claim.AgentID != identity.AgentID || claim.AgentRuntime != identity.Runtime || claim.Model != identity.Model {
			return fmt.Errorf("prepared candidate has foreign/unknown owner")
		}
		if err := validatePublishedCandidateAdoptionSources(ctx, git.run, receipt); err != nil {
			return err
		}
		branch, e := git.out(ctx, "branch", "--show-current")
		if e != nil {
			return e
		}
		if branch != o.Integration {
			return fmt.Errorf("candidate branch drifted")
		}
		chain, e := git.out(ctx, "rev-list", "--first-parent", head)
		if e != nil {
			return e
		}
		if !containsExactLine(chain, receipt.Candidate.SHA) {
			return fmt.Errorf("prepared candidate is not a first-parent ancestor")
		}
		parents, e := git.out(ctx, "show", "-s", "--format=%P%x00%B", receipt.Candidate.SHA)
		if e != nil {
			return e
		}
		parentText, message, _ := strings.Cut(parents, "\x00")
		if parentText != receipt.TargetSHA+" "+receipt.Sources[0].SHA || carriesLaneTrailer(SourceCommit{Body: message}) {
			return fmt.Errorf("prepared merge parents/trailers changed")
		}
		origin, e := git.out(ctx, "remote", "get-url", "origin")
		if e != nil {
			return e
		}
		parsed, e := gitremote.Parse(origin)
		if e != nil || parsed.Identity.Host() != "github.com" {
			return fmt.Errorf("prepared origin is not authoritative GitHub")
		}
		remote, e := hostedRepositoryForCandidate(ctx, git.run, receipt)
		if e != nil {
			return e
		}
		if remote != o.Repository {
			return fmt.Errorf("prepared origin identity differs")
		}
		number, _, e := o.resolveGitHub().OpenPullRequest(ctx, o.Repository, o.Integration, o.To)
		if e != nil {
			return e
		}
		if number <= 0 {
			return fmt.Errorf("prepared candidate requires an open PR")
		}
		selector := fmt.Sprintf("https://github.com/%s/pull/%d", o.Repository, number)
		live := receipt
		live.Candidate = candidate
		live.Target = o.To
		if e = provePublishedCandidatePullRequest(ctx, git.run, live, selector); e != nil {
			return e
		}
		policy, reason := worktreeMergeValidationDeferralEligible(ctx, o.Repository, o.To, false)
		if !policy {
			return fmt.Errorf("prepared CI deferral no longer authoritative: %s", reason)
		}
		observation := &PreparedProvenance{Status: "prepared-ci-deferred", PublishedHead: head, PullRequest: selector, Deferral: PreparedDeferral{Route: receipt.ValidationDeferral.Route, CandidateSHA: receipt.ValidationDeferral.CandidateSHA, RecordedAt: receipt.ValidationDeferral.RecordedAt, Policy: "authoritative-required-checks"}}
		if record.Prepared != nil && legacyJSON(record.Prepared) != legacyJSON(observation) {
			return fmt.Errorf("prepared publication changed")
		}
		record.Prepared = observation
		return validatePreparedRecord(record)
	}
	if err = prove(); err != nil {
		return empty, err
	}
	o.beforeArchive = prove
	return archiveProvenance(ctx, o, record)
}

func validatePreparedReceipt(r WorktreeMergeReceipt, path string, o LegacyAdoptOptions, actor ReviewerIdentity) error {
	if !legacyRepositoryPattern.MatchString(o.Repository) || !validBranchName(o.Integration) || !validBranchName(o.To) ||
		!legacySHA.MatchString(r.TargetSHA) || !legacySHA.MatchString(r.Candidate.SHA) ||
		r.SchemaVersion != 1 || r.ReceiptPath != path || r.Lane != worktreeMergeLaneID(r.Repository, r.Target) || r.Repository != o.Repository || r.Target != o.To ||
		(r.Route.Route != "" && r.Route.Route != WorktreeMergeRoutePullRequest) || r.Phase != WorktreeMergePhasePrepare || r.Status != WorktreeMergePrepared || r.ID == "" || r.Candidate.Task != r.ID || r.Candidate.Branch != o.Integration || filepath.Clean(r.Candidate.Worktree) != filepath.Clean(o.Worktree) ||
		!strings.HasPrefix(r.Candidate.Branch, "wb/integration/"+r.Target+"/") || len(r.Sources) != 1 || !r.Sources[0].Merged || !legacySHA.MatchString(r.Sources[0].SHA) || !validBranchName(r.Sources[0].Branch) ||
		r.ValidationDeferral == nil || r.Validation.Status != quality.StatusSkipped || r.Validation.Revision != r.Candidate.SHA || !r.Validation.WorkspaceClean ||
		len(r.Validation.Results) != 1 || r.Validation.Results[0].Status != quality.StatusSkipped || r.ValidationIdentity != nil || r.PullRequest != "" || r.PublishedCandidateSHA != "" || r.LandingSHA != "" || r.Rebase != nil || r.RevertOf != nil || r.RebatchOf != "" ||
		len(r.TargetRefreshes) > 0 || len(r.SourceRefreshes) > 0 || len(r.ForwardRepairs) > 0 || r.LaneOwner == nil || r.LaneOwner.Owner.WBSessionID == "" || !safeLegacyIdentities(actor, r.LaneOwner.Owner.WBSessionID) || !identityComplete(actor) {
		return fmt.Errorf("unsupported prepared receipt or foreign adopter")
	}
	if r.ValidationDeferral.Reason == "" || r.ValidationDeferral.Reason != r.Validation.Results[0].Detail {
		return fmt.Errorf("prepared policy evidence differs")
	}
	return nil
}
func containsExactLine(lines, value string) bool {
	for _, line := range strings.Fields(lines) {
		if line == value {
			return true
		}
	}
	return false
}
