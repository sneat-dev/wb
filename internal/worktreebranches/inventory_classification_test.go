package worktreebranches

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktreelanding"
)

func TestInventoryClassificationReceiptsAndSupersession(t *testing.T) {
	ctx := context.Background()
	sha := strings.Repeat("a", 40)
	target := strings.Repeat("b", 40)
	landing := strings.Repeat("c", 40)
	repo := Repository{Slug: "org/repo", Path: "/repo"}
	ref := BranchRef{Name: "feature", SHA: sha}
	sweep := InventorySweep{Base: "main"}
	var ancestorErr, contentErr, attestedErr, queryErr, digestErr error
	ancestor := false
	landingAncestor := true
	content := true
	contentTarget := true
	var contentTargetErr error
	targetTreeErr := false
	queryPR := true
	attested := false
	rejection := "not an attested receipt"
	supersession := false
	cherry := "+ " + sha
	treesEqual := false
	treeErr := false
	pr := &PullRequest{Number: 7, MergeSHA: landing}
	service := InventoryService{Ports: InventoryPorts{
		Git: func(_ context.Context, _ string, args ...string) (string, error) {
			if args[0] == "cherry" {
				return cherry, nil
			}
			return "", nil
		},
		IsAncestor: func(_ context.Context, _ string, source, _ string) (bool, error) {
			if source == landing {
				return landingAncestor, ancestorErr
			}
			return ancestor, ancestorErr
		},
		ContentContained: func(_ context.Context, _ string, _, destination string) (bool, error) {
			if destination == target {
				return contentTarget, contentTargetErr
			}
			return content, contentErr
		},
		CommitTree: func(_ context.Context, _ string, commit string) (string, error) {
			if treeErr || (targetTreeErr && commit == target) {
				return "", errors.New("tree unavailable")
			}
			if treesEqual {
				return "same", nil
			}
			return commit, nil
		},
		Supersession: func(context.Context, string, Repository, BranchRef, string, string) (*SupersessionEvidence, string) {
			if supersession {
				return &SupersessionEvidence{Reviewer: "reviewer", ReceiptID: "id"}, ""
			}
			return nil, "mismatch"
		},
		SupersessionDigest: func(string) (string, error) { return "digest", digestErr },
		AttestedReceipt: func(context.Context, Repository, BranchRef, string, string, string) (*worktreelanding.VerifiedCandidate, string, error) {
			if attested {
				return &worktreelanding.VerifiedCandidate{LandingSHA: landing, PullRequest: pr}, "", nil
			}
			return nil, rejection, attestedErr
		},
		PullRequestsForHead: func(context.Context, Repository, string) ([]GitHubPullRequest, error) {
			if queryPR {
				return []GitHubPullRequest{{Number: 7}}, queryErr
			}
			return nil, queryErr
		},
		AbsorbingPR: func([]GitHubPullRequest, string) *PullRequest {
			if queryPR {
				return pr
			}
			return nil
		},
	}}
	classify := func() BranchEntry {
		return service.ClassifyBranch(ctx, repo, sweep, ref, BranchScopeRemote, target, "main", nil, nil, map[string][]GitHubPullRequest{})
	}
	sweep.SupersededBy = "receipt.json"
	supersession = true
	if got := classify(); got.Disposition != BranchSuperseded || !got.SupersededAtOrigin || got.SupersessionSHA256 != "digest" {
		t.Fatalf("superseded: %#v", got)
	}
	digestErr = errors.New("digest failed")
	if got := classify(); got.Disposition != BranchUnreadable || !strings.Contains(got.Evidence, "digest") {
		t.Fatalf("digest: %#v", got)
	}
	digestErr = nil
	supersession = false
	ancestorErr = errors.New("ancestor failed")
	if got := classify(); got.Disposition != BranchUnreadable || got.SupersessionRejection != "mismatch" {
		t.Fatalf("supersession rejection: %#v", got)
	}
	ancestorErr = nil
	sweep.SupersededBy = ""
	sweep.AbsorbedBy = "7"
	attestedErr = errors.New("attestation failed")
	if got := classify(); got.Disposition != BranchUnreadable || !strings.Contains(got.Evidence, "attestation failed") {
		t.Fatalf("attestation error: %#v", got)
	}
	attestedErr = nil
	attested = true
	if got := classify(); got.Disposition != BranchReceipted || got.LandingSHA != landing {
		t.Fatalf("attested: %#v", got)
	}
	attested = false
	sweep.AbsorbedBy = ""
	sweep.Receipts = true
	ancestor = true
	if got := classify(); got.Disposition != BranchContained {
		t.Fatalf("containment precedence: %#v", got)
	}
	ancestor = false
	if got := classify(); got.Disposition != BranchReceipted {
		t.Fatalf("PR receipt: %#v", got)
	}
	queryErr = errors.New("github unavailable")
	if got, note := service.ClassifyLandingReceipt(ctx, repo, ref, "main", target, nil); got != nil || !strings.Contains(note, "query failed") {
		t.Fatalf("query failure: %#v %q", got, note)
	}
	queryErr = nil
	queryPR = false
	if got, note := service.ClassifyLandingReceipt(ctx, repo, ref, "main", target, nil); got != nil || !strings.Contains(note, "no merged") {
		t.Fatalf("no receipt: %#v %q", got, note)
	}
	queryPR = true
	pr.MergeSHA = "bad"
	if got, note := service.ClassifyLandingReceipt(ctx, repo, ref, "main", target, nil); got != nil || !strings.Contains(note, "no valid merge commit") {
		t.Fatalf("invalid merge: %#v %q", got, note)
	}
	pr.MergeSHA = landing
	ancestorErr = errors.New("check failed")
	if got, note := service.ClassifyLandingReceipt(ctx, repo, ref, "main", target, nil); got != nil || !strings.Contains(note, "containment check failed") {
		t.Fatalf("ancestor error: %#v %q", got, note)
	}
	ancestorErr = nil
	landingAncestor = false
	if got, note := service.ClassifyLandingReceipt(ctx, repo, ref, "main", target, nil); got != nil || !strings.Contains(note, "not contained") {
		t.Fatalf("not landed: %#v %q", got, note)
	}
	landingAncestor = true
	contentErr = errors.New("content failed")
	if got, note := service.ClassifyLandingReceipt(ctx, repo, ref, "main", target, nil); got != nil || !strings.Contains(note, "three-way proof failed") {
		t.Fatalf("content error: %#v %q", got, note)
	}
	contentErr = nil
	content = false
	if got, note := service.ClassifyLandingReceipt(ctx, repo, ref, "main", target, nil); got != nil || !strings.Contains(note, "does not carry") {
		t.Fatalf("missing content: %#v %q", got, note)
	}
	content = true
	contentTargetErr = errors.New("target proof failed")
	if got, note := service.ClassifyLandingReceipt(ctx, repo, ref, "main", target, nil); got != nil || !strings.Contains(note, "three-way proof failed") {
		t.Fatalf("target proof error: %#v %q", got, note)
	}
	contentTargetErr = nil
	contentTarget = false
	if got, note := service.ClassifyLandingReceipt(ctx, repo, ref, "main", target, nil); got != nil || !strings.Contains(note, "target has since diverged") {
		t.Fatalf("target diverged: %#v %q", got, note)
	}
	contentTarget = true
	if got, note := service.ClassifyLandingReceipt(ctx, repo, ref, "main", target, nil); got == nil || note != "" {
		t.Fatalf("receipt: %#v %q", got, note)
	}
	sweep.Receipts = true
	queryPR = false
	if got := classify(); got.Disposition != BranchUnique || !strings.Contains(got.Evidence, "no merged pull request") {
		t.Fatalf("receipt absent: %#v", got)
	}
	queryPR = true
	sweep.Receipts = false
	sweep.AbsorbedBy = "7"
	attested = false
	if got := classify(); got.Disposition != BranchUnique || got.AbsorbedByRejection != rejection {
		t.Fatalf("rejected attestation: %#v", got)
	}
	sweep.AbsorbedBy = ""
	sweep.Receipts = false
	treesEqual = true
	if got := classify(); got.Disposition != BranchAbsorbed {
		t.Fatalf("equal trees: %#v", got)
	}
	treesEqual = false
	treeErr = true
	if got := classify(); got.Disposition != BranchUnique {
		t.Fatalf("tree unavailable: %#v", got)
	}
	treeErr = false
	targetTreeErr = true
	if got := classify(); got.Disposition != BranchUnique {
		t.Fatalf("target tree unavailable: %#v", got)
	}
}
