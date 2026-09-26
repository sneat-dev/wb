package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/testenv"
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
	if err := testenv.WriteExecutableFile(script, []byte(withEmptyActionsRuns(body)), 0o755); err != nil {
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

// TestPrepareWorktreeMergeRunsLazyHostLoadAdmissionOnlyWhenPlanDoesNotDefer
// is round 4's minor 2 test: cmd/wb's combined command can skip the
// up-front host-load admission check on a cheap prediction that the
// resolved validation plan will defer, and hand PrepareWorktreeMerge a
// lazy fallback (RequireHostLoadAdmission) instead. This proves that
// fallback fires exactly when it must — never when the plan genuinely
// defers, and always (stopping the prepare on refusal, never running local
// validation) when it does not.
func TestPrepareWorktreeMergeRunsLazyHostLoadAdmissionOnlyWhenPlanDoesNotDefer(t *testing.T) {
	t.Run("plan defers: the lazy fallback is never called", func(t *testing.T) {
		fixture := newEngineFixture(t)
		source := createMergeSource(t, fixture, "lazy-admission-defers-source", "feature/lazy-admission-defers", "a.txt", "a\n")
		installWorktreeMergeDeferralGH(t, `{"protected":true,"protection":{"required_pull_request_reviews":{},"required_status_checks":{}}}`,
			`{"strict":true,"contexts":["CI"],"checks":[]}`, `[]`)

		called := false
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
			ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main",
			Model: "test-model", AgentRuntime: "test", Route: WorktreeMergeRoutePullRequest,
			RequireHostLoadAdmission: func() (*WorktreeMergeHostLoadAdmission, error) {
				called = true
				return nil, fmt.Errorf("must not be reached")
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if receipt.ValidationDeferral == nil {
			t.Fatalf("this test requires a deferred receipt: %+v", receipt)
		}
		if called {
			t.Fatal("the lazy host-load admission fallback was called even though the plan deferred")
		}
	})

	t.Run("plan does not defer and admission refuses: prepare fails before validation runs", func(t *testing.T) {
		fixture := newEngineFixture(t)
		source := createMergeSource(t, fixture, "lazy-admission-refuses-source", "feature/lazy-admission-refuses", "a.txt", "a\n")

		refusal := fmt.Errorf("wb worktree merge: host load 9.0 exceeds floor 2.0")
		called := false
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
			ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main",
			Model: "test-model", AgentRuntime: "test", ValidateLocally: true,
			RequireHostLoadAdmission: func() (*WorktreeMergeHostLoadAdmission, error) {
				called = true
				return nil, refusal
			},
		})
		if !called {
			t.Fatal("the lazy host-load admission fallback was never called even though the plan did not defer")
		}
		if err == nil || !strings.Contains(err.Error(), "host load 9.0 exceeds floor") {
			t.Fatalf("err = %v, want the admission refusal", err)
		}
		if receipt.Validation.Status == quality.StatusPassed {
			t.Fatalf("local validation ran despite the admission refusal: %+v", receipt.Validation)
		}
	})

	t.Run("plan does not defer and admission allows: validation runs and records the admission", func(t *testing.T) {
		fixture := newEngineFixture(t)
		source := createMergeSource(t, fixture, "lazy-admission-allows-source", "feature/lazy-admission-allows", "a.txt", "a\n")

		granted := &WorktreeMergeHostLoadAdmission{Load: 1.0, Floor: 2.0}
		called := false
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
			ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main",
			Model: "test-model", AgentRuntime: "test", ValidateLocally: true,
			RequireHostLoadAdmission: func() (*WorktreeMergeHostLoadAdmission, error) {
				called = true
				return granted, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !called {
			t.Fatal("the lazy host-load admission fallback was never called even though the plan did not defer")
		}
		if receipt.Validation.Status != quality.StatusPassed {
			t.Fatalf("validation.status = %s, want passed: %+v", receipt.Validation.Status, receipt.Validation)
		}
		if receipt.HostLoadAdmission == nil || receipt.HostLoadAdmission.Load != granted.Load {
			t.Fatalf("receipt did not record the lazily-obtained admission: %+v", receipt.HostLoadAdmission)
		}
	})
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
		plan := worktreeMergeValidationPlan{Route: receipt.Route, Defer: true}
		if err := requireWorktreeMergePublishedValidationContext(context.Background(), receipt, plan, 0, 0, 0); err != nil {
			t.Fatalf("PR-route deferral was refused: %v", err)
		}
	})

	t.Run("same deferral does not authorize a call resolved as direct (B1)", func(t *testing.T) {
		receipt := base
		receipt.Route = WorktreeMergeRouteDecision{Route: WorktreeMergeRouteDirect}
		receipt.Validation = quality.VerificationReport{Status: quality.StatusSkipped}
		receipt.ValidationDeferral = &WorktreeMergeValidationDeferral{Route: WorktreeMergeRoutePullRequest, CandidateSHA: receipt.Candidate.SHA}
		plan := worktreeMergeValidationPlan{Route: receipt.Route, Defer: false}
		if err := requireWorktreeMergePublishedValidationContext(context.Background(), receipt, plan, 0, 0, 0); err == nil {
			t.Fatal("a route this call resolved as direct was authorized to publish by a stale PR-route deferral")
		}
	})

	t.Run("already-published-at-current-SHA carve-out requires the PR route too", func(t *testing.T) {
		receipt := base
		receipt.Route = WorktreeMergeRouteDecision{Route: WorktreeMergeRouteDirect}
		receipt.PublishedCandidateSHA = receipt.Candidate.SHA
		receipt.Status = WorktreeMergePrepared
		receipt.Validation = quality.VerificationReport{Status: quality.StatusPassed}
		plan := worktreeMergeValidationPlan{Route: receipt.Route, Defer: false}
		if err := requireWorktreeMergePublishedValidationContext(context.Background(), receipt, plan, 0, 0, 0); err == nil {
			t.Fatal("already-published carve-out fired for a call resolved as direct")
		}
	})

	t.Run("already-published-at-current-SHA carve-out still works on the PR route", func(t *testing.T) {
		receipt := base
		receipt.Route = WorktreeMergeRouteDecision{Route: WorktreeMergeRoutePullRequest}
		receipt.PublishedCandidateSHA = receipt.Candidate.SHA
		receipt.Status = WorktreeMergePrepared
		receipt.Validation = quality.VerificationReport{Status: quality.StatusPassed}
		plan := worktreeMergeValidationPlan{Route: receipt.Route, Defer: true}
		if err := requireWorktreeMergePublishedValidationContext(context.Background(), receipt, plan, 0, 0, 0); err != nil {
			t.Fatalf("already-published carve-out was refused on the PR route: %v", err)
		}
	})

	t.Run("a validated exact identity still authorizes publish regardless of route", func(t *testing.T) {
		receipt := base
		receipt.Route = WorktreeMergeRouteDecision{Route: WorktreeMergeRouteDirect}
		receipt.Status = WorktreeMergePrepared
		receipt.Validation = quality.VerificationReport{Status: quality.StatusPassed, Revision: receipt.Candidate.SHA}
		receipt.ValidationIdentity = &WorktreeMergeValidationIdentity{CandidateSHA: receipt.Candidate.SHA}
		plan := worktreeMergeValidationPlan{Route: receipt.Route, Defer: false}
		if err := requireWorktreeMergePublishedValidationContext(context.Background(), receipt, plan, 0, 0, 0); err != nil {
			t.Fatalf("genuinely validated exact identity was refused: %v", err)
		}
	})

	// Finding X1 (red-team follow-up): a receipt whose PR-route deferral was
	// recorded by an earlier call must NOT authorize a publish on THIS call
	// when this call's own plan no longer permits deferring (--allow-unfenced
	// or --validate-locally), even though the route and candidate SHA still
	// match exactly.
	t.Run("X1: a matching PR-route deferral does not authorize publish when this call's plan forbids deferring", func(t *testing.T) {
		receipt := base
		receipt.Route = WorktreeMergeRouteDecision{Route: WorktreeMergeRoutePullRequest}
		receipt.Validation = quality.VerificationReport{Status: quality.StatusSkipped}
		receipt.ValidationDeferral = &WorktreeMergeValidationDeferral{Route: WorktreeMergeRoutePullRequest, CandidateSHA: receipt.Candidate.SHA}
		plan := worktreeMergeValidationPlan{Route: receipt.Route, Defer: false}
		if err := requireWorktreeMergePublishedValidationContext(context.Background(), receipt, plan, 0, 0, 0); err == nil {
			t.Fatal("a stale deferral was accepted even though this call's plan forbids deferring")
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
	plan := worktreeMergeValidationPlan{Route: receipt.Route, Defer: true}
	reusable, err := preparedValidationStillValidContext(context.Background(), receipt, plan, 0, 0, 0)
	if err != nil || !reusable {
		t.Fatalf("preparedValidationStillValidContext(context.Background(), deferred receipt) = (%t, %v, 0, 0, 0), want (true, nil)", reusable, err)
	}

	// A deferral for a different candidate SHA (stale) must not be reused.
	stale := receipt
	stale.Candidate.SHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	reusable, err = preparedValidationStillValidContext(context.Background(), stale, plan, 0, 0, 0)
	if err != nil || reusable {
		t.Fatalf("preparedValidationStillValidContext(context.Background(), stale deferred receipt) = (%t, %v, 0, 0, 0), want (false, nil)", reusable, err)
	}

	// A deferral recorded under a route this call did not resolve as pr must
	// not be reused either.
	direct := receipt
	direct.Route = WorktreeMergeRouteDecision{Route: WorktreeMergeRouteDirect}
	directPlan := worktreeMergeValidationPlan{Route: direct.Route, Defer: false}
	reusable, err = preparedValidationStillValidContext(context.Background(), direct, directPlan, 0, 0, 0)
	if err != nil || reusable {
		t.Fatalf("preparedValidationStillValidContext(context.Background(), direct-route call, PR deferral, 0, 0, 0) = (%t, %v), want (false, nil)", reusable, err)
	}

	// Finding X1: even on the PR route with a matching candidate SHA, a
	// deferral must not be reused when THIS call's own plan forbids
	// deferring (--allow-unfenced or --validate-locally on the resume).
	noLongerDeferring := worktreeMergeValidationPlan{Route: receipt.Route, Defer: false}
	reusable, err = preparedValidationStillValidContext(context.Background(), receipt, noLongerDeferring, 0, 0, 0)
	if err != nil || reusable {
		t.Fatalf("preparedValidationStillValidContext(context.Background(), X1: plan.Defer=false) = (%t, %v, 0, 0, 0), want (false, nil)", reusable, err)
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

// TestResolveWorktreeMergeValidationPlanForcesLocalValidationForAllowUnfencedOrValidateLocally
// covers finding X1's scenarios A, B, and C at the plan-resolution level:
// AllowUnfenced tells ciwait it may accept an unfenced or unreadable policy
// as a merge gate — exactly the guarantee deferral relies on — so a call
// made with either --allow-unfenced (A, B) or --validate-locally (C) must
// never defer, even against an otherwise-eligible authoritative fenced
// policy.
func TestResolveWorktreeMergeValidationPlanForcesLocalValidationForAllowUnfencedOrValidateLocally(t *testing.T) {
	installWorktreeMergeDeferralGH(t, `{"protected":true,"protection":{"required_pull_request_reviews":{},"required_status_checks":{}}}`,
		`{"strict":true,"contexts":["CI"],"checks":[]}`, `[]`)

	baseline, err := resolveWorktreeMergeValidationPlan(context.Background(), "acme/app", "main", WorktreeMergeRoutePullRequest, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !baseline.Defer {
		t.Fatalf("baseline plan (no --allow-unfenced, no --validate-locally) did not defer: %+v", baseline)
	}

	// Scenario A/B: --allow-unfenced (as a flag on this call, or inherited
	// from a previously recorded receipt.AllowUnfenced) must force local
	// validation even against this same eligible policy.
	unfenced, err := resolveWorktreeMergeValidationPlan(context.Background(), "acme/app", "main", WorktreeMergeRoutePullRequest, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if unfenced.Defer {
		t.Fatalf("X1 A/B: --allow-unfenced unexpectedly deferred validation: %+v", unfenced)
	}

	// Scenario C: --validate-locally must force local validation too.
	validateLocally, err := resolveWorktreeMergeValidationPlan(context.Background(), "acme/app", "main", WorktreeMergeRoutePullRequest, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if validateLocally.Defer {
		t.Fatalf("X1 C: --validate-locally unexpectedly deferred validation: %+v", validateLocally)
	}
}

// TestPeekWorktreeMergeValidationDeferralSkipsHostLoadGateWhenDeferring is
// Minor 10's regression test (sneat-dev/wb#591 round 3 red-team follow-up):
// the combined `wb worktree land`/`wb land` used to check host load before
// it could know local validation would be deferred to the pull-request
// route's authoritative CI. PeekWorktreeMergeValidationDeferral resolves
// that cheaply -- source inspection and remote route/policy reads only,
// never a local validation run -- from not-yet-prepared source worktrees,
// so the combined command can skip the host-load gate exactly when the
// plan defers, and must still gate it (return false) when the target's
// required-check policy is not eligible to defer.
func TestPeekWorktreeMergeValidationDeferralSkipsHostLoadGateWhenDeferring(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "peek-defer-source", "feature/peek-defer", "peek-defer.txt", "peek\n")
	installWorktreeMergeDeferralGH(t, `{"protected":true,"protection":{"required_pull_request_reviews":{},"required_status_checks":{}}}`,
		`{"strict":true,"contexts":["CI"],"checks":[]}`, `[]`)

	deferred, err := PeekWorktreeMergeValidationDeferral(context.Background(), fixture.githubDir, []string{source.WorktreeDir}, "main", WorktreeMergeRoutePullRequest, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !deferred {
		t.Fatal("an authoritative, non-empty, server-fenced pull-request-route policy must be reported as deferring, so the combined command can skip the host-load gate")
	}

	// --validate-locally on this call must never report a deferral, so the
	// combined command still gates it on host load.
	forcedLocal, err := PeekWorktreeMergeValidationDeferral(context.Background(), fixture.githubDir, []string{source.WorktreeDir}, "main", WorktreeMergeRoutePullRequest, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if forcedLocal {
		t.Fatal("--validate-locally on this call must never be reported as deferring")
	}

	// --allow-unfenced on this call must never report a deferral either.
	unfenced, err := PeekWorktreeMergeValidationDeferral(context.Background(), fixture.githubDir, []string{source.WorktreeDir}, "main", WorktreeMergeRoutePullRequest, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if unfenced {
		t.Fatal("--allow-unfenced must never be reported as deferring")
	}
}

// TestWorktreeMergeValidationPlanHolderResolvesRouteExactlyOnce is the minor
// finding #3 memo test (sneat-dev/wb#591 red-team follow-up): every
// LandWorktreeMerge validation site and the publish guard share one
// worktreeMergeValidationPlanHolder, and must trigger exactly one real
// network resolution per call no matter how many times resolve is invoked
// on it (M7, B1). One resolution issues exactly five `gh` calls — two for
// ResolveWorktreeMergeRoute (the branch policy summary, then the active
// branch rules) and three for worktreeMergeValidationDeferralEligible's own
// targetBranchRequiredChecks (the branch policy summary again, the classic
// required-status-checks detail, and the active branch rules again); five
// resolve calls issuing anything more than that one resolution's own five
// calls means it re-resolved instead of memoizing.
func TestWorktreeMergeValidationPlanHolderResolvesRouteExactlyOnce(t *testing.T) {
	const callsPerResolution = 5
	bin := t.TempDir()
	counter := filepath.Join(bin, "calls")
	script := "#!/bin/sh\nset -eu\n" +
		"echo \"$*\" >>\"$WB_TEST_CALL_COUNTER\"\n" +
		"case \"$*\" in\n" +
		"  'api repos/acme/app/branches/main --include'|'api repos/acme/app/branches/main') printf '%s\\n' '{\"protected\":true,\"protection\":{\"required_pull_request_reviews\":{},\"required_status_checks\":{}}}' ;;\n" +
		"  'api repos/acme/app/branches/main/protection/required_status_checks --include'|'api repos/acme/app/branches/main/protection/required_status_checks') printf '%s\\n' '{\"strict\":true,\"contexts\":[\"CI\"],\"checks\":[]}' ;;\n" +
		"  'api repos/acme/app/rules/branches/main?per_page=100 --include'|'api repos/acme/app/rules/branches/main?per_page=100') printf '%s\\n' '[]' ;;\n" +
		"  *) echo \"unexpected gh command: $*\" >&2; exit 2 ;;\n" +
		"esac\n"
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(withEmptyActionsRuns(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_CALL_COUNTER", counter)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	holder := &worktreeMergeValidationPlanHolder{}
	for i := 0; i < 5; i++ {
		if _, err := holder.resolve(context.Background(), "acme/app", "main", WorktreeMergeRoutePullRequest, false, false); err != nil {
			t.Fatalf("resolve #%d failed: %v", i, err)
		}
	}
	raw, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	calls := len(strings.Split(strings.TrimSpace(string(raw)), "\n"))
	if calls != callsPerResolution {
		t.Fatalf("gh was invoked %d times across 5 holder.resolve calls, want exactly %d (one resolution): %s", calls, callsPerResolution, string(raw))
	}
}

// TestApplyRecordedWorktreeMergeRouteBeforeFirstResolve is the minor finding
// #1 regression test (sneat-dev/wb#591 red-team follow-up): a receipt that
// already recorded an explicit route earlier in its own history (before
// ever publishing a pull request, so retainWorktreeMergeLandIntent's own
// PullRequest-gated recording never ran) must still have that recorded
// route honored on a resume whose options.Route is left at its "auto"
// default, rather than resolving auto's own policy from scratch.
func TestApplyRecordedWorktreeMergeRouteBeforeFirstResolve(t *testing.T) {
	receipt := WorktreeMergeReceipt{Route: WorktreeMergeRouteDecision{Requested: WorktreeMergeRoutePullRequest}}
	options := WorktreeMergeLandOptions{Route: WorktreeMergeRouteAuto}
	applyRecordedWorktreeMergeRouteBeforeFirstResolve(&receipt, &options)
	if options.Route != WorktreeMergeRoutePullRequest {
		t.Fatalf("options.Route = %q, want the recorded pr route to be applied", options.Route)
	}

	// An explicit non-auto request on THIS call must never be overridden by
	// history.
	explicit := WorktreeMergeReceipt{Route: WorktreeMergeRouteDecision{Requested: WorktreeMergeRoutePullRequest}}
	explicitOptions := WorktreeMergeLandOptions{Route: WorktreeMergeRouteDirect}
	applyRecordedWorktreeMergeRouteBeforeFirstResolve(&explicit, &explicitOptions)
	if explicitOptions.Route != WorktreeMergeRouteDirect {
		t.Fatalf("options.Route = %q, want an explicit --route direct on this call to win over recorded history", explicitOptions.Route)
	}

	// No recorded history leaves options.Route untouched.
	fresh := WorktreeMergeReceipt{}
	freshOptions := WorktreeMergeLandOptions{Route: WorktreeMergeRouteAuto}
	applyRecordedWorktreeMergeRouteBeforeFirstResolve(&fresh, &freshOptions)
	if freshOptions.Route != WorktreeMergeRouteAuto {
		t.Fatalf("options.Route = %q, want auto to remain when there is no recorded history", freshOptions.Route)
	}
}

// TestMissingRequiredChecksTrustsGitHubsOwnSkippedOrNeutralVerdict is round
// 3's replacement for the round-2 finding X2 regression test
// (sneat-dev/wb#591 red-team follow-up): the final red-team pass found the
// strict "requireExecuted" mode itself broken (on sneat-co/sneat-go, a
// required check that legitimately concludes "skipped" on an exact-tree
// reuse made the post-target wait hang forever), so round 3 removes it
// entirely. missingRequiredChecks (reverted to its pre-X2 signature and
// behavior) must treat a registered required check as satisfied by name
// regardless of its conclusion -- including "skipped" and "neutral" -- for
// every caller, deferred or not: GitHub branch protection's own evaluation
// is the gate.
func TestMissingRequiredChecksTrustsGitHubsOwnSkippedOrNeutralVerdict(t *testing.T) {
	required := []RequiredRemoteCheck{{Name: "CI"}}

	skipped := []RemoteCheck{{Name: "check-run:CI", Bucket: "pass", Conclusion: "skipped"}}
	if missing := missingRequiredChecks(skipped, required); len(missing) != 0 {
		t.Fatalf("a registered skipped required check was treated as missing: %v", missing)
	}

	neutral := []RemoteCheck{{Name: "check-run:CI", Bucket: "pass", Conclusion: "neutral"}}
	if missing := missingRequiredChecks(neutral, required); len(missing) != 0 {
		t.Fatalf("a registered neutral required check was treated as missing: %v", missing)
	}

	success := []RemoteCheck{{Name: "check-run:CI", Bucket: "pass", Conclusion: "success"}}
	if missing := missingRequiredChecks(success, required); len(missing) != 0 {
		t.Fatalf("a genuinely successful required check was treated as missing: %v", missing)
	}

	unregistered := []RemoteCheck{{Name: "check-run:OtherCheck", Bucket: "pass", Conclusion: "success"}}
	if missing := missingRequiredChecks(unregistered, required); len(missing) == 0 {
		t.Fatal("a required check that never registered under its own name was not reported missing")
	}
}

// TestCheckRunBucketTreatsNeutralAsPass is Minor 11's regression test
// (sneat-dev/wb#591 round 3 red-team follow-up): round 2's global
// neutral-joins-skipped-in-"skipping" bucket change altered `wb ci wait`
// JSON output and graduation's validateCIWait as an unintended side effect.
// Round 3 reverts it: "neutral" buckets as "pass" again, exactly as before
// round 2, while "skipped" alone still buckets as "skipping".
func TestCheckRunBucketTreatsNeutralAsPass(t *testing.T) {
	if bucket := checkRunBucket("completed", "neutral"); bucket != "pass" {
		t.Fatalf("checkRunBucket(completed, neutral) = %q, want pass", bucket)
	}
	if bucket := checkRunBucket("completed", "skipped"); bucket != "skipping" {
		t.Fatalf("checkRunBucket(completed, skipped) = %q, want skipping", bucket)
	}
	if bucket := checkRunBucket("completed", "success"); bucket != "pass" {
		t.Fatalf("checkRunBucket(completed, success) = %q, want pass", bucket)
	}
}

// TestLandWorktreeMergePullRequestDeferredValidationWaitsOnFakeCI is the
// minor finding #3 end-to-end deferral test (sneat-dev/wb#591 red-team
// follow-up): a candidate prepared on the pull-request route against an
// authoritative, non-empty, server-fenced required-check policy defers
// local validation, and a subsequent land call still publishes, waits on
// (fake) CI through the shared engine, and merges — never silently skipping
// the wait itself just because local validation was deferred.
func TestLandWorktreeMergePullRequestDeferredValidationWaitsOnFakeCI(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "deferred-e2e-source", "feature/deferred-e2e", "deferred-e2e.txt", "e2e\n")
	installWorktreeMergeDeferralGH(t, `{"protected":true,"protection":{"required_pull_request_reviews":{},"required_status_checks":{}}}`,
		`{"strict":true,"contexts":["CI"],"checks":[]}`, `[]`)

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main",
		Model: "test-model", AgentRuntime: "test", Route: WorktreeMergeRoutePullRequest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ValidationDeferral == nil {
		t.Fatalf("prepare did not defer validation, so this test would not exercise the deferred-landing path: %+v", receipt)
	}

	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)

	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("landing a deferred candidate through the shared engine failed: receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded && landed.Status != WorktreeMergeComplete {
		t.Fatalf("landed receipt status = %s, want landed or complete: %+v", landed.Status, landed)
	}
	if landed.Validation.Status != quality.StatusSkipped {
		t.Fatalf("landed receipt lost its deferred validation status: %+v", landed.Validation)
	}
	if landed.ValidationDeferral == nil {
		t.Fatalf("landed receipt lost its validation_deferral: %+v", landed)
	}
	log := gh.ghLog(t)
	if !strings.Contains(log, "api --method PUT repos/acme/app/pulls/41/merge") {
		t.Fatalf("gh log did not show the shared engine actually waiting on and merging the deferred candidate:\n%s", log)
	}
}

// TestLandWorktreeMergePullRequestDeferredValidationSkippedCheckLandsAndRecordsFinding
// is round 3's B1 regression test (sneat-dev/wb#591 red-team follow-up): a
// deferred PR-route land whose exact head's required check concludes
// "skipped" (the real-world sneat-co/sneat-go `strongo_workflow / Lint`
// exact-tree-reuse shape) must still land through both the candidate/PR
// phase and the post-target phase — waitForWorktreeMergeChecks must never
// hang the post-merge wait to checks_pending on a false "have not
// registered" reason — and must record the non-blocking
// deferred-validation-check-skipped finding on the landed receipt.
func TestLandWorktreeMergePullRequestDeferredValidationSkippedCheckLandsAndRecordsFinding(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "deferred-skip-source", "feature/deferred-skip", "deferred-skip.txt", "skip\n")
	installWorktreeMergeDeferralGH(t, `{"protected":true,"protection":{"required_pull_request_reviews":{},"required_status_checks":{}}}`,
		`{"strict":true,"contexts":["CI"],"checks":[]}`, `[]`)

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main",
		Model: "test-model", AgentRuntime: "test", Route: WorktreeMergeRoutePullRequest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ValidationDeferral == nil {
		t.Fatalf("prepare did not defer validation, so this test would not exercise the deferred-landing path: %+v", receipt)
	}

	installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch).writeState(t, "check-conclusion", "skipped")

	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("landing a deferred candidate with a skipped required check failed (B1 regression): receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded && landed.Status != WorktreeMergeComplete {
		t.Fatalf("landed receipt status = %s, want landed or complete (B1: must not hang at checks_pending): %+v", landed.Status, landed)
	}
	found := false
	for _, finding := range landed.Findings {
		if finding.Code == WorktreeMergeFindingDeferredValidationCheckSkipped {
			found = true
			if len(finding.Checks) == 0 {
				t.Fatalf("recorded finding named no checks: %+v", finding)
			}
		}
	}
	if !found {
		t.Fatalf("landed receipt did not record the deferred-validation-check-skipped finding: %+v", landed.Findings)
	}
}

// TestRecordDeferredValidationCheckSkippedFindingReplacesRatherThanAppends
// is round 4's minor 1 unit test: calling the recorder twice for the same
// receipt — exactly what happens across two resume slices that each
// re-observe the same wait's checks from scratch — must leave exactly one
// finding with this code, not two. The second call additionally proves the
// replacement carries the *latest* observation (a second, different skipped
// check), not a stale copy of the first.
func TestRecordDeferredValidationCheckSkippedFindingReplacesRatherThanAppends(t *testing.T) {
	receipt := &WorktreeMergeReceipt{ValidationDeferral: &WorktreeMergeValidationDeferral{}}
	first := PullRequestWaitResult{
		RequiredChecks: []RequiredRemoteCheck{{Name: "CI"}},
		Checks:         []RemoteCheck{{Name: "CI", Conclusion: "skipped"}},
	}
	recordDeferredValidationCheckSkippedFinding(receipt, first)
	if len(receipt.Findings) != 1 {
		t.Fatalf("after one call, findings = %+v, want exactly one", receipt.Findings)
	}
	second := PullRequestWaitResult{
		RequiredChecks: []RequiredRemoteCheck{{Name: "CI"}, {Name: "Lint"}},
		Checks: []RemoteCheck{
			{Name: "CI", Conclusion: "success"},
			{Name: "Lint", Conclusion: "neutral"},
		},
	}
	recordDeferredValidationCheckSkippedFinding(receipt, second)
	if len(receipt.Findings) != 1 {
		t.Fatalf("after two calls, findings = %+v, want still exactly one (replaced, not appended)", receipt.Findings)
	}
	if got := receipt.Findings[0].Checks; len(got) != 1 || got[0] != "Lint" {
		t.Fatalf("replaced finding = %+v, want it to carry the second call's own checks (Lint)", receipt.Findings[0])
	}
}

// TestLandWorktreeMergePullRequestNonDeferredSkippedCheckRecordsNoFinding
// covers the negative case the coordinator asked for directly: a land whose
// candidate was validated locally (never deferred) must never record the
// deferred-validation-check-skipped finding, even when the exact head's
// required check concludes "skipped" — the finding only ever describes a
// deferred candidate's own required-check policy, never an ordinary one.
func TestLandWorktreeMergePullRequestNonDeferredSkippedCheckRecordsNoFinding(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "non-deferred-skip-source", "feature/non-deferred-skip", "non-deferred-skip.txt", "skip\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ValidationDeferral != nil {
		t.Fatalf("this test requires a non-deferred receipt: %+v", receipt)
	}

	installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch).writeState(t, "check-conclusion", "skipped")

	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("landing a non-deferred candidate with a skipped required check failed: receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded && landed.Status != WorktreeMergeComplete {
		t.Fatalf("landed receipt status = %s, want landed or complete: %+v", landed.Status, landed)
	}
	if len(landed.Findings) != 0 {
		t.Fatalf("non-deferred land unexpectedly recorded findings: %+v", landed.Findings)
	}
}

// TestLandWorktreeMergePullRequestDeferredValidationRecordsFindingOnceAcrossTwoResumeSlices
// is round 4's minor 1 integration test: a resume that stops at
// checks_pending (a resume "slice" that observed the wait but did not
// resolve it) must record no deferred-validation-check-skipped finding at
// all — the wait has not resolved to anything worth recording yet — and a
// later resume that lands must record exactly one, not accumulate one per
// slice.
func TestLandWorktreeMergePullRequestDeferredValidationRecordsFindingOnceAcrossTwoResumeSlices(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "two-slices-source", "feature/two-slices", "two-slices.txt", "two\n")
	installWorktreeMergeDeferralGH(t, `{"protected":true,"protection":{"required_pull_request_reviews":{},"required_status_checks":{}}}`,
		`{"strict":true,"contexts":["CI"],"checks":[]}`, `[]`)

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main",
		Model: "test-model", AgentRuntime: "test", Route: WorktreeMergeRoutePullRequest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ValidationDeferral == nil {
		t.Fatalf("prepare did not defer validation, so this test would not exercise the deferred-landing path: %+v", receipt)
	}

	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	gh.writeState(t, "check-conclusion", "")
	gh.writeState(t, "check-conclusion-status", "in_progress")

	// First resume slice: the wait times out while checks are still
	// pending. Nothing has resolved yet, so no finding of any kind may be
	// recorded on this receipt.
	pendingOptions := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	pendingOptions.Timeout = 200 * time.Millisecond
	pending, err := ResumeWorktreeMerge(context.Background(), pendingOptions)
	if err == nil || pending.Status != WorktreeMergeChecksPending {
		t.Fatalf("first resume slice did not stop at checks_pending: receipt=%+v err=%v", pending, err)
	}
	if len(pending.Findings) != 0 {
		t.Fatalf("a checks-pending resume slice recorded a finding before the wait resolved: %+v", pending.Findings)
	}

	// Second resume slice: the required check now concludes "skipped", so
	// this slice both lands and records the finding — exactly once.
	gh.writeState(t, "check-conclusion", "skipped")
	landedOptions := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	landed, err := ResumeWorktreeMerge(context.Background(), landedOptions)
	if err != nil {
		t.Fatalf("second resume slice did not land: receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded && landed.Status != WorktreeMergeComplete {
		t.Fatalf("landed receipt status = %s, want landed or complete: %+v", landed.Status, landed)
	}
	found := 0
	for _, finding := range landed.Findings {
		if finding.Code == WorktreeMergeFindingDeferredValidationCheckSkipped {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("landed receipt recorded the deferred-validation-check-skipped finding %d time(s) across two resume slices, want exactly 1: %+v", found, landed.Findings)
	}
}

// TestAwaitLandablePullRequestSkippedRequiredCheckAlwaysPasses is round 3's
// replacement for the round-2 pair of X2 engine-level tests
// (sneat-dev/wb#591 red-team follow-up). The round-2 "blocks" test only
// passed by exhausting its own wait budget (Minor B2), not by proving
// correctness against real-world GitHub behavior: on sneat-co/sneat-go a
// required check legitimately concludes "skipped" on an exact-tree reuse,
// and GitHub itself still counts it as satisfied and merges. Round 3
// removes the strict RequireExecutedRequiredChecks gate entirely, so there
// is no longer a "deferred" vs. "plain" distinction at this engine level:
// every awaitLandablePullRequest caller must report a skipped required
// check as passing and proceed to merge.
func TestAwaitLandablePullRequestSkippedRequiredCheckAlwaysPasses(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "x2-engine-source", "feature/x2-engine", "x2-engine.txt", "x2\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch).writeState(t, "check-conclusion", "skipped")
	runEngineGit(t, receipt.Candidate.Worktree, "push", fixture.repository.CloneURL, "HEAD:refs/heads/"+receipt.Candidate.Branch)

	view := PullRequestView{Number: 41}
	view.Head.SHA, view.Head.Ref = receipt.Candidate.SHA, receipt.Candidate.Branch
	view.Base.Ref = "main"

	options := PullRequestLandOptions{
		Repository: "acme/app", PullRequest: "41", ProjectsRoot: fixture.githubDir,
		MergeMethod: "merge", MergeMethodExplicit: true,
		NoAutoMerge: true, NoUpdateBranch: true,
		Slice: 3 * time.Second, CheckPollInterval: 100 * time.Millisecond,
	}
	_, waited, _, _, refusal, err := awaitLandablePullRequest(context.Background(), options, view, "41", "x2 subject", "x2 body", map[string]string{})
	if err != nil {
		t.Fatalf("awaitLandablePullRequest returned an unexpected error: %v", err)
	}
	if refusal != nil {
		t.Fatalf("awaitLandablePullRequest unexpectedly refused: %+v", refusal)
	}
	if waited.Status != PullRequestWaitPassed {
		t.Fatalf("a skipped required check must count as passing: %+v", waited)
	}
}
