package orchestrate

import (
	"context"
	"strings"
	"testing"
)

// #586: when auto-merge is armed, WB never disarms it, so the review-stale
// refusal must say so explicitly rather than silently leaving the operator
// to guess whether the pull request could still merge without them.
func TestReviewStaleRefusalNamesArmedAutoMergeState(t *testing.T) {
	restoreProof := reviewHeadAdvanceProof
	restoreParents := reviewCommitParents
	restoreCheckout := resolveReviewCheckout
	t.Cleanup(func() {
		reviewHeadAdvanceProof = restoreProof
		reviewCommitParents = restoreParents
		resolveReviewCheckout = restoreCheckout
	})
	// A checkout is available (round 3, minor 5's seam), so the walk
	// actually runs the fakes below rather than short-circuiting on "no
	// checkout" — that path is unverifiable, not stale, and is covered by
	// TestReviewStaleRefusalUnverifiableWithNoCheckoutDoesNotRefuse.
	resolveReviewCheckout = func(ctx context.Context, options PullRequestLandOptions, view PullRequestView) (string, string, bool) {
		return "wt", "feature", true
	}
	// Any head shape that is not provably a WB update-branch advance.
	reviewCommitParents = func(ctx context.Context, repository, sha string) ([]string, error) {
		return []string{"unrelated-parent"}, nil
	}

	options := PullRequestLandOptions{Repository: "acme/app"}
	view := PullRequestView{}
	view.Base.Ref = "main"
	view.Head.Ref = "feature"

	armed, armedNote := reviewStaleRefusal(context.Background(), options, view, "reviewed-head", "current-head", true, "7")
	if armed == nil {
		t.Fatal("a head that is not the reviewed one must refuse")
	}
	if armedNote != "" {
		t.Fatalf("a genuine refusal must carry no unverifiable note: %q", armedNote)
	}
	if armed.code != LandRefusalReviewStale {
		t.Fatalf("code = %q, want %q", armed.code, LandRefusalReviewStale)
	}
	if !strings.Contains(armed.reason, "auto-merge remains armed") {
		t.Fatalf("armed refusal must name the armed state: %q", armed.reason)
	}

	notArmed, notArmedNote := reviewStaleRefusal(context.Background(), options, view, "reviewed-head", "current-head", false, "7")
	if notArmed == nil {
		t.Fatal("expected a refusal")
	}
	if notArmedNote != "" {
		t.Fatalf("a genuine refusal must carry no unverifiable note: %q", notArmedNote)
	}
	if strings.Contains(notArmed.reason, "auto-merge remains armed") {
		t.Fatalf("an unarmed refusal must not claim auto-merge is armed: %q", notArmed.reason)
	}
}

// Round 3, minor 5: with no local checkout anywhere (no branch-specific
// worktree, no canonical clone) the walk cannot run at all. That must never
// be reported as review-stale — false-refusing here is exactly the bug
// that made landing a second, unrelated pull request after WB's own
// update-branch on a different branch fail outright. It is an
// unverifiable note instead, so the caller records a finding and lands.
func TestReviewStaleRefusalUnverifiableWithNoCheckoutDoesNotRefuse(t *testing.T) {
	restoreCheckout := resolveReviewCheckout
	t.Cleanup(func() { resolveReviewCheckout = restoreCheckout })
	resolveReviewCheckout = func(ctx context.Context, options PullRequestLandOptions, view PullRequestView) (string, string, bool) {
		return "", "", false
	}

	options := PullRequestLandOptions{Repository: "acme/app", ProjectsRoot: "/does/not/exist"}
	view := PullRequestView{}
	view.Base.Ref = "main"
	view.Head.Ref = "feature"

	refusal, note := reviewStaleRefusal(context.Background(), options, view, "reviewed-head", "current-head", true, "7")
	if refusal != nil {
		t.Fatalf("an unverifiable binding must never be refused: %#v", refusal)
	}
	if note == "" {
		t.Fatal("an unverifiable binding must be reported as a note the caller records as a finding")
	}
}

// Round 3, B3 (third bullet): when GitHub's armed auto-merge lands a head
// this review does not provably cover, the receipt must record
// review_bound: false and an informational finding — never a false
// review_bound: true, and never a refusal (the merge already happened and
// cannot be undone).
func TestRecordMergedByGitHubReviewBindingDowngradesAnUnprovenForeignHead(t *testing.T) {
	restoreProof := reviewHeadAdvanceProof
	restoreParents := reviewCommitParents
	restoreCheckout := resolveReviewCheckout
	t.Cleanup(func() {
		reviewHeadAdvanceProof = restoreProof
		reviewCommitParents = restoreParents
		resolveReviewCheckout = restoreCheckout
	})
	resolveReviewCheckout = func(ctx context.Context, options PullRequestLandOptions, view PullRequestView) (string, string, bool) {
		return "wt", "feature", true
	}
	// A foreign, non-merge commit: not provably descended from the reviewed
	// head through any WB/GitHub update-branch merge.
	reviewCommitParents = func(ctx context.Context, repository, sha string) ([]string, error) {
		return []string{"unrelated-parent"}, nil
	}

	options := PullRequestLandOptions{Repository: "acme/app"}
	view := PullRequestView{}
	view.Base.Ref = "main"
	view.Head.Ref = "feature"
	view.Head.SHA = "foreign-merged-head"
	result := &PullRequestLandResult{Evidence: map[string]string{}}

	recordMergedByGitHubReviewBinding(context.Background(), options, view, "reviewed-head", result)

	if result.ReviewBound == nil || *result.ReviewBound {
		t.Fatalf("ReviewBound = %v, want a pointer to false", result.ReviewBound)
	}
	if !strings.Contains(result.Evidence["review"], "review-unbound") {
		t.Fatalf("evidence[review] = %q, want the review-unbound finding", result.Evidence["review"])
	}
}

// The provably-covered case must leave the receipt untouched: the caller
// already recorded review_bound: true when it originally bound the review,
// and this must not overwrite that with a spurious finding.
func TestRecordMergedByGitHubReviewBindingLeavesAProvenHeadAlone(t *testing.T) {
	options := PullRequestLandOptions{Repository: "acme/app"}
	view := PullRequestView{}
	view.Base.Ref = "main"
	view.Head.Ref = "feature"
	view.Head.SHA = "reviewed-head"
	result := &PullRequestLandResult{Evidence: map[string]string{}, ReviewBound: boolPtr(true)}

	recordMergedByGitHubReviewBinding(context.Background(), options, view, "reviewed-head", result)

	if result.ReviewBound == nil || !*result.ReviewBound {
		t.Fatalf("ReviewBound = %v, want unchanged true", result.ReviewBound)
	}
	if result.Evidence["review"] != "" {
		t.Fatalf("evidence[review] = %q, want no finding for a head that equals the reviewed one", result.Evidence["review"])
	}
}

// The identical-head case is the ordinary one and must need no git call at
// all — locateBranchCheckout is never reached.
func TestReviewStaleRefusalNilWhenHeadUnchanged(t *testing.T) {
	options := PullRequestLandOptions{Repository: "acme/app", ProjectsRoot: "/does/not/exist"}
	view := PullRequestView{}
	view.Base.Ref = "main"
	view.Head.Ref = "feature"
	if refusal, note := reviewStaleRefusal(context.Background(), options, view, "same", "same", false, "7"); refusal != nil || note != "" {
		t.Fatalf("unchanged head must not refuse or note: refusal=%#v note=%q", refusal, note)
	}
	if refusal, note := reviewStaleRefusal(context.Background(), options, view, "", "current", false, "7"); refusal != nil || note != "" {
		t.Fatalf("an unrecorded reviewed head must not refuse or note: refusal=%#v note=%q", refusal, note)
	}
}
