package orchestrate

import (
	"context"
	"crypto/sha1"
	"fmt"
	"strings"
	"testing"
)

func TestRetiredPrepareWitnessRequiresExactOriginalInputs(t *testing.T) {
	t.Parallel()
	id := func(s string) string { return strings.Repeat(s, 40) }
	sources := []WorktreeMergeSource{{SHA: id("a"), Branch: "alpha", Merged: true}, {SHA: id("b"), Branch: "beta", Merged: true}}
	r := WorktreeMergeReceipt{Phase: WorktreeMergePhasePrepare, Status: WorktreeMergePrepared, Sources: sources, TargetSHA: id("d")}
	raw := func(tree, a, b string) string {
		return fmt.Sprintf("tree %s\nparent %s\nparent %s\nauthor A <a@example.test> 1 +0000\ncommitter B <b@example.test> 1 +0000\n\nmessage\n", tree, a, b)
	}
	signed := func(body string, r *WorktreeMergeReceipt) {
		r.Candidate.SHA = fmt.Sprintf("%x", sha1.Sum([]byte(fmt.Sprintf("commit %d%c%s", len(body), 0, body))))
	}
	for _, x := range []struct {
		tree, a, b string
		valid      bool
	}{{id("c"), id("a"), id("b"), true}, {id("c"), id("b"), id("a"), false}, {"bad", id("a"), id("b"), false}} {
		body := raw(x.tree, x.a, x.b)
		signed(body, &r)
		got, err := originalPrepareTree([]byte(body), r)
		if (err == nil) != x.valid || (x.valid && got != x.tree) {
			t.Fatalf("ordered merge body: %q %v", got, err)
		}
	}
	body := raw(id("c"), id("a"), id("b"))
	signed(body, &r)
	r.Validation.Status = "passed"
	r.Validation.WorkspaceClean = true
	r.Validation.Revision = r.Candidate.SHA
	r.ValidationIdentity = &WorktreeMergeValidationIdentity{CandidateSHA: r.Candidate.SHA, TargetSHA: r.TargetSHA, SourceSHAs: sourceSHAs(r.Sources)}
	digest := strings.Repeat("b", 64)
	proof := RetiredPrepareTreeProof{Candidate: r.Candidate.SHA, Tree: id("c"), Landing: id("e"), ReceiptSHA256: digest, CleanupDigests: []string{digest, digest, digest}, SourcePRs: []int{1, 2}, SourceHeads: sourceSHAs(r.Sources), SourceLandings: []string{id("c"), id("d")}}
	if !proof.matches(r) {
		t.Fatal("original proof rejected")
	}
	for _, modify := range []func(*WorktreeMergeReceipt){func(r *WorktreeMergeReceipt) { r.PullRequest = "2" }, func(r *WorktreeMergeReceipt) { r.Sources = r.Sources[:1] }, func(r *WorktreeMergeReceipt) { r.TargetSHA = "-bad" }, func(r *WorktreeMergeReceipt) { r.ValidationIdentity = nil }} {
		x := r
		modify(&x)
		if proof.matches(x) {
			t.Fatal("out-of-scope receipt accepted")
		}
	}
	for _, modify := range []func(*RetiredPrepareTreeProof){func(p *RetiredPrepareTreeProof) { p.ReceiptSHA256 = "zz" }, func(p *RetiredPrepareTreeProof) { p.SourceLandings = []string{id("e"), "not-a-commit"} }, func(p *RetiredPrepareTreeProof) { p.SourcePRs = []int{1, 1} }} {
		x := proof
		modify(&x)
		if x.matches(r) {
			t.Fatal("invalid recorded terminal proof accepted")
		}
	}
	for _, receipts := range [][]string{nil, {"a", "b"}} {
		_, err := SweepTargetAbsorbedWorktreeMergeReceipts(context.Background(), WorktreeMergeTargetAbsorbedSweepOptions{ProjectsRoot: t.TempDir(), CandidateCommitFile: "witness", Receipts: receipts})
		if err == nil {
			t.Fatal("witness must identify one receipt")
		}
	}
}
