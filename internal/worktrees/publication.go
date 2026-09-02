package worktrees

import (
	"context"
	"fmt"
)

// Publication statuses reuse the canonical-freshness vocabulary deliberately:
// "is my HEAD at origin/<branch>?" and "is this clone at origin/<base>?" are
// the same comparison against a different ref, and one vocabulary means an
// agent parses one set of strings.
const (
	// PublicationPublished — HEAD is exactly origin/<branch>. The push landed.
	PublicationPublished = CanonicalFreshnessCurrent
	// PublicationUnpublished — HEAD carries commits origin/<branch> does not.
	// This is the state `git push` reporting "Everything up-to-date" leaves
	// behind when it pushed a ref other than the one HEAD is on.
	PublicationUnpublished = CanonicalFreshnessAhead
	// PublicationBehind — origin/<branch> carries commits HEAD does not.
	PublicationBehind = CanonicalFreshnessStale
	// PublicationDiverged — both sides carry commits the other does not.
	PublicationDiverged = CanonicalFreshnessDiverged
	// PublicationUnborn — the branch has never been pushed at all.
	PublicationUnborn = CanonicalFreshnessMissing
)

// inspectPublication answers the question no Git hook can answer: after a
// push, is the commit I am sitting on actually on the remote?
//
// Git runs no post-push hook, and it runs pre-push only when it has refs to
// update. So the most dangerous push is the one that does nothing: on
// 2026-09-02 a reviewer's checkout inside an author's worktree left HEAD
// pointing at a commit no branch reached, `git push` printed "Everything up to
// date" because the branch ref it pushed genuinely had not moved, and the work
// was orphaned with a success message on screen.
//
// The check is deliberately the same fetch-and-compare `wb worktree guard`
// already performs for a canonical clone, aimed at the worktree's own branch
// instead of the base branch. Nothing is fast-forwarded, merged, or otherwise
// changed: it fetches the one ref, counts both sides, and reports.
func inspectPublication(ctx context.Context, root, branch string) *CanonicalFreshness {
	return inspectCanonicalFreshnessWith(ctx, root, branch, git)
}

// PublicationFinding renders the one-line diagnosis and the exact remedy for a
// checkout whose HEAD is not published, or "" when nothing is wrong.
//
// The remedy matters as much as the diagnosis. The failure this exists to
// catch already printed a success message once; an operator who is told only
// "unpublished" has been given the same non-answer a second time.
func PublicationFinding(publication *CanonicalFreshness, branch string) string {
	if publication == nil {
		return ""
	}
	switch publication.Status {
	case PublicationPublished:
		return ""
	case PublicationUnpublished:
		return fmt.Sprintf(
			"HEAD %s is %d commit(s) ahead of %s: this work is NOT on the remote. Run `git push origin HEAD:%s`, then verify again — a `git push` that printed \"Everything up-to-date\" pushed a ref other than the one HEAD is on.",
			publicationSHA(publication.LocalSHA), publication.Ahead, publication.RemoteRef, branch)
	case PublicationUnborn:
		return fmt.Sprintf(
			"%s does not exist on the remote: nothing about this branch has ever been published. Run `git push -u origin %s`, then verify again.",
			publication.RemoteRef, branch)
	case PublicationBehind:
		return fmt.Sprintf(
			"%s is %d commit(s) ahead of HEAD: the remote branch carries work this checkout does not have. Reconcile before pushing over it.",
			publication.RemoteRef, publication.Behind)
	case PublicationDiverged:
		return fmt.Sprintf(
			"HEAD and %s have diverged (%d local, %d remote): neither side contains the other. Reconcile before pushing over it.",
			publication.RemoteRef, publication.Ahead, publication.Behind)
	default:
		// Offline, fetch failure, or a ref that moved mid-check. WB cannot
		// prove the work is published, and "cannot prove" must never render
		// as "published".
		reason := publication.Error
		if reason == "" {
			reason = "no reason was recorded"
		}
		return fmt.Sprintf(
			"publication of HEAD against %s could not be verified (%s): %s",
			publication.RemoteRef, publication.Status, reason)
	}
}

// PublicationVerified reports whether HEAD is provably on the remote. Anything
// WB could not observe is unverified, never assumed published.
func PublicationVerified(publication *CanonicalFreshness) bool {
	return publication != nil && publication.Status == PublicationPublished
}

// publicationSHA is shortSHA that names an absent value rather than rendering
// it as an empty string in the middle of a sentence.
func publicationSHA(sha string) string {
	if sha == "" {
		return "(unknown)"
	}
	return shortSHA(sha)
}
