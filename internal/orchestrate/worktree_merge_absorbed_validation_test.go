package orchestrate

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAbsorbedConflictReceiptEligibilityPreservesImmutableIdentity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		mutate     func(*WorktreeMergeReceipt)
		diagnostic string
	}{
		{"valid empty candidate SHA", nil, ""},
		{"valid candidate SHA", func(r *WorktreeMergeReceipt) { r.Candidate.SHA = "candidate-head" }, ""},
		{"valid multiple sources", func(r *WorktreeMergeReceipt) { r.Sources = append(r.Sources, r.Sources[0]) }, ""},
		{"different receipt path", func(r *WorktreeMergeReceipt) { r.ReceiptPath += ".other" }, "has inconsistent immutable receipt identity"},
		{"missing ID", func(r *WorktreeMergeReceipt) { r.ID = "" }, "has inconsistent immutable receipt identity"},
		{"missing lane", func(r *WorktreeMergeReceipt) { r.Lane = "" }, "has inconsistent immutable receipt identity"},
		{"wrong lane", func(r *WorktreeMergeReceipt) { r.Lane = "other" }, "has inconsistent immutable receipt identity"},
		{"wrong phase", func(r *WorktreeMergeReceipt) { r.Phase = WorktreeMergePhaseLand }, "want an unpublished prepare conflict"},
		{"wrong status", func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }, "want an unpublished prepare conflict"},
		{"recorded landing", func(r *WorktreeMergeReceipt) { r.LandingSHA = "landing" }, "already recorded a landing SHA landing; use acknowledge-landed-failed instead"},
		{"published URL", func(r *WorktreeMergeReceipt) { r.PullRequest = "https://example.test/pull/1" }, "already published a candidate; use acknowledge-stranded-landing instead"},
		{"published SHA", func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA = "published" }, "already published a candidate; use acknowledge-stranded-landing instead"},
		{"missing repository", func(r *WorktreeMergeReceipt) { r.Repository = ""; r.Lane = worktreeMergeLaneID(r.Repository, r.Target) }, "lacks complete immutable repository or target identity"},
		{"missing target", func(r *WorktreeMergeReceipt) { r.Target = ""; r.Lane = worktreeMergeLaneID(r.Repository, r.Target) }, "lacks complete immutable repository or target identity"},
		{"missing target SHA", func(r *WorktreeMergeReceipt) { r.TargetSHA = "" }, "lacks complete immutable repository or target identity"},
		{"missing candidate task", func(r *WorktreeMergeReceipt) { r.Candidate.Task = "" }, "lacks complete immutable candidate identity"},
		{"missing candidate path", func(r *WorktreeMergeReceipt) { r.Candidate.Worktree = "" }, "lacks complete immutable candidate identity"},
		{"missing candidate branch", func(r *WorktreeMergeReceipt) { r.Candidate.Branch = "" }, "lacks complete immutable candidate identity"},
		{"no sources", func(r *WorktreeMergeReceipt) { r.Sources = nil }, "has no receipted sources"},
		{"missing source task", func(r *WorktreeMergeReceipt) { r.Sources[0].Task = "" }, "has an incomplete immutable source identity"},
		{"missing source path", func(r *WorktreeMergeReceipt) { r.Sources[0].Worktree = "" }, "has an incomplete immutable source identity"},
		{"missing source branch", func(r *WorktreeMergeReceipt) { r.Sources[0].Branch = "" }, "has an incomplete immutable source identity"},
		{"missing source SHA", func(r *WorktreeMergeReceipt) { r.Sources[0].SHA = "" }, "has an incomplete immutable source identity"},
		{"incomplete second source", func(r *WorktreeMergeReceipt) { r.Sources = append(r.Sources, WorktreeMergeSource{Task: "second"}) }, "has an incomplete immutable source identity"},
		{"identity before status", func(r *WorktreeMergeReceipt) { r.ID = ""; r.Status = WorktreeMergePrepared }, "has inconsistent immutable receipt identity"},
		{"status before landing", func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared; r.LandingSHA = "landing" }, "want an unpublished prepare conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "receipt.json")
			r := WorktreeMergeReceipt{ID: "receipt", ReceiptPath: path, Repository: "acme/app", Target: "main", TargetSHA: "target", Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict, Candidate: WorktreeMergeCandidate{Task: "candidate", Worktree: "candidate-path", Branch: "candidate-branch"}, Sources: []WorktreeMergeSource{{Task: "source", Worktree: "source-path", Branch: "source-branch", SHA: "source-head"}}}
			r.Lane = worktreeMergeLaneID(r.Repository, r.Target)
			if tc.mutate != nil {
				tc.mutate(&r)
			}
			err := validateAbsorbedConflictReceipt(r, path)
			if tc.diagnostic == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			want := fmt.Sprintf("receipt %s %s", path, tc.diagnostic)
			if tc.diagnostic == "want an unpublished prepare conflict" {
				want = fmt.Sprintf("receipt %s is %s/%s, %s", path, r.Phase, r.Status, tc.diagnostic)
			}
			if err == nil || err.Error() != want {
				t.Fatalf("error=%v want %q", err, want)
			}
		})
	}
	t.Run("malformed retired sidecar fails closed without rewriting evidence", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "receipt.json")
		r := WorktreeMergeReceipt{ID: "receipt", ReceiptPath: path, Repository: "acme/app", Target: "main", TargetSHA: "target", Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict, PullRequest: "https://example.test/pull/1", Candidate: WorktreeMergeCandidate{Task: "candidate", Worktree: "candidate-path", Branch: "candidate-branch"}, Sources: []WorktreeMergeSource{{Task: "source", Worktree: "source-path", Branch: "source-branch", SHA: "source-head"}}}
		r.Lane = worktreeMergeLaneID(r.Repository, r.Target)
		if err := persistWorktreeMergeReceipt(r); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sidecar := retiredPublicationAcknowledgementPath(path)
		corrupt := []byte("not JSON\n")
		if err := os.WriteFile(sidecar, corrupt, 0600); err != nil {
			t.Fatal(err)
		}
		err = validateAbsorbedConflictReceipt(r, path)
		if err == nil || !strings.Contains(err.Error(), "retired-publication acknowledgement") {
			t.Fatalf("error=%v want actual sidecar reader refusal", err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("receipt rewritten")
		}
		after, err = os.ReadFile(sidecar)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(corrupt, after) {
			t.Fatal("sidecar rewritten")
		}
	})
}
