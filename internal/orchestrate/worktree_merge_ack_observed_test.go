package orchestrate

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

func historicalObservedAcknowledgement(t *testing.T) (WorktreeMergeReceipt, WorktreeMergeLandedFailureAcknowledgement) {
	t.Helper()
	r := acknowledgementReadReceipt(t)
	r.Status, r.Phase, r.LandingSHA = WorktreeMergePostTargetCIFailed, WorktreeMergePhaseLand, "landing-sha"
	r.Checks.Status, r.Checks.Head = githubchecks.PullRequestWaitFailed, r.LandingSHA
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	hash, err := worktreeMergeReceiptSHA256(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	a := WorktreeMergeLandedFailureAcknowledgement{
		SchemaVersion: 1, Status: "landed_failure_acknowledged", ReceiptSHA256: hash,
		ReceiptPath: r.ReceiptPath, AcknowledgementPath: landedFailureAcknowledgementPath(r.ReceiptPath),
		ReceiptID: r.ID, ReceiptStatus: r.Status, Lane: r.Lane, Repository: r.Repository, Target: r.Target,
		ReceiptTargetSHA: r.TargetSHA, ReceiptLandingSHA: r.LandingSHA, CurrentTargetSHA: "remote-target-sha",
		CandidateSHA: r.Candidate.SHA, ClaimBaseSHA: r.TargetSHA, CandidateWorktree: r.Candidate.Worktree,
		CandidateBranch: r.Candidate.Branch, Sources: append([]WorktreeMergeSource(nil), r.Sources...),
		ObservedSources: append([]WorktreeMergeSource(nil), r.Sources...),
		Actor:           "operator", Reason: "historical landed descendant proof", RecordedAt: r.CreatedAt,
	}
	a.ObservedSources[0].SHA = "observed-descendant-sha"
	a.ID = landedFailureAcknowledgementID(a)
	return r, a
}

func TestHistoricalObservedAcknowledgementPreservesEvidence(t *testing.T) {
	t.Parallel()
	r, a := historicalObservedAcknowledgement(t)
	writeAcknowledgementReadJSON(t, a.AcknowledgementPath, a)
	before, err := os.ReadFile(a.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := readLandedFailureAcknowledgement(a.AcknowledgementPath, r)
	if err != nil || stored.ID != a.ID || stored.ReceiptSHA256 != a.ReceiptSHA256 || stored.ObservedSources[0] != a.ObservedSources[0] {
		t.Fatalf("read historical evidence: %+v, %v", stored, err)
	}
	after, err := os.ReadFile(a.AcknowledgementPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("reader changed historical bytes: %v", err)
	}
	if ok, err := hasLandedFailureAcknowledgement(r); !ok || err != nil {
		t.Fatalf("historic acknowledgement not recognized: %v, %v", ok, err)
	}
}

func TestHistoricalObservedAcknowledgementRejectsMalformedEvidence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*WorktreeMergeReceipt, *WorktreeMergeLandedFailureAcknowledgement)
		want   string
	}{
		{"digest without observations", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) { a.ObservedSources = nil }, "lacks observed sources"},
		{"missing receipt", func(r *WorktreeMergeReceipt, _ *WorktreeMergeLandedFailureAcknowledgement) {
			if err := os.Remove(r.ReceiptPath); err != nil {
				t.Fatal(err)
			}
		}, "no such file"},
		{"changed receipt bytes", func(r *WorktreeMergeReceipt, _ *WorktreeMergeLandedFailureAcknowledgement) {
			if err := os.WriteFile(r.ReceiptPath, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
		}, "invalid historical"},
		{"wrong receipt digest", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) { a.ReceiptSHA256 = "wrong" }, "invalid historical"},
		{"missing receipt digest", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) { a.ReceiptSHA256 = "" }, "invalid historical"},
		{"invalid receipt state", func(r *WorktreeMergeReceipt, _ *WorktreeMergeLandedFailureAcknowledgement) { r.Checks.Head = "wrong" }, "invalid historical"},
		{"other receipt status", func(r *WorktreeMergeReceipt, _ *WorktreeMergeLandedFailureAcknowledgement) {
			r.Status, r.Phase, r.LandingSHA = WorktreeMergeValidationFailed, WorktreeMergePhasePrepare, ""
		}, "invalid historical"},
		{"extra observation", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) {
			a.ObservedSources = append(a.ObservedSources, a.ObservedSources[0])
		}, "invalid historical"},
		{"missing actor", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) { a.Actor = "" }, "invalid historical"},
		{"missing reason", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) { a.Reason = "" }, "invalid historical"},
		{"missing timestamp", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) {
			a.RecordedAt = time.Time{}
		}, "invalid historical"},
		{"other candidate checkout", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) {
			a.CandidateWorktree = "other"
		}, "invalid historical"},
		{"other candidate branch", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) {
			a.CandidateBranch = "other"
		}, "invalid historical"},
		{"other source task", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) {
			a.ObservedSources[0].Task = "other"
		}, "mismatched observed source"},
		{"other source branch", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) {
			a.ObservedSources[0].Branch = "other"
		}, "mismatched observed source"},
		{"other source checkout", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) {
			a.ObservedSources[0].Worktree = "other"
		}, "mismatched observed source"},
		{"other merged marker", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) {
			a.ObservedSources[0].Merged = !a.ObservedSources[0].Merged
		}, "mismatched observed source"},
		{"missing observed sha", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) {
			a.ObservedSources[0].SHA = ""
		}, "mismatched observed source"},
		{"other repository", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) {
			a.Repository = "other/repo"
		}, "invalid receipt identity"},
	}
	for _, tc := range cases {
		//nolint:paralleltest // Mutation callbacks retain the parent test assertion handle.
		t.Run(tc.name, func(t *testing.T) {
			r, a := historicalObservedAcknowledgement(t)
			tc.mutate(&r, &a)
			a.ID = landedFailureAcknowledgementID(a)
			writeAcknowledgementReadJSON(t, a.AcknowledgementPath, a)
			stored, err := readLandedFailureAcknowledgement(a.AcknowledgementPath, r)
			if err == nil || !strings.Contains(err.Error(), tc.want) || stored.ID != "" {
				t.Fatalf("expected %q and no accepted evidence; got %+v, %v", tc.want, stored, err)
			}
		})
	}
}

func TestHistoricalObservedAcknowledgementDigestBindsObservedFields(t *testing.T) {
	t.Parallel()
	_, a := historicalObservedAcknowledgement(t)
	for _, field := range []string{"ReceiptSHA256", "Task", "Worktree", "Branch", "SHA"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			changed := a
			changed.ObservedSources = append([]WorktreeMergeSource(nil), a.ObservedSources...)
			switch field {
			case "ReceiptSHA256":
				changed.ReceiptSHA256 += "changed"
			case "Task":
				changed.ObservedSources[0].Task += "changed"
			case "Worktree":
				changed.ObservedSources[0].Worktree += "changed"
			case "Branch":
				changed.ObservedSources[0].Branch += "changed"
			case "SHA":
				changed.ObservedSources[0].SHA += "changed"
			}
			if landedFailureAcknowledgementID(changed) == a.ID {
				t.Fatal("observed evidence not bound into identity")
			}
		})
	}
	legacy := a
	legacy.ObservedSources, legacy.ReceiptSHA256 = nil, ""
	data, err := json.Marshal(legacy)
	if err != nil || bytes.Contains(data, []byte("observed_sources")) || bytes.Contains(data, []byte("receipt_sha256")) {
		t.Fatalf("legacy wire format changed: %s, %v", data, err)
	}
}

func TestHistoricalObservedAcknowledgementIdentityMatchesOriginalFormat(t *testing.T) {
	t.Parallel()
	a := WorktreeMergeLandedFailureAcknowledgement{
		ReceiptID: "receipt-id", ReceiptPath: "/projects/.wb/receipt.json", ReceiptStatus: WorktreeMergePostTargetCIFailed,
		ReceiptTargetSHA: "target", ReceiptLandingSHA: "landing", CurrentTargetSHA: "current-target", CandidateSHA: "candidate",
		ReceiptSHA256:   "receipt-digest",
		Sources:         []WorktreeMergeSource{{Task: "source", Worktree: "/worktrees/source", Branch: "feature/source", SHA: "source-sha"}},
		ObservedSources: []WorktreeMergeSource{{Task: "source", Worktree: "/worktrees/source", Branch: "feature/source", SHA: "descendant-sha"}},
	}
	// This vector uses the historical NUL-delimited wire identity, independently
	// computed rather than round-tripping the current implementation.
	const originalID = "4ae041d660417fb1195517c030c8c72b48a0b73f648d9cd2146f7377333a3909"
	if got := landedFailureAcknowledgementID(a); got != originalID {
		t.Fatalf("historical identity = %s, want %s", got, originalID)
	}
}

func TestHistoricalObservedAcknowledgementRejectsUnboundDescendant(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"changed descendant", "legacy id"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r, a := historicalObservedAcknowledgement(t)
			if kind == "changed descendant" {
				a.ObservedSources[0].SHA = "another-descendant"
			} else {
				legacy := a
				legacy.ObservedSources, legacy.ReceiptSHA256 = nil, ""
				a.ID = landedFailureAcknowledgementID(legacy)
			}
			writeAcknowledgementReadJSON(t, a.AcknowledgementPath, a)
			if _, err := readLandedFailureAcknowledgement(a.AcknowledgementPath, r); err == nil || !strings.Contains(err.Error(), "invalid receipt identity") {
				t.Fatalf("unbound descendant accepted: %v", err)
			}
		})
	}
}
