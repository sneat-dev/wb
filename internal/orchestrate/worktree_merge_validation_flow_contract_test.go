package orchestrate

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/mergevalidation"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
)

func TestValidationProgressReportsCompletedFailureWithoutRetry(t *testing.T) {
	t.Parallel()
	if reportWorktreeMergeQualityProgress(nil) != nil {
		t.Fatal("nil reporter produced a callback")
	}
	var got []progress.Event
	callback := reportWorktreeMergeQualityProgress(func(event progress.Event) { got = append(got, event) })
	callback(quality.Progress{State: quality.ProgressCompleted, Status: quality.StatusFailed, Check: quality.CheckBuild, Command: "  go build  ", Detail: " go build ", Attempts: 1, Completed: 2, Total: 4})
	want := progress.Event{Operation: "worktree_merge", Phase: "validate_candidate", State: progress.Failed, Detail: "build: go build: attempt 1: failed", Completed: 2, Total: 4}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("completed failure = %#v, want %#v", got, want)
	}
}

func validationFlowDirectDeferral() (WorktreeMergeReceipt, worktreeMergeValidationPlan) {
	contract := &worktreeMergeDirectCIContract{PullRequest: "https://github.com/acme/app/pull/17", PullRequestNumber: 17, Base: "main", WorkflowID: 300}
	receipt := WorktreeMergeReceipt{Status: WorktreeMergePrepared, Route: WorktreeMergeRouteDecision{Route: WorktreeMergeRouteDirect}, Candidate: WorktreeMergeCandidate{SHA: "exact-candidate"},
		Validation:         quality.VerificationReport{Status: quality.StatusSkipped, Revision: "exact-candidate"},
		ValidationDeferral: &WorktreeMergeValidationDeferral{Route: WorktreeMergeRouteDirect, CandidateSHA: "exact-candidate", DirectCIPullRequest: contract.PullRequest, DirectCIPullRequestNumber: contract.PullRequestNumber, DirectCIBase: contract.Base, DirectCIWorkflowID: contract.WorkflowID}}
	return receipt, worktreeMergeValidationPlan{Defer: true, DirectCI: contract}
}

func TestDirectValidationDeferralPreservesExactAgreementAndDistinctCallerGates(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name                string
		mutate              func(*WorktreeMergeReceipt, *worktreeMergeValidationPlan)
		prepared, published bool
	}{
		{name: "exact direct", prepared: true, published: true},
		{name: "legacy empty route", mutate: func(r *WorktreeMergeReceipt, _ *worktreeMergeValidationPlan) { r.Route.Route = "" }, prepared: true},
		{name: "wrong current route", mutate: func(r *WorktreeMergeReceipt, _ *worktreeMergeValidationPlan) {
			r.Route.Route = WorktreeMergeRoutePullRequest
		}},
		{name: "not prepared", mutate: func(r *WorktreeMergeReceipt, _ *worktreeMergeValidationPlan) {
			r.Status = WorktreeMergeValidationFailed
		}, published: true},
		{name: "current plan requires local", mutate: func(_ *WorktreeMergeReceipt, p *worktreeMergeValidationPlan) { p.Defer = false }},
		{name: "no current contract", mutate: func(_ *WorktreeMergeReceipt, p *worktreeMergeValidationPlan) { p.DirectCI = nil }},
		{name: "missing recorded deferral", mutate: func(r *WorktreeMergeReceipt, _ *worktreeMergeValidationPlan) { r.ValidationDeferral = nil }},
		{name: "wrong recorded route", mutate: func(r *WorktreeMergeReceipt, _ *worktreeMergeValidationPlan) {
			r.ValidationDeferral.Route = WorktreeMergeRoutePullRequest
		}},
		{name: "candidate drift", mutate: func(r *WorktreeMergeReceipt, _ *worktreeMergeValidationPlan) {
			r.ValidationDeferral.CandidateSHA = "other"
		}},
		{name: "pull URL drift", mutate: func(r *WorktreeMergeReceipt, _ *worktreeMergeValidationPlan) {
			r.ValidationDeferral.DirectCIPullRequest = "other"
		}},
		{name: "pull number drift", mutate: func(r *WorktreeMergeReceipt, _ *worktreeMergeValidationPlan) {
			r.ValidationDeferral.DirectCIPullRequestNumber++
		}},
		{name: "base drift", mutate: func(r *WorktreeMergeReceipt, _ *worktreeMergeValidationPlan) {
			r.ValidationDeferral.DirectCIBase = "other"
		}},
		{name: "workflow drift", mutate: func(r *WorktreeMergeReceipt, _ *worktreeMergeValidationPlan) {
			r.ValidationDeferral.DirectCIWorkflowID++
		}},
		{name: "validation no longer skipped", mutate: func(r *WorktreeMergeReceipt, _ *worktreeMergeValidationPlan) {
			r.Validation.Status = quality.StatusPassed
		}},
		{name: "validation revision drift", mutate: func(r *WorktreeMergeReceipt, _ *worktreeMergeValidationPlan) { r.Validation.Revision = "other" }},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			receipt, plan := validationFlowDirectDeferral()
			if row.mutate != nil {
				row.mutate(&receipt, &plan)
			}
			before, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := preparedValidationStillValidContext(t.Context(), receipt, plan, 0, 0, 0)
			if err != nil || prepared != row.prepared {
				t.Fatalf("prepared = %t, %v, want %t", prepared, err, row.prepared)
			}
			err = requireWorktreeMergePublishedValidationContext(t.Context(), receipt, plan, 0, 0, 0)
			if (err == nil) != row.published {
				t.Fatalf("published error = %v, accepted want %t", err, row.published)
			}
			after, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("eligibility inspection changed receipt")
			}
		})
	}
}

func TestValidationReceiptRefusalsPrecedeDeferralAndFingerprinting(t *testing.T) {
	t.Parallel()
	t.Run("new regression", func(t *testing.T) {
		t.Parallel()
		receipt, plan := validationFlowDirectDeferral()
		receipt.Validation = quality.VerificationReport{Status: quality.StatusFailed, Revision: receipt.Candidate.SHA, Results: []quality.VerificationEntry{{Language: "go", Check: quality.CheckBuild, Command: "go build", Status: quality.StatusFailed, Detail: "new compiler failure"}}}
		before, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if reusable, err := preparedValidationStillValidContext(t.Context(), receipt, plan, 0, 0, 0); err != nil || reusable {
			t.Fatalf("new regression reused: %t, %v", reusable, err)
		}
		if err := requireWorktreeMergePublishedValidationContext(t.Context(), receipt, plan, 0, 0, 0); err == nil || !strings.Contains(err.Error(), "recheck candidate deadcode regression before publish") || !strings.Contains(err.Error(), "introduced or changed failure") {
			t.Fatalf("new regression publish = %v", err)
		}
		after, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatal("regression refusal mutated receipt")
		}
	})
	t.Run("corrupt imported identity", func(t *testing.T) {
		t.Parallel()
		receipt, plan := validationFlowDirectDeferral()
		receipt.ImportedMainDeadcode = &mergevalidation.ImportedMainDeadcode{CandidateSHA: "other-candidate"}
		if err := requireWorktreeMergePublishedValidationContext(t.Context(), receipt, plan, 0, 0, 0); err == nil || !strings.Contains(err.Error(), "recheck imported main deadcode attestation before publish") || !strings.Contains(err.Error(), "does not bind the exact") {
			t.Fatalf("corrupt attestation publish = %v", err)
		}
		if reusable, err := preparedValidationStillValidContext(t.Context(), receipt, plan, 0, 0, 0); err == nil || reusable {
			t.Fatalf("corrupt attestation reuse = %t, %v", reusable, err)
		}
	})
	t.Run("wrong target baseline", func(t *testing.T) {
		t.Parallel()
		receipt := WorktreeMergeReceipt{Status: WorktreeMergePrepared, TargetSHA: "exact-target", Candidate: WorktreeMergeCandidate{SHA: "exact-candidate"}, ValidationIdentity: &WorktreeMergeValidationIdentity{CandidateSHA: "exact-candidate"}}
		report := quality.VerificationReport{Status: quality.StatusFailed, WorkspaceClean: true, Results: []quality.VerificationEntry{{Language: "go", Check: quality.CheckBuild, Command: "go build", Status: quality.StatusFailed, Detail: "same inherited compiler failure"}}}
		receipt.Validation = report
		receipt.Validation.Revision = receipt.Candidate.SHA
		receipt.BaselineValidation = report
		receipt.BaselineValidation.Revision = "wrong-target"
		if reusable, err := preparedValidationStillValidContext(t.Context(), receipt, worktreeMergeValidationPlan{}, 0, 0, 0); err != nil || reusable {
			t.Fatalf("wrong baseline reused = %t, %v", reusable, err)
		}
	})
}

func TestDirectValidationInputRefusalDoesNotStampSkippedEvidence(t *testing.T) {
	t.Parallel()
	receipt, plan := validationFlowDirectDeferral()
	before, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	err = applyOrDeferWorktreeMergeValidation(context.Background(), &receipt, plan, 0, 0, 0, 0, func(progress.Event) { calls++ })
	if err == nil || !strings.Contains(err.Error(), "requires exact target and candidate identities") {
		t.Fatalf("incomplete input = %v", err)
	}
	after, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || calls != 0 {
		t.Fatalf("input refusal mutated receipt/progress: %s -> %s, events %d", before, after, calls)
	}
}

func TestCandidateInvalidQualityPolicyStopsBeforeChecksAndPreservesPriorReport(t *testing.T) {
	t.Parallel()
	candidate := t.TempDir()
	writeEngineFile(t, filepath.Join(candidate, ".wb", "quality.yaml"), "version: 2\n")
	prior := quality.VerificationReport{Status: quality.StatusFailed, Revision: "previous-candidate", Results: []quality.VerificationEntry{{Check: quality.CheckBuild, Status: quality.StatusFailed, Detail: "prior compiler failure"}}}
	receipt := WorktreeMergeReceipt{Repository: "acme/app", Candidate: WorktreeMergeCandidate{SHA: "new-candidate", Worktree: candidate}, Validation: prior,
		ImportedMainDeadcode: &mergevalidation.ImportedMainDeadcode{CandidateSHA: "previous-candidate"}}
	var events []progress.Event
	err := validateWorktreeMergeCandidate(t.Context(), &receipt, 0, 0, 0, 0, func(event progress.Event) { events = append(events, event) })
	if err == nil || !strings.Contains(err.Error(), "load candidate quality policy") || !strings.Contains(err.Error(), "has version 2; want 1") {
		t.Fatalf("invalid policy refusal = %v", err)
	}
	if !reflect.DeepEqual(receipt.Validation, prior) || receipt.ImportedMainDeadcode != nil || len(events) != 0 {
		t.Fatalf("invalid policy ran checks or replaced report: receipt %+v, events %+v", receipt, events)
	}
}
