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
	t.Cleanup(func() {
		reviewHeadAdvanceProof = restoreProof
		reviewCommitParents = restoreParents
	})
	// Any head shape that is not provably a WB update-branch advance.
	reviewCommitParents = func(ctx context.Context, repository, sha string) ([]string, error) {
		return []string{"unrelated-parent"}, nil
	}

	options := PullRequestLandOptions{Repository: "acme/app"}
	view := PullRequestView{}
	view.Base.Ref = "main"
	view.Head.Ref = "feature"

	armed := reviewStaleRefusal(context.Background(), options, view, "reviewed-head", "current-head", true, "7")
	if armed == nil {
		t.Fatal("a head that is not the reviewed one must refuse")
	}
	if armed.code != LandRefusalReviewStale {
		t.Fatalf("code = %q, want %q", armed.code, LandRefusalReviewStale)
	}
	if !strings.Contains(armed.reason, "auto-merge remains armed") {
		t.Fatalf("armed refusal must name the armed state: %q", armed.reason)
	}

	notArmed := reviewStaleRefusal(context.Background(), options, view, "reviewed-head", "current-head", false, "7")
	if notArmed == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(notArmed.reason, "auto-merge remains armed") {
		t.Fatalf("an unarmed refusal must not claim auto-merge is armed: %q", notArmed.reason)
	}
}

// The identical-head case is the ordinary one and must need no git call at
// all — locateBranchCheckout is never reached.
func TestReviewStaleRefusalNilWhenHeadUnchanged(t *testing.T) {
	options := PullRequestLandOptions{Repository: "acme/app", ProjectsRoot: "/does/not/exist"}
	view := PullRequestView{}
	view.Base.Ref = "main"
	view.Head.Ref = "feature"
	if refusal := reviewStaleRefusal(context.Background(), options, view, "same", "same", false, "7"); refusal != nil {
		t.Fatalf("unchanged head must not refuse: %#v", refusal)
	}
	if refusal := reviewStaleRefusal(context.Background(), options, view, "", "current", false, "7"); refusal != nil {
		t.Fatalf("an unrecorded reviewed head must not refuse: %#v", refusal)
	}
}
