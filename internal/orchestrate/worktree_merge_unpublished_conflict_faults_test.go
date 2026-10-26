package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestUnpublishedConflictAcknowledgementReaderAuthenticatesEveryPreservedClaim(t *testing.T) {
	t.Parallel()
	r := unpublishedConflictPolicyFixture()
	r.ReceiptPath = filepath.Join(t.TempDir(), "receipt.json")
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.ReceiptPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ack, err := inspectUnpublishedConflictWithPorts(t.Context(), "/root", r, "actor", "reason", unpublishedConflictInspectionFixture(r))
	if err != nil {
		t.Fatal(err)
	}
	ack.ReceiptSHA256, err = worktreeMergeReceiptSHA256(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	ack.ID = unpublishedConflictProofID(ack)
	write := func(value WorktreeMergeUnpublishedConflictAcknowledgement) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ack.AcknowledgementPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(ack)
	if _, err := readUnpublishedConflictAcknowledgement(r); err != nil {
		t.Fatal(err)
	}
	bad := ack
	bad.Claims = append([]UnpublishedConflictClaim(nil), ack.Claims...)
	bad.Claims[0].ClaimID = ""
	bad.ID = unpublishedConflictProofID(bad)
	write(bad)
	if _, err := readUnpublishedConflictAcknowledgement(r); err == nil {
		t.Fatal("accepted unbound preserved claim")
	}
	bad = ack
	bad.ReceiptSHA256 = "changed"
	bad.ID = unpublishedConflictProofID(bad)
	write(bad)
	if _, err := readUnpublishedConflictAcknowledgement(r); err == nil {
		t.Fatal("accepted changed receipt digest")
	}
	write(ack)
	invalid := r
	invalid.Phase = WorktreeMergePhasePrepare
	if _, err := readUnpublishedConflictAcknowledgement(invalid); err == nil {
		t.Fatal("accepted recoverable receipt shape")
	}
	invalid = r
	invalid.RebatchOf = filepath.Join(t.TempDir(), "missing-predecessor.json")
	if _, err := readUnpublishedConflictAcknowledgement(invalid); err == nil {
		t.Fatal("accepted unbound rebatch predecessor")
	}
	if err := os.Remove(r.ReceiptPath); err != nil {
		t.Fatal(err)
	}
	if _, err := readUnpublishedConflictAcknowledgement(r); err == nil {
		t.Fatal("accepted absent immutable receipt")
	}
}

func unpublishedConflictPolicyFixture() WorktreeMergeReceipt {
	r := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, Repository: "acme/app", Target: "main", TargetSHA: "base", Phase: WorktreeMergePhaseLand, Status: WorktreeMergeConflict, ReceiptPath: "/receipt", Sources: []WorktreeMergeSource{{Task: "source", Worktree: "/source", Branch: "feature/source", SHA: "source-head"}}}
	r.Lane = worktreeMergeLaneID(r.Repository, r.Target)
	r.ID = worktreeMergeOperationID(r.Lane, r.Sources)
	r.Candidate = WorktreeMergeCandidate{Task: r.ID, Worktree: "/candidate", Branch: "wb/candidate", SHA: "candidate-head"}
	return r
}

func unpublishedConflictInspectionFixture(r WorktreeMergeReceipt) unpublishedConflictInspectionPorts {
	assets := map[string]WorktreeMergeSource{r.Candidate.Worktree: {Task: r.Candidate.Task, Worktree: r.Candidate.Worktree, Branch: r.Candidate.Branch, SHA: r.Candidate.SHA}, r.Sources[0].Worktree: r.Sources[0]}
	for _, candidate := range r.RebatchedCandidates {
		assets[candidate.Worktree] = WorktreeMergeSource{Task: candidate.Task, Worktree: candidate.Worktree, Branch: candidate.Branch, SHA: candidate.SHA}
	}
	return unpublishedConflictInspectionPorts{
		lineage:                func(WorktreeMergeReceipt) ([]UnpublishedConflictRebatchProof, error) { return nil, nil },
		predecessorPublication: func(context.Context, UnpublishedConflictRebatchProof) error { return nil },
		home:                   func(string) (string, error) { return "/private-test-home", nil },
		lane: func(string, string, string) (landinglane.Record, bool, error) {
			return landinglane.Record{}, false, nil
		},
		guard: func(_ context.Context, path string, _ worktrees.GuardOptions) (worktrees.GuardResult, error) {
			return worktrees.GuardResult{Path: path, Kind: "linked", Branch: assets[path].Branch, CanonicalDir: "/canonical"}, nil
		},
		canonical: func(string, string, string) (string, bool, error) { return "/canonical", true, nil },
		clean:     func(context.Context, string) error { return nil },
		head:      func(_ context.Context, path string) (string, error) { return assets[path].SHA, nil },
		view: func(_ context.Context, o worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error) {
			a := assets[o.Worktree]
			return worktrees.WorkLogView{Claim: &worktrees.WorkLogClaimView{ClaimID: "claim-" + a.Task, ClaimPath: "/claims/" + a.Task, Task: a.Task, Repository: r.Repository, Worktree: a.Worktree, Branch: a.Branch, Base: r.Target, BaseSHA: r.TargetSHA, Lifecycle: "active"}}, nil
		},
		ancestor: func(_ context.Context, _ string, base, head string) (bool, error) {
			return head != "remote-target" || base == r.TargetSHA, nil
		},
		hash:         func(string) (string, error) { return strings.Repeat("a", 64), nil },
		remote:       func(context.Context, string, string) (string, error) { return "", nil },
		target:       func(context.Context, string, string) (string, error) { return "remote-target", nil },
		pullRequests: func(context.Context, string, WorktreeMergeReceipt) error { return nil },
	}
}

func TestUnpublishedConflictInspectionRefusesEachFailedEvidenceOwner(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"shape", "lineage", "home", "lane read", "live lane", "idle lane", "guard", "canonical read", "canonical mismatch", "clean", "head read", "head mismatch", "claim read", "claim identity", "candidate base", "claim ancestry read", "claim ancestry absent", "live worktree", "claim hash", "remote read", "remote published", "pull request history", "target read", "target ancestry read", "target rewritten", "candidate ancestry read", "candidate landed", "receipt hash"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := unpublishedConflictPolicyFixture()
			p := unpublishedConflictInspectionFixture(r)
			sentinel := errors.New("selected evidence unavailable")
			switch kind {
			case "shape":
				r.Phase = WorktreeMergePhasePrepare
			case "lineage":
				p.lineage = func(WorktreeMergeReceipt) ([]UnpublishedConflictRebatchProof, error) { return nil, sentinel }
			case "home":
				p.home = func(string) (string, error) { return "", sentinel }
			case "lane read":
				p.lane = func(string, string, string) (landinglane.Record, bool, error) {
					return landinglane.Record{}, false, sentinel
				}
			case "live lane":
				p.lane = func(string, string, string) (landinglane.Record, bool, error) {
					return landinglane.Record{Owner: landinglane.Owner{OperationPID: os.Getpid()}}, true, nil
				}
			case "idle lane":
				p.lane = func(string, string, string) (landinglane.Record, bool, error) {
					return landinglane.Record{Owner: landinglane.Owner{OperationPID: 999999999}}, true, nil
				}
			case "guard":
				p.guard = func(context.Context, string, worktrees.GuardOptions) (worktrees.GuardResult, error) {
					return worktrees.GuardResult{}, sentinel
				}
			case "canonical read":
				p.canonical = func(string, string, string) (string, bool, error) { return "", false, sentinel }
			case "canonical mismatch":
				p.canonical = func(string, string, string) (string, bool, error) { return "/wrong", false, nil }
			case "clean":
				p.clean = func(context.Context, string) error { return sentinel }
			case "head read":
				p.head = func(context.Context, string) (string, error) { return "", sentinel }
			case "head mismatch":
				p.head = func(context.Context, string) (string, error) { return "changed", nil }
			case "claim read":
				p.view = func(context.Context, worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error) {
					return worktrees.WorkLogView{}, sentinel
				}
			case "claim identity", "candidate base", "live worktree":
				native := p.view
				p.view = func(ctx context.Context, o worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error) {
					v, e := native(ctx, o)
					if kind == "claim identity" {
						v.Claim.ClaimID = ""
					}
					if kind == "candidate base" {
						v.Claim.BaseSHA = "wrong"
					}
					if kind == "live worktree" {
						v.Owners = []worktrees.OwnerView{{OwnerRegistration: worktrees.OwnerRegistration{PID: os.Getpid()}, PIDStatus: "active"}}
					}
					return v, e
				}
			case "claim ancestry read", "claim ancestry absent", "target ancestry read", "target rewritten", "candidate ancestry read", "candidate landed":
				native := p.ancestor
				p.ancestor = func(ctx context.Context, path, base, head string) (bool, error) {
					selected := strings.HasPrefix(kind, "claim") && head == r.Candidate.SHA || strings.HasPrefix(kind, "target") && head == "remote-target" && base == r.TargetSHA || strings.HasPrefix(kind, "candidate") && head == "remote-target" && base == r.Candidate.SHA
					if selected {
						if strings.HasSuffix(kind, "read") {
							return false, sentinel
						}
						return kind == "candidate landed", nil
					}
					return native(ctx, path, base, head)
				}
			case "claim hash":
				p.hash = func(string) (string, error) { return "", sentinel }
			case "receipt hash":
				native := p.hash
				p.hash = func(path string) (string, error) {
					if path == r.ReceiptPath {
						return "", sentinel
					}
					return native(path)
				}
			case "remote read":
				p.remote = func(context.Context, string, string) (string, error) { return "", sentinel }
			case "remote published":
				p.remote = func(context.Context, string, string) (string, error) { return "published-ref", nil }
			case "pull request history":
				p.pullRequests = func(context.Context, string, WorktreeMergeReceipt) error { return sentinel }
			case "target read":
				p.target = func(context.Context, string, string) (string, error) { return "", sentinel }
			}
			ack, err := inspectUnpublishedConflictWithPorts(t.Context(), "/root", r, "actor", "reason", p)
			if kind == "idle lane" {
				if err != nil || ack.ID == "" {
					t.Fatalf("proved idle lane refused: %+v %v", ack, err)
				}
				return
			}
			if err == nil || ack.ID != "" {
				t.Fatalf("failed evidence accepted: %+v %v", ack, err)
			}
		})
	}
}

func TestUnpublishedConflictApplicationNeverWritesAfterOwnerFailureOrDrift(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"resolve", "first read", "shape", "audit identity", "lock", "locked read", "locked identity substitution", "final identity substitution", "first inspection", "reviewed proof", "existing differs", "existing unreadable", "last read", "last inspection", "late drift", "persist", "success"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := unpublishedConflictPolicyFixture()
			ack := WorktreeMergeUnpublishedConflictAcknowledgement{ID: "proof", AcknowledgementPath: "/ack"}
			sentinel := errors.New("selected owner failure")
			reads, inspections, writes := 0, 0, 0
			p := unpublishedConflictPorts{resolve: func(string, string) (string, error) { return "/receipt", nil }, read: func(string) (WorktreeMergeReceipt, error) {
				reads++
				if kind == "first read" && reads == 1 || kind == "locked read" && reads == 2 || kind == "last read" && reads == 3 {
					return WorktreeMergeReceipt{}, sentinel
				}
				if kind == "locked identity substitution" && reads == 2 || kind == "final identity substitution" && reads == 3 {
					changed := r
					changed.Target = "other-target"
					changed.Lane = worktreeMergeLaneID(changed.Repository, changed.Target)
					changed.ID = worktreeMergeOperationID(changed.Lane, changed.Sources)
					changed.Candidate.Task = changed.ID
					return changed, nil
				}
				return r, nil
			}, lock: func(string, string, bool) (OperationLock, error) { return OperationLock{}, nil }, inspect: func(context.Context, string, WorktreeMergeReceipt, string, string) (WorktreeMergeUnpublishedConflictAcknowledgement, error) {
				inspections++
				if kind == "first inspection" && inspections == 1 || kind == "last inspection" && inspections == 2 {
					return WorktreeMergeUnpublishedConflictAcknowledgement{}, sentinel
				}
				if kind == "late drift" && inspections == 2 {
					return WorktreeMergeUnpublishedConflictAcknowledgement{ID: "changed"}, nil
				}
				return ack, nil
			}, ack: func(WorktreeMergeReceipt) (WorktreeMergeUnpublishedConflictAcknowledgement, error) {
				return WorktreeMergeUnpublishedConflictAcknowledgement{}, os.ErrNotExist
			}, persist: func(string, WorktreeMergeUnpublishedConflictAcknowledgement) error {
				writes++
				if kind == "persist" {
					return sentinel
				}
				return nil
			}}
			o := WorktreeMergeUnpublishedConflictOptions{Apply: true, Actor: "actor", Reason: "reason", ExpectedProofID: "proof"}
			switch kind {
			case "resolve":
				p.resolve = func(string, string) (string, error) { return "", sentinel }
			case "shape":
				r.Phase = WorktreeMergePhasePrepare
			case "audit identity":
				o.Actor = ""
			case "lock":
				p.lock = func(string, string, bool) (OperationLock, error) { return OperationLock{}, sentinel }
			case "reviewed proof":
				o.ExpectedProofID = "old"
			case "existing differs":
				p.ack = func(WorktreeMergeReceipt) (WorktreeMergeUnpublishedConflictAcknowledgement, error) {
					return WorktreeMergeUnpublishedConflictAcknowledgement{ID: "other"}, nil
				}
			case "existing unreadable":
				p.ack = func(WorktreeMergeReceipt) (WorktreeMergeUnpublishedConflictAcknowledgement, error) {
					return WorktreeMergeUnpublishedConflictAcknowledgement{}, sentinel
				}
			}
			_, err := acknowledgeUnpublishedConflict(t.Context(), o, p)
			if kind == "success" {
				if err != nil || writes != 1 {
					t.Fatalf("valid proof not published once: writes=%d %v", writes, err)
				}
				return
			}
			if err == nil {
				t.Fatal("failed owner accepted")
			}
			if kind != "persist" && writes != 0 {
				t.Fatalf("failure published acknowledgement %d", writes)
			}
		})
	}
}
