package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// legacyRemovedPullRequestIdentity is the canonical selector a receipt proved.
// It is returned once, so every later step reuses the same authenticated value
// instead of reparsing bytes that the lane-lock reread has already pinned.
type legacyRemovedPullRequestIdentity struct {
	Number string
	URL    string
}

// This compatibility evidence proves publication and current absence, never a
// successful cleanup transaction or a Work Log lifecycle transition.
func validateLegacyRemovedPRReceipt(r WorktreeMergeReceipt) (legacyRemovedPullRequestIdentity, error) {
	if err := validateLandedFailureAcknowledgementReceipt(r, r.ReceiptPath); err != nil {
		return legacyRemovedPullRequestIdentity{}, err
	}
	if r.Status != WorktreeMergePostTargetCIFailed || r.Route.Route != WorktreeMergeRoutePullRequest || !r.Cleanup ||
		r.PullRequest == "" || r.PublishedCandidateSHA == "" || r.PublishedCandidateSHA != r.Candidate.SHA || r.TargetSHA == "" ||
		len(r.Sources) == 0 || len(r.RebatchedCandidates) != 0 || r.ID != worktreeMergeOperationID(r.Lane, r.Sources) || r.Candidate.Task != r.ID {
		return legacyRemovedPullRequestIdentity{}, errors.New("legacy removed-PR recovery requires an exact failed post-target CI receipt, published PR candidate, and unambiguous cleanup identities")
	}
	// Every consumer of this shared receipt validator, including the historical
	// acknowledgement reader that frees a lane, must authenticate the stored pull
	// request against the receipt's own repository. A reader must never rely on
	// the writer having done so.
	number, canonical, err := canonicalLegacyRemovedPullRequestURL(r.PullRequest, r.Repository)
	if err != nil {
		return legacyRemovedPullRequestIdentity{}, err
	}
	identity := legacyRemovedPullRequestIdentity{Number: number, URL: canonical}
	_, err = terminalWorkLogExpectations(r)
	return identity, err
}

// canonicalLegacyRemovedPullRequestURL parses the receipt's stored pull request
// identity and binds it to the receipt's repository. GitHub's CLI resolves a
// full URL to its own repository, so passing the URL and --repo together can
// read a foreign pull request whose commits happen to be identical (forks can
// share SHAs). Only an exact canonical https://github.com/<owner>/<repository>/
// pull/<number> selector is accepted; every other spelling, including a URL
// with credentials, a port, a query, a fragment, an escaped path, or a
// different repository, refuses rather than being guessed at.
func canonicalLegacyRemovedPullRequestURL(selector, repository string) (number, canonical string, err error) {
	// The accepted selector is exact, so surrounding whitespace is refused
	// rather than silently normalized into a different identity.
	if strings.TrimSpace(selector) != selector {
		return "", "", errors.New("legacy pull request URL must not carry leading or trailing whitespace")
	}
	parsed, err := url.Parse(selector)
	if err != nil {
		return "", "", fmt.Errorf("parse legacy pull request URL: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
		return "", "", errors.New("legacy pull request URL must be an exact https://github.com/<owner>/<repository>/pull/<number> identity")
	}
	parts := strings.Split(parsed.Path, "/")
	if len(parts) != 5 || parts[0] != "" || parts[1] == "" || parts[2] == "" || parts[3] != "pull" || parts[4] == "" {
		return "", "", errors.New("legacy pull request URL must be an exact https://github.com/<owner>/<repository>/pull/<number> identity")
	}
	parsedRepository := parts[1] + "/" + parts[2]
	if parsedRepository != repository {
		return "", "", fmt.Errorf("legacy pull request URL names repository %s, not receipted repository %s", parsedRepository, repository)
	}
	number = parts[4]
	value, err := strconv.ParseUint(number, 10, 64)
	if err != nil || value == 0 || strconv.FormatUint(value, 10) != number {
		return "", "", fmt.Errorf("legacy pull request URL carries noncanonical pull request number %q", number)
	}
	return number, "https://github.com/" + parsedRepository + "/pull/" + number, nil
}

// validateLegacyRemovedPullRequestResponse binds the read-back identity to the
// same repository and number the receipt selector named. State alone cannot do
// that: another repository can report the same candidate, base, and landing
// commits, so both GitHub's URL and its numeric identity are authenticated.
func validateLegacyRemovedPullRequestResponse(view legacyRemovedPullRequestView, repository, number, expectedURL string) error {
	if view.Number <= 0 || strconv.Itoa(view.Number) != number {
		return fmt.Errorf("legacy pull request read returned identity %d, not receipted number %s", view.Number, number)
	}
	responseNumber, responseURL, err := canonicalLegacyRemovedPullRequestURL(view.URL, repository)
	if err != nil {
		return fmt.Errorf("legacy pull request read returned a foreign identity: %w", err)
	}
	if responseNumber != number || responseURL != expectedURL {
		return fmt.Errorf("legacy pull request read returned %s, not receipted %s", responseURL, expectedURL)
	}
	return nil
}

func validateLegacyRemovedPRAcknowledgement(a WorktreeMergeLandedFailureAcknowledgement, r WorktreeMergeReceipt) error {
	if _, err := validateLegacyRemovedPRReceipt(r); err != nil {
		return err
	}
	digest, err := worktreeMergeReceiptSHA256(r.ReceiptPath)
	if err != nil {
		return err
	}
	if a.LegacyValidation != nil || len(a.ObservedSources) != 0 || a.LegacyRemovedPR != r.PullRequest || a.ReceiptSHA256 != digest ||
		a.ClaimBaseSHA != "" || a.CandidateWorktree != r.Candidate.Worktree || a.CandidateBranch != r.Candidate.Branch ||
		a.Actor == "" || a.Reason == "" || a.RecordedAt.IsZero() {
		return errors.New("invalid legacy removed-PR evidence")
	}
	return nil
}

func acknowledgeLegacyRemovedPRFailure(ctx context.Context, o WorktreeMergeLandedFailureAcknowledgementOptions, r WorktreeMergeReceipt, path string, run runner.Runner, read func(string) (WorktreeMergeReceipt, error), persist func(string, WorktreeMergeLandedFailureAcknowledgement) error) (WorktreeMergeLandedFailureAcknowledgement, error) {
	if o.Apply && (strings.TrimSpace(o.Actor) == "" || strings.TrimSpace(o.Reason) == "") {
		return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("--actor and --reason are required with --apply")
	}
	identity, err := validateLegacyRemovedPRReceipt(r)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	lock, err := AcquireOperationLock(o.ProjectsRoot, r.Lane, true)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	defer func() { _ = lock.Release() }()
	// Re-read the immutable tuple under its lane lock, then authenticate the exact
	// same bytes again immediately before writing the append-only acknowledgement.
	locked, err := read(path)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	if !reflect.DeepEqual(r, locked) {
		return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("receipt changed while acquiring legacy recovery lock")
	}
	digest, err := worktreeMergeReceiptSHA256(path)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	canonical, err := worktrees.CanonicalRepositoryPath(o.ProjectsRoot, r.Repository)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	// The same immutable tuple passed this deterministic validation above.
	assets, _ := terminalWorkLogExpectations(r)
	if err := requireLegacyRemovedPRAssetsAbsent(ctx, run, o.ProjectsRoot, canonical, r, assets); err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	number, expectedURL := identity.Number, identity.URL
	output, err := githubRead(ctx, "", "pr", "view", number, "--repo", r.Repository, "--json", "number,url,state,mergedAt,mergeCommit,headRefOid,baseRefName")
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("read exact legacy PR: %w", err)
	}
	var pr legacyRemovedPullRequestView
	if err := json.Unmarshal([]byte(output), &pr); err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	if err := validateLegacyRemovedPullRequestResponse(pr, r.Repository, number, expectedURL); err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	if pr.State != "MERGED" || pr.MergedAt == "" || pr.BaseRefName != r.Target || pr.HeadRefOID != r.Candidate.SHA || pr.MergeCommit.OID != r.LandingSHA {
		return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("legacy PR must prove exact merged head, recorded landing, target, and merge time")
	}
	target, err := fetchExactMergeTargetWithRunner(ctx, run, canonical, r.Target)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	roots := append([]string{r.TargetSHA, r.LandingSHA, r.Candidate.SHA}, sourceSHAs(r.Sources)...)
	for _, root := range roots {
		contains, err := isMergeAncestorWithRunner(ctx, run, canonical, root, target)
		if err != nil {
			return WorktreeMergeLandedFailureAcknowledgement{}, err
		}
		if !contains {
			return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("current target %s does not contain recorded root %s", target, root)
		}
	}
	for _, source := range r.Sources {
		contains, err := isMergeAncestorWithRunner(ctx, run, canonical, source.SHA, r.Candidate.SHA)
		if err != nil {
			return WorktreeMergeLandedFailureAcknowledgement{}, err
		}
		if !contains {
			return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("legacy candidate does not contain exact receipted source")
		}
	}
	contains, err := isMergeAncestorWithRunner(ctx, run, canonical, r.TargetSHA, r.Candidate.SHA)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	if !contains {
		return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("legacy candidate does not contain immutable receipt base")
	}
	if err := requireLegacyRemovedPRAssetsAbsent(ctx, run, o.ProjectsRoot, canonical, r, assets); err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	finalDigest, err := worktreeMergeReceiptSHA256(path)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	if digest != finalDigest {
		return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("historical receipt bytes changed during legacy recovery")
	}
	a := WorktreeMergeLandedFailureAcknowledgement{
		SchemaVersion: worktreeMergeLandedFailureAcknowledgementSchemaVersion, Status: "landed_failure_acknowledged",
		ReceiptPath: path, ReceiptID: r.ID, ReceiptStatus: r.Status, Lane: r.Lane, Repository: r.Repository, Target: r.Target,
		AcknowledgementPath: landedFailureAcknowledgementPath(path), ReceiptTargetSHA: r.TargetSHA, ReceiptLandingSHA: r.LandingSHA,
		CurrentTargetSHA: target, CandidateSHA: r.Candidate.SHA, CandidateWorktree: r.Candidate.Worktree, CandidateBranch: r.Candidate.Branch,
		Sources: append([]WorktreeMergeSource(nil), r.Sources...), LegacyRemovedPR: r.PullRequest, ReceiptSHA256: digest,
		Actor: strings.TrimSpace(o.Actor), Reason: strings.TrimSpace(o.Reason), RecordedAt: time.Now().UTC(),
	}
	a.ID = landedFailureAcknowledgementID(a)
	return finishLandedFailureAcknowledgement(r, a, o.Apply, persist)
}

type legacyRemovedPullRequestView struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	State       string `json:"state"`
	MergedAt    string `json:"mergedAt"`
	HeadRefOID  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
	MergeCommit struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
}

func requireLegacyRemovedPRAssetsAbsent(ctx context.Context, run runner.Runner, projectsRoot, canonical string, r WorktreeMergeReceipt, assets []worktrees.TerminalWorkLogExpectation) error {
	registered, _, err := runCommand(ctx, run, 0, 0, canonical, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	for _, asset := range assets {
		if !filepath.IsAbs(asset.Worktree) || filepath.Clean(asset.Worktree) == filepath.Clean(canonical) {
			return errors.New("legacy checkout identity is unsafe")
		}
		if _, err := os.Lstat(asset.Worktree); !os.IsNotExist(err) {
			return fmt.Errorf("legacy checkout %s must be absent: %v", asset.Worktree, err)
		}
		for _, line := range strings.Split(registered, "\n") {
			if strings.HasPrefix(line, "worktree ") && filepath.Clean(strings.TrimPrefix(line, "worktree ")) == filepath.Clean(asset.Worktree) {
				return fmt.Errorf("legacy checkout %s remains registered", asset.Worktree)
			}
		}
	}
	return requireTerminalCleanupBranchesAbsentWithRunner(ctx, run, projectsRoot, r, assets, 0, 0)
}
