package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"
)

// orchCovAnswer runs one read against a fake gh that prints the named body.
func orchCovAnswer(t *testing.T, body, exit string) {
	t.Helper()
	state := orchCovScriptState(t, orchCovOneEndpointScript)
	state.answer(t, "body", body)
	state.answer(t, "exit", exit)
}

func TestOrchCovWaitForPullRequestChecksRequiresAPullRequest(t *testing.T) {
	t.Parallel()
	if _, err := WaitForPullRequestChecks(context.Background(), PullRequestWaitOptions{}); err == nil ||
		!strings.Contains(err.Error(), "pull request is required") {
		t.Fatalf("error = %v", err)
	}
	// A complete identity delegates to the exact-commit waiter, so the
	// missing poll interval is reported by that waiter and not silently
	// defaulted here.
	if _, err := WaitForPullRequestChecks(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", PullRequest: "7", Target: "main", Head: strings.Repeat("a", 40),
	}); err == nil || !strings.Contains(err.Error(), "check wait slice must be positive") {
		t.Fatalf("delegated error = %v", err)
	}
}

func TestOrchCovCommitStatusesNamesEveryObservedState(t *testing.T) {
	orchCovAnswer(t, `{"total_count":3,"statuses":[`+
		`{"context":"ci/old","state":"failure","target_url":"https://example.test/old"},`+
		`{"context":"ci/old","state":"success","target_url":"https://example.test/new"},`+
		`{"context":"ci/live","state":"pending","target_url":"https://example.test/live"},`+
		`{"context":"","state":"success"}]}`, "0")
	if _, _, reason := commitStatuses(context.Background(), PullRequestWaitOptions{Repository: "acme/app", Head: "aaaa"}); !strings.Contains(reason, "has no context") {
		t.Fatalf("contextless status reason = %q", reason)
	}

	orchCovAnswer(t, `{"total_count":4,"statuses":[`+
		`{"context":"ci/old","state":"success"},`+
		`{"context":"ci/live","state":"pending"}]}`, "0")
	if _, _, reason := commitStatuses(context.Background(), PullRequestWaitOptions{Repository: "acme/app", Head: "aaaa"}); !strings.Contains(reason, "refusing an incomplete CI receipt") {
		t.Fatalf("incomplete status receipt reason = %q", reason)
	}

	// GitHub returns the newest status first; a later duplicate is history.
	orchCovAnswer(t, `{"total_count":2,"statuses":[`+
		`{"context":"ci/old","state":"success","target_url":"https://example.test/new"},`+
		`{"context":"ci/old","state":"failure","target_url":"https://example.test/old"},`+
		`{"context":"ci/live","state":"pending","target_url":"https://example.test/live"}]}`, "0")
	checks, pending, reason := commitStatuses(context.Background(), PullRequestWaitOptions{Repository: "acme/app", Head: "aaaa"})
	if reason != "" || !pending {
		t.Fatalf("statuses pending=%t reason=%q", pending, reason)
	}
	// The newest observation wins; the older duplicate must not overrule it.
	if len(checks) != 2 || checks[0].Name != "status:ci/old" || checks[0].Bucket != "pass" ||
		checks[0].Link != "https://example.test/new" || checks[1].Name != "status:ci/live" || checks[1].Bucket != "pending" {
		t.Fatalf("statuses = %+v", checks)
	}

	orchCovAnswer(t, "not json", "0")
	if _, _, reason := commitStatuses(context.Background(), PullRequestWaitOptions{Repository: "acme/app", Head: "aaaa"}); !strings.Contains(reason, "decode GitHub commit statuses") {
		t.Fatalf("undecodable statuses reason = %q", reason)
	}
	orchCovInstallGH(t, orchCovNotFound)
	if _, _, reason := commitStatuses(context.Background(), PullRequestWaitOptions{Repository: "acme/app", Head: "aaaa"}); reason == "" {
		t.Fatal("an unreadable statuses endpoint reported no reason")
	}
}

func TestOrchCovCandidateContainsTargetNamesEveryComparisonOutcome(t *testing.T) {
	const target = "0123456789abcdef0123456789abcdef01234567"
	const candidate = "fedcba9876543210fedcba9876543210fedcba98"
	for _, test := range []struct {
		name   string
		body   string
		want   bool
		wantIn string
	}{
		{name: "ahead", body: `{"status":"ahead","base_commit":{"sha":"` + target + `"},"merge_base_commit":{"sha":"` + target + `"}}`, want: true},
		{name: "identical", body: `{"status":"identical","base_commit":{"sha":"` + target + `"},"merge_base_commit":{"sha":"` + target + `"}}`, want: true},
		{name: "behind", body: `{"status":"behind","base_commit":{"sha":"` + target + `"},"merge_base_commit":{"sha":"` + target + `"}}`},
		{name: "diverged", body: `{"status":"diverged","base_commit":{"sha":"` + target + `"},"merge_base_commit":{"sha":"` + target + `"}}`},
		{name: "unsupported status", body: `{"status":"unknown","base_commit":{"sha":"` + target + `"},"merge_base_commit":{"sha":"` + target + `"}}`, wantIn: "unsupported status"},
		{name: "missing base", body: `{"status":"ahead","base_commit":{"sha":""},"merge_base_commit":{"sha":"` + target + `"}}`, wantIn: "omitted base or merge-base SHA"},
		{name: "missing merge base", body: `{"status":"ahead","base_commit":{"sha":"` + target + `"},"merge_base_commit":{"sha":""}}`, wantIn: "omitted base or merge-base SHA"},
		{name: "wrong base", body: `{"status":"ahead","base_commit":{"sha":"` + candidate + `"},"merge_base_commit":{"sha":"` + target + `"}}`, wantIn: "want exact target"},
		{name: "wrong merge base", body: `{"status":"ahead","base_commit":{"sha":"` + target + `"},"merge_base_commit":{"sha":"` + candidate + `"}}`, wantIn: "did not use target"},
		{name: "undecodable", body: "not json", wantIn: "decode candidate ancestry"},
	} {
		t.Run(test.name, func(t *testing.T) {
			orchCovAnswer(t, test.body, "0")
			contains, reason := candidateContainsTarget(context.Background(), "acme/app", target, candidate)
			if contains != test.want {
				t.Fatalf("contains = %t, want %t (reason %q)", contains, test.want, reason)
			}
			if test.wantIn != "" && !strings.Contains(reason, test.wantIn) {
				t.Fatalf("reason = %q, want %q", reason, test.wantIn)
			}
			// "behind" and "diverged" are ordinary answers, not failures: the
			// candidate simply does not contain the target yet.
			if test.wantIn == "" && !test.want && test.name != "behind" && test.name != "diverged" && reason != "" {
				t.Fatalf("unexpected reason %q", reason)
			}
			if (test.name == "behind" || test.name == "diverged") && reason != "" {
				t.Fatalf("%s carried a reason: %q", test.name, reason)
			}
		})
	}
	orchCovInstallGH(t, orchCovNotFound)
	if _, reason := candidateContainsTarget(context.Background(), "acme/app", target, candidate); !strings.Contains(reason, "prove candidate ancestry") {
		t.Fatalf("unreadable comparison reason = %q", reason)
	}
}

func TestOrchCovMissingRequiredChecksLabelsProducerPinnedExpectations(t *testing.T) {
	t.Parallel()
	checks := []RemoteCheck{
		{Name: "check-run:CI", Bucket: "fail", AppID: 42},
		{Name: "status:legacy", Bucket: "pass", AppID: 0},
		{Name: "  ", Bucket: "pass"},
	}
	for _, test := range []struct {
		name     string
		required []RequiredRemoteCheck
		want     []string
	}{
		{name: "satisfied unpinned", required: []RequiredRemoteCheck{{Name: "legacy"}}},
		{name: "pinned producer mismatch", required: []RequiredRemoteCheck{{Name: "CI", IntegrationID: 7}}, want: []string{"CI (GitHub App 7)"}},
		{name: "pinned producer match", required: []RequiredRemoteCheck{{Name: "CI", IntegrationID: 42}}},
		{name: "absent", required: []RequiredRemoteCheck{{Name: "Deploy"}}, want: []string{"Deploy"}},
		{
			name:     "mixed",
			required: []RequiredRemoteCheck{{Name: "legacy"}, {Name: "Deploy"}, {Name: "CI", IntegrationID: 7}},
			want:     []string{"Deploy", "CI (GitHub App 7)"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := missingRequiredChecks(checks, test.required)
			if strings.Join(got, ",") != strings.Join(test.want, ",") {
				t.Fatalf("missing = %v, want %v", got, test.want)
			}
		})
	}
}

func TestOrchCovAddRequiredCheckRefusesAnEmptyContext(t *testing.T) {
	t.Parallel()
	required := map[string]RequiredRemoteCheck{}
	if reason := addRequiredCheck(required, "  ", 0); !strings.Contains(reason, "no context") {
		t.Fatalf("empty context reason = %q", reason)
	}
	if reason := addRequiredCheck(required, "CI", 42); reason != "" || required["CI\x0042"].IntegrationID != 42 {
		t.Fatalf("pinned check = %v, reason %q", required, reason)
	}
}

func TestOrchCovAddRequiredExpectationPrefersAProducerPinnedRule(t *testing.T) {
	t.Parallel()
	required := map[string]RequiredRemoteCheck{}
	addRequiredExpectation(required, RequiredRemoteCheck{Name: "CI"})
	addRequiredExpectation(required, RequiredRemoteCheck{Name: "CI", IntegrationID: 42})
	if len(required) != 1 || required["CI\x0042"].IntegrationID != 42 {
		t.Fatalf("pinned rule did not replace the unpinned one: %v", required)
	}
	// An unpinned duplicate must not overrule the pinned expectation.
	addRequiredExpectation(required, RequiredRemoteCheck{Name: "CI"})
	if len(required) != 1 || required["CI\x0042"].IntegrationID != 42 {
		t.Fatalf("unpinned rule overruled the pinned one: %v", required)
	}
}

func TestOrchCovSortRemoteAndRequiredChecksAreDeterministic(t *testing.T) {
	t.Parallel()
	checks := []RemoteCheck{
		{Name: "b", Bucket: "pass", Link: "z", AppID: 2},
		{Name: "b", Bucket: "pass", Link: "y", AppID: 9},
		{Name: "b", Bucket: "fail", Link: "a", AppID: 1},
		{Name: "a", Bucket: "pass"},
	}
	sortRemoteChecks(checks)
	got := make([]string, 0, len(checks))
	for _, check := range checks {
		got = append(got, check.Name+"/"+check.Bucket+"/"+check.Link)
	}
	if strings.Join(got, ",") != "a/pass/,b/fail/a,b/pass/y,b/pass/z" {
		t.Fatalf("sorted remote checks = %v", got)
	}

	required := []RequiredRemoteCheck{{Name: "b", IntegrationID: 2}, {Name: "b", IntegrationID: 1}, {Name: "a"}}
	sortRequiredChecks(required)
	if required[0].Name != "a" || required[1].IntegrationID != 1 || required[2].IntegrationID != 2 {
		t.Fatalf("sorted required checks = %+v", required)
	}
}

func TestOrchCovLatestActionsRunMatchesTheExactIdentity(t *testing.T) {
	t.Parallel()
	runs := []githubActionsRun{
		{ID: 1, WorkflowID: 10, Event: "pull_request", CheckSuiteID: 100, CreatedAt: time.Now()},
		{ID: 2, WorkflowID: 20, Event: "push", CheckSuiteID: 200, CreatedAt: time.Now()},
	}
	identity := githubActionsRunIdentity{WorkflowID: 20, Event: "push"}
	if got := latestActionsRun(runs, identity); got.ID != 2 {
		t.Fatalf("latestActionsRun = %+v", got)
	}
	if got := latestActionsRun(runs, githubActionsRunIdentity{WorkflowID: 99, Event: "push"}); got.ID != 0 {
		t.Fatalf("unmatched identity returned %+v", got)
	}
}

func TestOrchCovPullRequestIdentityRequiresAnExactHeadAndTarget(t *testing.T) {
	orchCovAnswer(t, `{"number":7,"state":"open","head":{"sha":"aaaa"},"base":{"ref":"main"}}`, "0")
	head, base, reason := pullRequestIdentity(context.Background(), "acme/app", "7")
	if reason != "" || head != "aaaa" || base != "main" {
		t.Fatalf("identity = %q/%q reason=%q", head, base, reason)
	}
	orchCovAnswer(t, `{"number":7,"state":"open","head":{"sha":"aaaa"},"base":{"ref":""}}`, "0")
	if _, _, reason := pullRequestIdentity(context.Background(), "acme/app", "7"); !strings.Contains(reason, "no exact head or target") {
		t.Fatalf("incomplete identity reason = %q", reason)
	}
	orchCovInstallGH(t, orchCovNotFound)
	if _, _, reason := pullRequestIdentity(context.Background(), "acme/app", "7"); reason == "" {
		t.Fatal("an unreadable pull request reported no reason")
	}
}

func TestOrchCovTargetHeadReadsTheExactRef(t *testing.T) {
	orchCovAnswer(t, `{"object":{"sha":"0123456789abcdef"}}`, "0")
	head, reason := targetHead(context.Background(), "acme/app", "main")
	if reason != "" || head != "0123456789abcdef" {
		t.Fatalf("target head = %q reason=%q", head, reason)
	}
	orchCovAnswer(t, `{"object":{"sha":""}}`, "0")
	if _, reason := targetHead(context.Background(), "acme/app", "main"); !strings.Contains(reason, "returned no SHA") {
		t.Fatalf("missing SHA reason = %q", reason)
	}
	orchCovAnswer(t, "not json", "0")
	if _, reason := targetHead(context.Background(), "acme/app", "main"); !strings.Contains(reason, "decode target ref") {
		t.Fatalf("undecodable ref reason = %q", reason)
	}
	orchCovInstallGH(t, orchCovNotFound)
	if _, reason := targetHead(context.Background(), "acme/app", "main"); reason == "" {
		t.Fatal("an unreadable target ref reported no reason")
	}
}

// orchCovActionsScript answers the exact-head Actions-run read with either a
// body from the state directory or an authoritative 404. It names the endpoint
// itself so the shared fixture does not install its own empty answer for it.
const orchCovActionsScript = `#!/bin/sh
S="$ORCHCOV_GH_STATE"
if [ "$1" = api ]; then
  case "$2" in
    */actions/runs?head_sha=*) : ;;
  esac
  if [ -f "$S/not-found" ]; then
    printf 'HTTP/2.0 404 Not Found\nContent-Type: application/json\n\n{"message":"Not Found"}\n'
    exit 1
  fi
  cat "$S/body"
  exit "$(cat "$S/exit")"
fi
echo "unexpected gh args: $*" >&2
exit 30
`

func TestOrchCovGitHubActionsRunsForHeadFailsClosedOnMalformedIdentity(t *testing.T) {
	options := PullRequestWaitOptions{Repository: "acme/app", Head: "aaaa"}
	answer := func(body string) {
		t.Helper()
		state := orchCovScriptState(t, orchCovActionsScript)
		state.answer(t, "body", body)
		state.answer(t, "exit", "0")
	}
	for _, test := range []struct {
		name   string
		body   string
		wantIn string
	}{
		{name: "undecodable", body: "not json", wantIn: "decode GitHub Actions runs"},
		{
			name:   "incomplete receipt",
			body:   `{"total_count":2,"workflow_runs":[{"id":1,"workflow_id":10,"event":"push","created_at":"2026-09-01T00:00:00Z","check_suite_id":100}]}`,
			wantIn: "only 1 of 2",
		},
		{
			name:   "missing run identity",
			body:   `{"total_count":1,"workflow_runs":[{"id":1,"workflow_id":0,"event":"push","created_at":"2026-09-01T00:00:00Z","check_suite_id":100}]}`,
			wantIn: "malformed exact-head workflow-run identity",
		},
		{
			name: "conflicting suite",
			body: `{"total_count":2,"workflow_runs":[` +
				`{"id":1,"workflow_id":10,"event":"push","created_at":"2026-09-01T00:00:00Z","check_suite_id":100},` +
				`{"id":2,"workflow_id":10,"event":"push","created_at":"2026-09-01T00:00:01Z","check_suite_id":100}]}`,
			wantIn: "maps to conflicting workflow runs",
		},
		{
			name: "successful receipt",
			body: `{"total_count":1,"workflow_runs":[` +
				`{"id":1,"workflow_id":10,"event":"push","status":"completed","conclusion":"success","created_at":"2026-09-01T00:00:00Z","check_suite_id":100}]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			answer(test.body)
			_, _, reason := githubActionsRunsForHead(context.Background(), options)
			if test.wantIn == "" {
				if reason != "" {
					t.Fatalf("valid receipt reason = %q", reason)
				}
				return
			}
			if !strings.Contains(reason, test.wantIn) {
				t.Fatalf("reason = %q, want %q", reason, test.wantIn)
			}
		})
	}

	state := orchCovScriptState(t, orchCovActionsScript)
	state.answer(t, "body", `{"total_count":0,"workflow_runs":[]}`)
	state.answer(t, "exit", "0")
	state.answer(t, "not-found", "1")
	if _, _, reason := githubActionsRunsForHead(context.Background(), options); reason == "" {
		t.Fatal("an unreadable Actions endpoint reported no reason")
	}
}
