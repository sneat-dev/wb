package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnpublishedConflictRebatchLineageRefusesChangedImmutableEvidence(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"success", "cycle", "outside", "read", "bridge", "replacement", "published metadata", "ambiguous publication", "receipt hash", "bridge hash", "unbound candidate"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := unpublishedConflictPolicyFixture()
			dir := t.TempDir()
			r.ReceiptPath = filepath.Join(dir, "current.json")
			r.RebatchOf = filepath.Join(dir, "previous.json")
			if err := os.WriteFile(r.RebatchOf, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			old := r
			old.ReceiptPath, old.RebatchOf = r.RebatchOf, ""
			old.Candidate = WorktreeMergeCandidate{Task: "old", Worktree: "/old", Branch: "wb/old", SHA: "old-head"}
			old.RebatchedCandidates = nil
			r.RebatchedCandidates = []WorktreeMergeCandidate{old.Candidate}
			bridge := WorktreeMergePreparedRebatch{ID: "bridge", ReplacementReceiptPath: r.ReceiptPath, Replacement: r.Candidate, Sources: r.Sources}
			sentinel := errors.New("selected lineage read failed")
			read := func(string) (WorktreeMergeReceipt, error) {
				if kind == "read" {
					return WorktreeMergeReceipt{}, sentinel
				}
				return old, nil
			}
			readBridge := func(string, WorktreeMergeReceipt) (WorktreeMergePreparedRebatch, error) {
				if kind == "bridge" {
					return WorktreeMergePreparedRebatch{}, sentinel
				}
				return bridge, nil
			}
			hash := func(path string) (string, error) {
				if kind == "receipt hash" && path == old.ReceiptPath || kind == "bridge hash" && path == rebatchPath(old.ReceiptPath) {
					return "", sentinel
				}
				return strings.Repeat("a", 64), nil
			}
			switch kind {
			case "cycle":
				r.RebatchOf = r.ReceiptPath
			case "outside":
				r.RebatchOf = filepath.Join(t.TempDir(), "elsewhere")
			case "replacement":
				bridge.Replacement.SHA = "different"
			case "published metadata":
				old.PullRequest = "41"
			case "ambiguous publication":
				old.PublishedCandidateSHA = old.Candidate.SHA
			case "unbound candidate":
				old.RebatchedCandidates = []WorktreeMergeCandidate{old.Candidate}
			}
			proofs, err := unpublishedConflictRebatchLineageWithPorts(r, read, readBridge, hash)
			if kind == "success" {
				if err != nil || len(proofs) != 1 || proofs[0].Candidate != old.Candidate {
					t.Fatalf("lost immutable custody: %+v %v", proofs, err)
				}
				return
			}
			if err == nil || len(proofs) != 0 {
				t.Fatalf("changed lineage accepted: %+v %v", proofs, err)
			}
		})
	}
}

func TestUnpublishedConflictPredecessorInspectionRefusesChangedCustody(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"base", "publication", "ancestry read", "already landed"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := unpublishedConflictPolicyFixture()
			old := WorktreeMergeCandidate{Task: "old", Worktree: "/old", Branch: "wb/old", SHA: "old-head"}
			r.RebatchedCandidates = []WorktreeMergeCandidate{old}
			r.RebatchOf = "/previous"
			p := unpublishedConflictInspectionFixture(r)
			proof := UnpublishedConflictRebatchProof{Candidate: old, TargetSHA: r.TargetSHA}
			if kind == "base" {
				proof.TargetSHA = "different-base"
			}
			p.lineage = func(WorktreeMergeReceipt) ([]UnpublishedConflictRebatchProof, error) {
				return []UnpublishedConflictRebatchProof{proof}, nil
			}
			sentinel := errors.New("predecessor observation failed")
			if kind == "publication" {
				p.predecessorPublication = func(context.Context, UnpublishedConflictRebatchProof) error { return sentinel }
			}
			if kind == "ancestry read" || kind == "already landed" {
				native := p.ancestor
				p.ancestor = func(ctx context.Context, path, base, head string) (bool, error) {
					if base == old.SHA && head == "remote-target" {
						if kind == "ancestry read" {
							return false, sentinel
						}
						return true, nil
					}
					return native(ctx, path, base, head)
				}
			}
			if ack, err := inspectUnpublishedConflictWithPorts(t.Context(), "/root", r, "actor", "reason", p); err == nil || ack.ID != "" {
				t.Fatalf("unproved predecessor accepted: %+v %v", ack, err)
			}
		})
	}
}
