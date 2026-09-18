package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

// installWorktreeMergeDeferralGH installs a fake `gh` that serves the three
// endpoints worktreeMergeValidationDeferralEligible (via
// targetBranchRequiredChecks) and ResolveWorktreeMergeRoute need for
// "acme/app"/"main": the branch policy summary, the authoritative classic
// required-status-check detail, and the active branch rules. See red-team
// finding B2 (sneat-dev/wb#591).
func installWorktreeMergeDeferralGH(t *testing.T, branchJSON, classicDetailJSON, rulesJSON string) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := "#!/bin/sh\nset -eu\n" +
		"case \"$*\" in\n" +
		"  'api repos/acme/app/branches/main --include'|'api repos/acme/app/branches/main') printf '%s\\n' \"$WB_TEST_BRANCH_JSON\" ;;\n" +
		"  'api repos/acme/app/branches/main/protection/required_status_checks --include'|'api repos/acme/app/branches/main/protection/required_status_checks') printf '%s\\n' \"$WB_TEST_CLASSIC_DETAIL_JSON\" ;;\n" +
		"  'api repos/acme/app/rules/branches/main?per_page=100 --include'|'api repos/acme/app/rules/branches/main?per_page=100') printf '%s\\n' \"$WB_TEST_RULES_JSON\" ;;\n" +
		"  *) echo \"unexpected gh command: $*\" >&2; exit 2 ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(withEmptyActionsRuns(body)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_BRANCH_JSON", branchJSON)
	t.Setenv("WB_TEST_CLASSIC_DETAIL_JSON", classicDetailJSON)
	t.Setenv("WB_TEST_RULES_JSON", rulesJSON)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestWorktreeMergeValidationDeferralEligible covers red-team finding B2: the
// pull-request route may defer local validation only when the target's
// required-check policy was read authoritatively, is non-empty, and is fenced
// by a server-enforced strict up-to-date policy.
func TestWorktreeMergeValidationDeferralEligible(t *testing.T) {
	for _, test := range []struct {
		name              string
		branchJSON        string
		classicDetailJSON string
		rulesJSON         string
		wantEligible      bool
		wantReasonHas     string
	}{
		{
			name:              "authoritative non-empty fenced policy defers",
			branchJSON:        `{"protected":true,"protection":{"required_pull_request_reviews":{},"required_status_checks":{}}}`,
			classicDetailJSON: `{"strict":true,"contexts":["CI"],"checks":[]}`,
			rulesJSON:         `[]`,
			wantEligible:      true,
			wantReasonHas:     "authoritative, non-empty, server-fenced",
		},
		{
			name:              "unfenced policy stays local",
			branchJSON:        `{"protected":true,"protection":{"required_pull_request_reviews":{},"required_status_checks":{"contexts":["CI"]}}}`,
			classicDetailJSON: `{"strict":false,"contexts":["CI"],"checks":[]}`,
			rulesJSON:         `[]`,
			wantEligible:      false,
			wantReasonHas:     "no server-enforced strict up-to-date fence",
		},
		{
			name:              "zero required checks stays local",
			branchJSON:        `{"protected":true,"protection":{"required_pull_request_reviews":{}}}`,
			classicDetailJSON: `{}`,
			rulesJSON:         `[]`,
			wantEligible:      false,
			wantReasonHas:     "no required status checks",
		},
		{
			name:              "unreadable policy stays local",
			branchJSON:        ``,
			classicDetailJSON: ``,
			rulesJSON:         ``,
			wantEligible:      false,
			wantReasonHas:     "unreadable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.branchJSON == "" {
				// Simulate an unreadable/unavailable authoritative policy: no
				// `gh` mock at all, so the real read fails (HTTP error / no
				// network), exercising the same "auto falls back to a
				// conservative PR route" path as ResolveWorktreeMergeRoute.
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				t.Setenv("PATH", t.TempDir())
			} else {
				installWorktreeMergeDeferralGH(t, test.branchJSON, test.classicDetailJSON, test.rulesJSON)
			}
			eligible, reason := worktreeMergeValidationDeferralEligible(context.Background(), "acme/app", "main")
			if eligible != test.wantEligible {
				t.Fatalf("eligible = %t, reason = %q, want eligible=%t", eligible, reason, test.wantEligible)
			}
			if !strings.Contains(reason, test.wantReasonHas) {
				t.Fatalf("reason = %q, want it to contain %q", reason, test.wantReasonHas)
			}
		})
	}
}

// TestPrepareWorktreeMergeDefersValidationOnAuthoritativePRRoute is the
// primary sneat-dev/wb#591 acceptance test: a call that resolves the
// pull-request route against an authoritative, non-empty, server-fenced
// required-check policy must not run local candidate validation at all.
func TestPrepareWorktreeMergeDefersValidationOnAuthoritativePRRoute(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "defer-source", "feature/defer", "a.txt", "a\n")
	installWorktreeMergeDeferralGH(t, `{"protected":true,"protection":{"required_pull_request_reviews":{},"required_status_checks":{}}}`,
		`{"strict":true,"contexts":["CI"],"checks":[]}`, `[]`)

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main",
		Model: "test-model", AgentRuntime: "test", Route: WorktreeMergeRoutePullRequest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != WorktreeMergePrepared {
		t.Fatalf("receipt status = %s, want prepared: %+v", receipt.Status, receipt)
	}
	if receipt.Validation.Status != quality.StatusSkipped {
		t.Fatalf("validation.status = %s, want skipped (deferred): %+v", receipt.Validation.Status, receipt.Validation)
	}
	if receipt.ValidationDeferral == nil {
		t.Fatalf("receipt has no validation_deferral: %+v", receipt)
	}
	if receipt.ValidationDeferral.Route != WorktreeMergeRoutePullRequest || receipt.ValidationDeferral.CandidateSHA != receipt.Candidate.SHA || receipt.ValidationDeferral.Reason == "" {
		t.Fatalf("validation_deferral = %+v, want route=pr candidate_sha=%s with a reason", receipt.ValidationDeferral, receipt.Candidate.SHA)
	}
	if receipt.ValidationIdentity != nil {
		t.Fatalf("deferred validation unexpectedly recorded a validation identity: %+v", receipt.ValidationIdentity)
	}
}

// TestPrepareWorktreeMergeValidateLocallyForcesValidationOnPRRoute covers the
// --validate-locally escape hatch: it must force local validation even when
// the pull-request route would otherwise be eligible to defer it.
func TestPrepareWorktreeMergeValidateLocallyForcesValidationOnPRRoute(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "validate-locally-source", "feature/validate-locally", "a.txt", "a\n")
	installWorktreeMergeDeferralGH(t, `{"protected":true,"protection":{"required_pull_request_reviews":{},"required_status_checks":{}}}`,
		`{"strict":true,"contexts":["CI"],"checks":[]}`, `[]`)

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main",
		Model: "test-model", AgentRuntime: "test", Route: WorktreeMergeRoutePullRequest, ValidateLocally: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ValidationDeferral != nil {
		t.Fatalf("--validate-locally unexpectedly deferred validation: %+v", receipt.ValidationDeferral)
	}
	if receipt.Validation.Status != quality.StatusPassed {
		t.Fatalf("validation.status = %s, want passed (ran locally): %+v", receipt.Validation.Status, receipt.Validation)
	}
	if receipt.ValidationIdentity == nil {
		t.Fatalf("locally-run validation is missing its validation identity: %+v", receipt)
	}
}

// TestRequireWorktreeMergePublishedValidationHonorsThisCallsRoute is the
// red-team finding B1 regression test: an exact PR-route deferral, and the
// pre-existing already-published carve-out, must authorize a publish only
// when THIS call's resolved route (receipt.Route, as LandWorktreeMerge stamps
// it once per call) is the pull-request route. A route this call resolves as
// direct must fall through to the ordinary validated-identity requirement.
func TestRequireWorktreeMergePublishedValidationHonorsThisCallsRoute(t *testing.T) {
	base := WorktreeMergeReceipt{
		Candidate:   WorktreeMergeCandidate{SHA: "cccccccccccccccccccccccccccccccccccccccc"},
		PullRequest: "https://example.test/acme/app/pull/1",
	}

	t.Run("PR-route deferral authorizes publish on the PR route", func(t *testing.T) {
		receipt := base
		receipt.Route = WorktreeMergeRouteDecision{Route: WorktreeMergeRoutePullRequest}
		receipt.Validation = quality.VerificationReport{Status: quality.StatusSkipped}
		receipt.ValidationDeferral = &WorktreeMergeValidationDeferral{Route: WorktreeMergeRoutePullRequest, CandidateSHA: receipt.Candidate.SHA}
		if err := requireWorktreeMergePublishedValidation(receipt); err != nil {
			t.Fatalf("PR-route deferral was refused: %v", err)
		}
	})

	t.Run("same deferral does not authorize a call resolved as direct (B1)", func(t *testing.T) {
		receipt := base
		receipt.Route = WorktreeMergeRouteDecision{Route: WorktreeMergeRouteDirect}
		receipt.Validation = quality.VerificationReport{Status: quality.StatusSkipped}
		receipt.ValidationDeferral = &WorktreeMergeValidationDeferral{Route: WorktreeMergeRoutePullRequest, CandidateSHA: receipt.Candidate.SHA}
		if err := requireWorktreeMergePublishedValidation(receipt); err == nil {
			t.Fatal("a route this call resolved as direct was authorized to publish by a stale PR-route deferral")
		}
	})

	t.Run("already-published-at-current-SHA carve-out requires the PR route too", func(t *testing.T) {
		receipt := base
		receipt.Route = WorktreeMergeRouteDecision{Route: WorktreeMergeRouteDirect}
		receipt.PublishedCandidateSHA = receipt.Candidate.SHA
		receipt.Status = WorktreeMergePrepared
		receipt.Validation = quality.VerificationReport{Status: quality.StatusPassed}
		if err := requireWorktreeMergePublishedValidation(receipt); err == nil {
			t.Fatal("already-published carve-out fired for a call resolved as direct")
		}
	})

	t.Run("already-published-at-current-SHA carve-out still works on the PR route", func(t *testing.T) {
		receipt := base
		receipt.Route = WorktreeMergeRouteDecision{Route: WorktreeMergeRoutePullRequest}
		receipt.PublishedCandidateSHA = receipt.Candidate.SHA
		receipt.Status = WorktreeMergePrepared
		receipt.Validation = quality.VerificationReport{Status: quality.StatusPassed}
		if err := requireWorktreeMergePublishedValidation(receipt); err != nil {
			t.Fatalf("already-published carve-out was refused on the PR route: %v", err)
		}
	})

	t.Run("a validated exact identity still authorizes publish regardless of route", func(t *testing.T) {
		receipt := base
		receipt.Route = WorktreeMergeRouteDecision{Route: WorktreeMergeRouteDirect}
		receipt.Status = WorktreeMergePrepared
		receipt.Validation = quality.VerificationReport{Status: quality.StatusPassed, Revision: receipt.Candidate.SHA}
		receipt.ValidationIdentity = &WorktreeMergeValidationIdentity{CandidateSHA: receipt.Candidate.SHA}
		if err := requireWorktreeMergePublishedValidation(receipt); err != nil {
			t.Fatalf("genuinely validated exact identity was refused: %v", err)
		}
	})
}

// TestPreparedValidationStillValidAcceptsMatchingPRRouteDeferral covers M7's
// requirement that preparedValidationStillValid (the stop-before-merge reuse
// check) accepts an exact matching PR-route deferral instead of forcing a
// pointless local re-validation.
func TestPreparedValidationStillValidAcceptsMatchingPRRouteDeferral(t *testing.T) {
	receipt := WorktreeMergeReceipt{
		Status:    WorktreeMergePrepared,
		Candidate: WorktreeMergeCandidate{SHA: "dddddddddddddddddddddddddddddddddddddddd"},
		Route:     WorktreeMergeRouteDecision{Route: WorktreeMergeRoutePullRequest},
		Validation: quality.VerificationReport{
			Status: quality.StatusSkipped, Revision: "dddddddddddddddddddddddddddddddddddddddd",
		},
		ValidationDeferral: &WorktreeMergeValidationDeferral{
			Route: WorktreeMergeRoutePullRequest, CandidateSHA: "dddddddddddddddddddddddddddddddddddddddd",
		},
	}
	reusable, err := preparedValidationStillValid(receipt)
	if err != nil || !reusable {
		t.Fatalf("preparedValidationStillValid(deferred receipt) = (%t, %v), want (true, nil)", reusable, err)
	}

	// A deferral for a different candidate SHA (stale) must not be reused.
	stale := receipt
	stale.Candidate.SHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	reusable, err = preparedValidationStillValid(stale)
	if err != nil || reusable {
		t.Fatalf("preparedValidationStillValid(stale deferred receipt) = (%t, %v), want (false, nil)", reusable, err)
	}

	// A deferral recorded under a route this call did not resolve as pr must
	// not be reused either.
	direct := receipt
	direct.Route = WorktreeMergeRouteDecision{Route: WorktreeMergeRouteDirect}
	reusable, err = preparedValidationStillValid(direct)
	if err != nil || reusable {
		t.Fatalf("preparedValidationStillValid(direct-route call, PR deferral) = (%t, %v), want (false, nil)", reusable, err)
	}
}

// TestApplyOrDeferWorktreeMergeValidationRecordsDeferralWithoutRunningLocally
// covers finding M7's actual single choke point directly: every one of
// LandWorktreeMerge's validation sites (preparing, validation_failed,
// conflict advance, refreshed, rebased, stop-before-merge, resumed, and
// PrepareWorktreeMerge's own standalone validation) calls through
// applyOrDeferWorktreeMergeValidation rather than validateWorktreeMergeCandidate
// directly, so exercising the choke point itself proves the deferral is
// honored at every site without needing eight separate multi-minute
// integration fixtures per call site.
func TestApplyOrDeferWorktreeMergeValidationRecordsDeferralWithoutRunningLocally(t *testing.T) {
	receipt := WorktreeMergeReceipt{
		Repository: "acme/app",
		Candidate:  WorktreeMergeCandidate{SHA: strings.Repeat("f", 40)},
	}
	plan := worktreeMergeValidationPlan{
		Route: WorktreeMergeRouteDecision{Route: WorktreeMergeRoutePullRequest, Requested: WorktreeMergeRouteAuto},
		Defer: true, Reason: "test-authoritative-fenced-policy",
	}
	// Timeout/retry/check knobs are irrelevant on the defer path — passing
	// zero values and, notably, never touching the filesystem or spawning any
	// process proves it never falls through to validateWorktreeMergeCandidate.
	if err := applyOrDeferWorktreeMergeValidation(context.Background(), &receipt, plan, 0, 0, 0, 0, nil); err != nil {
		t.Fatalf("applyOrDeferWorktreeMergeValidation(defer) = %v, want nil", err)
	}
	if receipt.Validation.Status != quality.StatusSkipped {
		t.Fatalf("validation.status = %s, want skipped", receipt.Validation.Status)
	}
	if receipt.ValidationDeferral == nil || receipt.ValidationDeferral.Route != WorktreeMergeRoutePullRequest ||
		receipt.ValidationDeferral.CandidateSHA != receipt.Candidate.SHA || receipt.ValidationDeferral.Reason != plan.Reason {
		t.Fatalf("validation_deferral = %+v, want route=pr candidate_sha=%s reason=%q", receipt.ValidationDeferral, receipt.Candidate.SHA, plan.Reason)
	}
	if receipt.ValidationIdentity != nil {
		t.Fatalf("deferred validation unexpectedly recorded a validation identity: %+v", receipt.ValidationIdentity)
	}
	if receipt.BaselineValidation.Status != "" {
		t.Fatalf("deferred validation unexpectedly recorded a baseline validation: %+v", receipt.BaselineValidation)
	}
}
