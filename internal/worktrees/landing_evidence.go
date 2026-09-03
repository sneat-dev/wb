package worktrees

import (
	"context"
	"fmt"
	"strings"
)

// DefaultResidueDepth bounds how many commits back from HEAD the
// commit-to-pull-request index is consulted. The measured sweep found
// ahead-counts of 1, 2, 4 and 5 on landed branches, so ten is generous; a
// branch further past its landing is genuinely unlanded work, not residue, and
// must keep refusing rather than cost ten more API reads to say so.
const DefaultResidueDepth = 10

// ResidualCommit is one local commit stacked on a landed head.
type ResidualCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// LandingEvidence proves by commit identity that a branch's work reached the
// target even though the branch's own head did not.
//
// It exists because a squash merge leaves no ancestry: the landed content is a
// new commit, so `git` reports the source branch as unmerged forever. Add one
// ordinary post-merge commit — a `git merge origin/main`, a review fixup that
// was squashed differently — and even GitHub's commit-to-pull-request index for
// the head returns nothing, because that head was never pushed. The measured
// sweep hit this on 7 of 11 refusals, every one of them a demonstrably merged
// branch, and reported all of them as a bare "awaiting push". The operator
// needs to see the opposite: the work landed, and here are the commits that did
// not.
type LandingEvidence struct {
	// LandedSHA is the newest commit of this branch with a verified landing.
	LandedSHA string `json:"landed_sha"`
	// LandingSHA is the commit in the target that carried the work there.
	LandingSHA string `json:"landing_sha"`
	// PullRequest is the merged pull request GitHub's own commit index named.
	PullRequest *PullRequest `json:"pull_request,omitempty"`
	// Residue is every commit this checkout holds past LandedSHA, newest first.
	// Deleting the branch discards exactly these commits, which is why
	// `--allow-residue` prints them before it widens past them.
	Residue []ResidualCommit `json:"residue,omitempty"`
	// Truncated records that the walk hit its depth bound before finding a
	// landing, so absence of evidence here is not evidence of absence.
	Truncated bool `json:"truncated,omitempty"`
}

// landingEvidence looks for a landed ancestor of head. It is only consulted
// once the cheaper proofs — containment, a rebase-merge receipt, an absorbed
// landing receipt for the head itself — have all said no.
//
// The walk only ever examines commits in target..head: a commit already in the
// target proves nothing about this branch, and one outside it is a candidate
// whose landing must be proved by exactly the same receipt an ordinary absorbed
// landing needs. Nothing here weakens that proof; it only asks the question
// about more than one commit.
func landingEvidence(
	ctx context.Context,
	worktree, repository, slug, head, base, target string,
	depth int,
) (*LandingEvidence, error) {
	if depth == 0 {
		depth = DefaultResidueDepth
	}
	if depth < 0 || target == "" || head == "" {
		return nil, nil
	}
	candidates, truncated, err := commitsNotIn(ctx, repository, target, head, depth)
	if err != nil || len(candidates) == 0 {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate == head {
			// The caller already proved the head has no landing receipt of its
			// own; asking GitHub again for the same commit would be exactly the
			// duplicated check verbs are required not to make.
			continue
		}
		pullRequests, err := githubPullRequests(ctx, worktree, slug, candidate)
		if err != nil {
			return nil, err
		}
		receipt, _, err := absorbedLandingReceipt(ctx, worktree, repository, slug, candidate, base, target, "", pullRequests)
		if err != nil {
			return nil, err
		}
		if receipt == nil {
			continue
		}
		residue, err := residualCommits(ctx, repository, target, head, candidate, depth)
		if err != nil {
			return nil, err
		}
		return &LandingEvidence{
			LandedSHA:   candidate,
			LandingSHA:  receipt.LandingSHA,
			PullRequest: receipt.PullRequest,
			Residue:     residue,
		}, nil
	}
	if truncated {
		return &LandingEvidence{Truncated: true}, nil
	}
	return nil, nil
}

// commitsNotIn lists the commits reachable from head but not from target,
// newest first, and reports whether the bound cut the list short.
func commitsNotIn(ctx context.Context, repository, target, head string, limit int) ([]string, bool, error) {
	output, err := git(ctx, repository, "rev-list", fmt.Sprintf("--max-count=%d", limit+1), head, "--not", target)
	if err != nil {
		return nil, false, fmt.Errorf("list commits of %s not in %s: %w", head, target, err)
	}
	commits := splitNonEmptyLines(output)
	if len(commits) > limit {
		return commits[:limit], true, nil
	}
	return commits, false, nil
}

// residualCommits lists what a checkout holds past its landed commit.
func residualCommits(ctx context.Context, repository, target, head, landed string, limit int) ([]ResidualCommit, error) {
	output, err := git(ctx, repository, "log",
		fmt.Sprintf("--max-count=%d", limit), "--format=%H %s",
		head, "--not", target, landed)
	if err != nil {
		return nil, fmt.Errorf("list residual commits of %s past %s: %w", head, landed, err)
	}
	lines := splitNonEmptyLines(output)
	residue := make([]ResidualCommit, 0, len(lines))
	for _, line := range lines {
		sha, subject, _ := strings.Cut(line, " ")
		residue = append(residue, ResidualCommit{SHA: sha, Subject: strings.TrimSpace(subject)})
	}
	return residue, nil
}

func splitNonEmptyLines(value string) []string {
	lines := make([]string, 0, 8)
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// detachedRefusal names what a detached checkout is missing. Every
// pull-request review creates one, so the refusal has to say whether the head
// is on origin at all: a review checkout of a landed pull request is ordinary
// and removable, while a detached head GitHub has never seen is the only thing
// standing between someone's work and its deletion.
func detachedRefusal(result ListResult) string {
	if result.HeadUnknownToRemote {
		return "detached HEAD " + shortSHA(result.HeadSHA) +
			" was never pushed: GitHub's commit index has never seen it, so nothing can prove what it holds"
	}
	return "detached HEAD " + shortSHA(result.HeadSHA) +
		" is on origin but no merged pull request into " + result.Base + " is associated with it"
}

// landedWithResidue reports the one shape `--allow-residue` widens past: the
// work landed, proved by the same receipt any other candidate needs, and the
// checkout holds commits past the landed head.
func (result ListResult) landedWithResidue() bool {
	return result.Landing != nil && result.Landing.LandedSHA != "" && len(result.Landing.Residue) > 0
}

// residueReason is the refusal an operator can act on. "Awaiting push" was the
// dominant false negative in the measured sweep — 7 of 11 refusals, every one
// on a demonstrably merged branch — because it named the symptom and hid both
// the landing and the commits that did not land.
func (result ListResult) residueReason() string {
	if result.Landing == nil {
		return ""
	}
	landing := "landed + residue: " + shortSHA(result.Landing.LandedSHA) + " landed at " + shortSHA(result.Landing.LandingSHA)
	if result.Landing.PullRequest != nil {
		landing += " via " + result.Landing.PullRequest.URL
	}
	return landing + "; " + pluralCommits(len(result.Landing.Residue)) + " not in the target: " +
		result.Landing.ResidueSummary() + "; rerun with --allow-residue to retire it and discard them"
}

func pluralCommits(count int) string {
	if count == 1 {
		return "1 residual commit"
	}
	return fmt.Sprintf("%d residual commits", count)
}

// ResidueSummary renders the residual commits for a refusal or a receipt.
func (evidence *LandingEvidence) ResidueSummary() string {
	if evidence == nil || len(evidence.Residue) == 0 {
		return ""
	}
	parts := make([]string, 0, len(evidence.Residue))
	for _, commit := range evidence.Residue {
		parts = append(parts, shortSHA(commit.SHA)+" "+commit.Subject)
	}
	return strings.Join(parts, "; ")
}
