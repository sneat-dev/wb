package orchestrate

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver/testfixture"
)

func TestMergeValidationPlanRejectsUnknownRouteBeforeObservation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	decision, err := ResolveWorktreeMergeRoute(ctx, "", "", "invalid")
	if err == nil || err.Error() != `unsupported merge route "invalid"` || decision != (WorktreeMergeRouteDecision{}) {
		t.Fatalf("invalid route = %#v, %v", decision, err)
	}
	plan, err := resolveWorktreeMergeValidationPlan(ctx, "", "", "invalid", false, false)
	if err == nil || err.Error() != `unsupported merge route "invalid"` || !reflect.DeepEqual(plan, worktreeMergeValidationPlan{}) {
		t.Fatalf("invalid plan = %#v, %v", plan, err)
	}
}

func TestMergeValidationPlanHolderCachesFirstErrorAndPlan(t *testing.T) {
	t.Parallel()
	var holder worktreeMergeValidationPlanHolder
	first, firstErr := holder.resolve(context.Background(), "", "", "invalid", false, false)
	if firstErr == nil || !holder.resolved {
		t.Fatalf("first resolve = %#v, %v", first, firstErr)
	}
	// Different, valid arguments must not cause a second observation, or replace
	// the first error with a new result. This is a per-call holder, not shared state.
	second, secondErr := holder.resolve(context.Background(), "acme/app", "main", WorktreeMergeRouteAuto, true, true, "17")
	if secondErr != firstErr || !reflect.DeepEqual(second, first) || holder.err != firstErr {
		t.Fatalf("cached resolve = %#v, %v; first = %#v, %v", second, secondErr, first, firstErr)
	}
}

//nolint:paralleltest // The genuine gh observation fixture mutates PATH and XDG_STATE_HOME; subcases are sequential.
func TestMergeRoutePreservesAuthoritativeAndConservativePolicy(t *testing.T) {
	for _, tt := range []struct {
		name, branch, rules string
		requested, want     WorktreeMergeRoute
		reason, failure     string
		missingGH           bool
	}{
		{name: "default auto is direct only when unprotected and unruled", branch: `{"protected":false,"protection":{}}`, rules: `[]`, want: WorktreeMergeRouteDirect, reason: "authoritatively unprotected"},
		{name: "explicit direct", branch: `{"protected":false,"protection":{}}`, rules: `[]`, requested: WorktreeMergeRouteDirect, want: WorktreeMergeRouteDirect, reason: "explicit direct route"},
		{name: "explicit PR", branch: `{"protected":false,"protection":{}}`, rules: `[]`, requested: WorktreeMergeRoutePullRequest, want: WorktreeMergeRoutePullRequest, reason: "explicit pull-request route"},
		{name: "protected auto", branch: `{"protected":true,"protection":{}}`, rules: `[]`, want: WorktreeMergeRoutePullRequest, reason: "target protection"},
		{name: "review policy", branch: `{"protected":false,"protection":{"required_pull_request_reviews":{}}}`, rules: `[]`, want: WorktreeMergeRoutePullRequest},
		{name: "PR rule", branch: `{"protected":false,"protection":{}}`, rules: `[{"type":"pull_request"}]`, want: WorktreeMergeRoutePullRequest},
		{name: "known non-PR rule remains conservative", branch: `{"protected":false,"protection":{"required_pull_request_reviews":null}}`, rules: `[{"type":"required_status_checks"}]`, want: WorktreeMergeRoutePullRequest},
		{name: "unknown rule", branch: `{"protected":false,"protection":{}}`, rules: `[{"type":"future_rule"}]`, want: WorktreeMergeRoutePullRequest},
		{name: "direct refuses active rules", branch: `{"protected":false,"protection":{}}`, rules: `[{"type":"creation"}]`, requested: WorktreeMergeRouteDirect, failure: "direct route is not authoritatively permitted by target policy"},
		{name: "merge queue precedes explicit PR", branch: `{"protected":true,"protection":{}}`, rules: `[{"type":"merge_queue"}]`, requested: WorktreeMergeRoutePullRequest, want: WorktreeMergeRouteUnsupported, reason: "merge queue"},
		{name: "malformed branch", branch: `{`, rules: `[]`, want: WorktreeMergeRoutePullRequest, reason: "incomplete"},
		{name: "missing protected field", branch: `{}`, rules: `[]`, want: WorktreeMergeRoutePullRequest, reason: "incomplete"},
		{name: "malformed active rules", branch: `{"protected":false,"protection":{}}`, rules: `{`, want: WorktreeMergeRoutePullRequest, reason: "active target rules are unavailable"},
		{name: "unavailable policy conservatively selects PR", missingGH: true, want: WorktreeMergeRoutePullRequest, reason: "target branch policy is unavailable"},
		{name: "unavailable direct policy refuses", missingGH: true, requested: WorktreeMergeRouteDirect, want: WorktreeMergeRoutePullRequest, failure: "direct route is not authoritatively permitted:"},
	} {
		//nolint:paralleltest // Process-wide environment changes in TestMergeRoutePreservesAuthoritativeAndConservativePolicy, installWorktreeMergeDeferralGH; these rows share their parent environment and remain sequential.
		t.Run(tt.name, func(t *testing.T) {
			if tt.missingGH {
				t.Setenv("PATH", t.TempDir())
				t.Setenv("XDG_STATE_HOME", t.TempDir())
			} else {
				installWorktreeMergeDeferralGH(t, tt.branch, `{}`, tt.rules)
			}
			got, err := ResolveWorktreeMergeRoute(context.Background(), "acme/app", "main", tt.requested)
			requested := tt.requested
			if requested == "" {
				requested = WorktreeMergeRouteAuto
			}
			if got.Requested != requested || got.Route != tt.want {
				t.Fatalf("route = %#v, %v; want requested=%q route=%q", got, err, requested, tt.want)
			}
			if tt.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tt.failure) {
					t.Fatalf("error = %v; want %q", err, tt.failure)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got.Reason, tt.reason) {
				t.Fatalf("reason = %q; want %q", got.Reason, tt.reason)
			}
		})
	}
}

//nolint:paralleltest // The real subprocess observation fixture mutates PATH, XDG_STATE_HOME and scripted remote-response environment.
func TestMergeValidationPlanDirectCIInputsAndPartialRefusals(t *testing.T) {
	for _, tt := range []struct {
		name            string
		requested       WorktreeMergeRoute
		local, unfenced bool
	}{
		{name: "automatic requested route", requested: WorktreeMergeRouteAuto},
		{name: "explicit PR requested route", requested: WorktreeMergeRoutePullRequest},
		{name: "force local", requested: WorktreeMergeRouteDirect, local: true},
		{name: "allow unfenced", requested: WorktreeMergeRouteDirect, unfenced: true},
	} {
		//nolint:paralleltest // Process-wide environment changes in installDirectCITestGH; these rows share their parent environment and remain sequential.
		t.Run(tt.name, func(t *testing.T) {
			testfixture.InstallDirectCIGH(t)
			plan, err := resolveWorktreeMergeValidationPlan(context.Background(), "acme/app", "integration", tt.requested, tt.local, tt.unfenced, "17")
			want := WorktreeMergeRouteDirect
			if tt.requested == WorktreeMergeRoutePullRequest {
				want = WorktreeMergeRoutePullRequest
			}
			if err == nil || err.Error() != "direct CI deferral requires --route direct without --validate-locally or --allow-unfenced" || plan.Route.Route != want || plan.Defer || plan.DirectCI != nil {
				t.Fatalf("partial refusal = %#v, %v", plan, err)
			}
		})
	}
	//nolint:paralleltest // Process-wide environment changes in installDirectCITestGH; these rows share their parent environment and remain sequential.
	t.Run("only first optional argument is considered", func(t *testing.T) {
		testfixture.InstallDirectCIGH(t)
		plan, err := resolveWorktreeMergeValidationPlan(context.Background(), "acme/app", "integration", WorktreeMergeRouteDirect, false, false, "  ", "17")
		if err != nil || plan.Defer || plan.DirectCI != nil || plan.Route.Route != WorktreeMergeRouteDirect {
			t.Fatalf("blank first argument = %#v, %v", plan, err)
		}
		plan, err = resolveWorktreeMergeValidationPlan(context.Background(), "acme/app", "integration", WorktreeMergeRouteDirect, false, false, "17", "not-a-PR")
		if err != nil || !plan.Defer || plan.DirectCI == nil || plan.DirectCI.PullRequest != "17" {
			t.Fatalf("nonblank first argument = %#v, %v", plan, err)
		}
	})
	//nolint:paralleltest // Process-wide environment changes in installDirectCITestGH; these rows share their parent environment and remain sequential.
	t.Run("contract error retains resolved route", func(t *testing.T) {
		testfixture.InstallDirectCIGH(t)
		plan, err := resolveWorktreeMergeValidationPlan(context.Background(), "acme/app", "integration", WorktreeMergeRouteDirect, false, false, "not-a-PR")
		if err == nil || !strings.Contains(err.Error(), "direct CI deferral cannot prove open head PR and workflow:") || plan.Route.Route != WorktreeMergeRouteDirect || plan.Defer || plan.DirectCI != nil {
			t.Fatalf("contract refusal = %#v, %v", plan, err)
		}
	})
}
