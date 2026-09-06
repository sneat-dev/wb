package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/runqueue"
)

func TestFleetMergePolicyHelpAndFlags(t *testing.T) {
	command := newFleetMergePolicyCmd()
	for _, name := range []string{"apply", "org", "repo", "user", "parallel", "report-dir", "resume", "format", "json"} {
		if command.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s", name)
		}
	}
	for _, phrase := range []string{"read-only", "required-linear-history", "merge-queue", "never weakens", "organization", "enterprise"} {
		if !strings.Contains(command.Long, phrase) {
			t.Errorf("help does not explain %q", phrase)
		}
	}
	if got := command.Flags().Lookup("apply").DefValue; got != "false" {
		t.Fatalf("--apply default = %s", got)
	}
	if got, want := command.Flags().Lookup("parallel").DefValue, strconv.Itoa(min(runqueue.Budget(), 16)); got != want {
		t.Fatalf("--parallel default = %s, want WB CPU budget %s", got, want)
	}
}

func TestFleetMergePolicyApplyRequiresExplicitScope(t *testing.T) {
	command := newFleetMergePolicyCmd()
	if err := command.Flags().Set("apply", "true"); err != nil {
		t.Fatal(err)
	}
	err := command.RunE(command, nil)
	if err == nil || !strings.Contains(err.Error(), "explicit --org, --repo, or --user") {
		t.Fatalf("error = %v", err)
	}
}

func TestDiscoverRemoteMergePolicyFleetMarksGitHubInventoryRemote(t *testing.T) {
	originalUser, originalOrgs, originalList := mergePolicyAuthUser, mergePolicyMemberOrgs, mergePolicyListRemote
	t.Cleanup(func() {
		mergePolicyAuthUser, mergePolicyMemberOrgs, mergePolicyListRemote = originalUser, originalOrgs, originalList
	})
	mergePolicyAuthUser = func() (string, error) { return "alex", nil }
	mergePolicyMemberOrgs = func() ([]string, error) { return []string{"acme"}, nil }
	mergePolicyListRemote = func(owner string) ([]discover.Repo, error) { return []discover.Repo{{Org: owner, Name: "app"}}, nil }
	repos, err := discoverRemoteMergePolicyFleet("acme/", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || !repos[0].Remote || repos[0].Slug() != "acme/app" {
		t.Fatalf("repos = %#v", repos)
	}
}

func TestApplyExplicitOrganizationNeverInspectsMemberOrganizations(t *testing.T) {
	originalUser, originalOrgs, originalList := mergePolicyAuthUser, mergePolicyMemberOrgs, mergePolicyListRemote
	originalDiscover, originalRead, originalExecute := mergePolicyDiscover, mergePolicyRead, mergePolicyExecute
	t.Cleanup(func() {
		mergePolicyAuthUser, mergePolicyMemberOrgs, mergePolicyListRemote = originalUser, originalOrgs, originalList
		mergePolicyDiscover, mergePolicyRead, mergePolicyExecute = originalDiscover, originalRead, originalExecute
	})
	mergePolicyAuthUser = func() (string, error) { t.Fatal("explicit --org must not add the user owner"); return "", nil }
	mergePolicyMemberOrgs = func() ([]string, error) { t.Fatal("explicit --org must not inventory memberships"); return nil, nil }
	var listed []string
	mergePolicyListRemote = func(owner string) ([]discover.Repo, error) {
		listed = append(listed, owner)
		return []discover.Repo{{Org: owner, Name: "app"}}, nil
	}
	mergePolicyDiscover = func(_ string, filter string, owners, repositories []string, includeUser bool) ([]discover.Repo, error) {
		return discoverRemoteMergePolicyFleet(filter, owners, repositories, includeUser)
	}
	repositoryBody := []byte(`{"default_branch":"main","allow_merge_commit":false,"allow_squash_merge":true,"allow_rebase_merge":true}`)
	mergePolicyRead = func(_ context.Context, endpoint string) ([]byte, error) {
		if strings.Contains(endpoint, "unselected") {
			t.Fatalf("unselected member organization was inspected: %s", endpoint)
		}
		switch {
		case strings.Contains(endpoint, "/rules/branches/"):
			return []byte(`[]`), nil
		case strings.HasSuffix(endpoint, "/protection"):
			return nil, errors.New("gh: Not Found (HTTP 404)")
		default:
			return repositoryBody, nil
		}
	}
	mergePolicyExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		if strings.Contains(strings.Join(args, " "), "unselected") {
			t.Fatal("unselected member organization was mutated")
		}
		return githubobserver.CommandResponse{}
	}
	report, err := runMergePolicy(context.Background(), mergePolicyOptions{apply: true, owners: []string{"selected"}, parallel: 1, reportDir: t.TempDir()}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(listed, []string{"selected"}) || len(report.Repositories) != 1 || report.Repositories[0].Repository != "selected/app" {
		t.Fatalf("listed=%#v report=%#v", listed, report.Repositories)
	}
}

func TestInspectMergePolicyReportsRepositoryDriftAndProtectionConflicts(t *testing.T) {
	original := mergePolicyRead
	t.Cleanup(func() { mergePolicyRead = original })
	mergePolicyRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"default_branch":"main","allow_merge_commit":false,"allow_squash_merge":true,"allow_rebase_merge":false,"merge_commit_title":"MERGE_MESSAGE","merge_commit_message":"PR_TITLE"}`), nil
		case "repos/acme/app/rules/branches/main?per_page=100":
			return []byte(`[{"type":"required_linear_history","ruleset_source_type":"Organization","ruleset_source":"acme","ruleset_id":7},{"type":"merge_queue","ruleset_source_type":"Repository","ruleset_source":"acme/app","ruleset_id":8}]`), nil
		case "repos/acme/app/branches/main/protection":
			return []byte(`{"required_linear_history":{"enabled":true}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	got := inspectMergePolicyRepository(context.Background(), "acme/app")
	if got.Disposition != "blocked" || len(got.Drift) != 5 || len(got.Conflicts) != 2 || !got.ClassicLinear {
		t.Fatalf("result = %#v", got)
	}
}

func TestRunMergePolicyApplyPlansBeforeMutationAndRefusesDrift(t *testing.T) {
	originalDiscover, originalRead, originalExecute := mergePolicyDiscover, mergePolicyRead, mergePolicyExecute
	t.Cleanup(func() {
		mergePolicyDiscover, mergePolicyRead, mergePolicyExecute = originalDiscover, originalRead, originalExecute
	})
	mergePolicyDiscover = func(string, string, []string, []string, bool) ([]discover.Repo, error) {
		return []discover.Repo{{Org: "acme", Name: "app", Remote: true}}, nil
	}
	reads := 0
	mergePolicyRead = func(_ context.Context, endpoint string) ([]byte, error) {
		if strings.Contains(endpoint, "/rules/branches/") {
			return []byte(`[]`), nil
		}
		if strings.Contains(endpoint, "/protection") {
			return nil, errors.New("gh: Not Found (HTTP 404)")
		}
		reads++
		if reads == 1 {
			return []byte(`{"default_branch":"main","allow_merge_commit":false,"allow_squash_merge":true,"allow_rebase_merge":true,"merge_commit_title":"MERGE_MESSAGE","merge_commit_message":"PR_TITLE"}`), nil
		}
		return []byte(`{"default_branch":"main","allow_merge_commit":true,"allow_squash_merge":true,"allow_rebase_merge":true,"merge_commit_title":"MERGE_MESSAGE","merge_commit_message":"PR_TITLE"}`), nil
	}
	mutated := false
	mergePolicyExecute = func(context.Context, ...string) githubobserver.CommandResponse {
		mutated = true
		return githubobserver.CommandResponse{}
	}
	reportDir := t.TempDir()
	var progress bytes.Buffer
	report, err := runMergePolicy(context.Background(), mergePolicyOptions{apply: true, parallel: 1, reportDir: reportDir}, &progress)
	if err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("settings drifted after planning; mutation must be refused")
	}
	if report.Repositories[0].Disposition != "blocked" || !strings.Contains(progress.String(), "planned 1 repositories") {
		t.Fatalf("report=%#v progress=%q", report, progress.String())
	}
	stored, err := os.ReadFile(filepath.Join(reportDir, "merge-policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted mergePolicyReport
	if err := json.Unmarshal(stored, &persisted); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persisted.Summary, report.Summary) {
		t.Fatalf("persisted summary %#v != %#v", persisted.Summary, report.Summary)
	}
}

func TestRunMergePolicyApplyIgnoresUnrelatedRepositoryResponseChanges(t *testing.T) {
	originalDiscover, originalRead, originalExecute := mergePolicyDiscover, mergePolicyRead, mergePolicyExecute
	t.Cleanup(func() {
		mergePolicyDiscover, mergePolicyRead, mergePolicyExecute = originalDiscover, originalRead, originalExecute
	})
	mergePolicyDiscover = func(string, string, []string, []string, bool) ([]discover.Repo, error) {
		return []discover.Repo{{Org: "acme", Name: "app", Remote: true}}, nil
	}
	repositoryReads := 0
	mergePolicyRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch {
		case strings.Contains(endpoint, "/rules/branches/"):
			return []byte(`[]`), nil
		case strings.Contains(endpoint, "/protection"):
			return nil, errors.New("gh: Not Found (HTTP 404)")
		default:
			repositoryReads++
			return fmt.Appendf(nil, `{"default_branch":"main","allow_merge_commit":false,"allow_squash_merge":true,"allow_rebase_merge":true,"merge_commit_title":"MERGE_MESSAGE","merge_commit_message":"PR_TITLE","temp_clone_token":"dummy-%d","unrelated_metadata":{"observation":%d}}`, repositoryReads, repositoryReads), nil
		}
	}
	mutations := 0
	mergePolicyExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutations++
		if command := strings.Join(args, " "); !strings.Contains(command, "--method PATCH repos/acme/app") {
			return githubobserver.CommandResponse{Err: errors.New("unexpected mutation " + command)}
		}
		return githubobserver.CommandResponse{}
	}

	report, err := runMergePolicy(context.Background(), mergePolicyOptions{apply: true, parallel: 1, reportDir: t.TempDir()}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if repositoryReads != 3 {
		t.Fatalf("repository reads = %d, want plan and two validations", repositoryReads)
	}
	if mutations != 1 || report.Repositories[0].Disposition != "applied" {
		t.Fatalf("mutations=%d repository=%#v", mutations, report.Repositories[0])
	}
}

func TestApplyRepositoryRulesetPreservesUnrelatedProtections(t *testing.T) {
	originalRead, originalExecute := mergePolicyRead, mergePolicyExecute
	t.Cleanup(func() { mergePolicyRead, mergePolicyExecute = originalRead, originalExecute })
	mergePolicyRead = func(context.Context, string) ([]byte, error) {
		return []byte(`{"id":7,"name":"default","target":"branch","enforcement":"active","conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},"rules":[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"CI"}]}},{"type":"required_linear_history"},{"type":"pull_request","parameters":{"required_approving_review_count":2,"allowed_merge_methods":["squash","rebase"]}}],"bypass_actors":[]}`), nil
	}
	var input string
	mergePolicyExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		for i, arg := range args {
			if arg == "--input" && i+1 < len(args) {
				payload, err := os.ReadFile(args[i+1])
				if err != nil {
					return githubobserver.CommandResponse{Err: err}
				}
				input = string(payload)
			}
		}
		return githubobserver.CommandResponse{}
	}
	if err := applySharedRuleset(context.Background(), mergePolicyRulesetChange{SourceType: "Repository", Source: "acme/app", ID: 7}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input, `"required_approving_review_count":2`) || !strings.Contains(input, `"required_status_checks"`) || !strings.Contains(input, `"allowed_merge_methods":["merge"]`) {
		t.Fatalf("payload did not preserve protections: %s", input)
	}
	if strings.Contains(input, `"id":7`) {
		t.Fatalf("response-only id was sent: %s", input)
	}
	if strings.Contains(input, `"required_linear_history"`) {
		t.Fatalf("repository linear-history rule was preserved: %s", input)
	}
}

func TestRepositoryRulesetLinearHistoryIsPlannedAsRemovableDrift(t *testing.T) {
	original := mergePolicyRead
	t.Cleanup(func() { mergePolicyRead = original })
	mergePolicyRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"default_branch":"main","allow_merge_commit":true,"allow_squash_merge":false,"allow_rebase_merge":false,"merge_commit_title":"PR_TITLE","merge_commit_message":"PR_BODY"}`), nil
		case "repos/acme/app/branches/main/protection":
			return nil, errors.New("gh: Not Found (HTTP 404)")
		case "repos/acme/app/rules/branches/main?per_page=100":
			return []byte(`[{"type":"required_linear_history","ruleset_source_type":"Repository","ruleset_source":"acme/app","ruleset_id":7}]`), nil
		case "repos/acme/app/rulesets/7":
			return []byte(`{"id":7,"rules":[{"type":"required_linear_history"}]}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	repo := inspectMergePolicyRepository(context.Background(), "acme/app")
	if repo.Disposition != "drift" || len(repo.Conflicts) != 0 || len(repo.Rulesets) != 1 {
		t.Fatalf("repository = %#v", repo)
	}
	report := mergePolicyReport{Repositories: []mergePolicyRepository{repo}}
	buildMergePolicyRulesetPlan(context.Background(), &report)
	if len(report.Rulesets) != 1 || report.Rulesets[0].Disposition != "planned" || report.Rulesets[0].ObservedSHA == "" {
		t.Fatalf("ruleset plan = %#v", report.Rulesets)
	}
}

func TestInspectClassicProtectionTreats404AsNoneAndReportsLinearHistory(t *testing.T) {
	original := mergePolicyRead
	t.Cleanup(func() { mergePolicyRead = original })
	mergePolicyRead = func(context.Context, string) ([]byte, error) { return nil, errors.New("gh: Not Found (HTTP 404)") }
	inspection, unavailable, err := inspectClassicProtectionSnapshot(context.Background(), "acme/app", "main")
	if err != nil || unavailable || inspection.ProtectionSHA != "none" || inspection.ClassicLinear || len(inspection.Conflicts) != 0 {
		t.Fatalf("404 result = %#v unavailable %v err %v", inspection, unavailable, err)
	}
	mergePolicyRead = func(context.Context, string) ([]byte, error) {
		return []byte(`{"required_linear_history":{"enabled":true}}`), nil
	}
	inspection, unavailable, err = inspectClassicProtectionSnapshot(context.Background(), "acme/app", "main")
	if err != nil || unavailable || !inspection.ClassicLinear || len(inspection.Conflicts) != 0 {
		t.Fatalf("linear result = %#v unavailable %v err %v", inspection, unavailable, err)
	}
}

func TestInspectMergePolicyCorroboratesExactPrivatePlanGate(t *testing.T) {
	original := mergePolicyRead
	t.Cleanup(func() { mergePolicyRead = original })
	repositoryBody := []byte(`{"default_branch":"main","allow_merge_commit":true,"allow_squash_merge":true,"allow_rebase_merge":false,"merge_commit_title":"PR_TITLE","merge_commit_message":"PR_BODY"}`)
	planGate := errors.New("gh: " + githubPolicyPlanGateMessage + " (HTTP 403)")
	mergePolicyRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch {
		case endpoint == "repos/acme/private":
			return repositoryBody, nil
		case strings.HasSuffix(endpoint, "/protection"), strings.Contains(endpoint, "/rules/branches/"):
			return nil, planGate
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	repo := inspectMergePolicyRepository(context.Background(), "acme/private")
	if repo.Disposition != "drift" || repo.Error != "" || repo.ProtectionSHA != mergePolicyUnavailableByPlan || repo.RulesSHA != mergePolicyUnavailableByPlan {
		t.Fatalf("repository = %#v", repo)
	}
}

func TestInspectMergePolicyPlanGateFailsClosedUnlessExactAndCorroborated(t *testing.T) {
	original := mergePolicyRead
	t.Cleanup(func() { mergePolicyRead = original })
	exact := errors.New("gh: " + githubPolicyPlanGateMessage + " (HTTP 403)")
	cases := []struct {
		name       string
		protection error
		rules      error
	}{
		{name: "generic forbidden", protection: errors.New("gh: Forbidden (HTTP 403)"), rules: errors.New("gh: Forbidden (HTTP 403)")},
		{name: "changed message", protection: errors.New("gh: Upgrade your plan to enable this feature. (HTTP 403)"), rules: errors.New("gh: Upgrade your plan to enable this feature. (HTTP 403)")},
		{name: "only classic is gated", protection: exact},
		{name: "only effective rules are gated", protection: errors.New("gh: Not Found (HTTP 404)"), rules: exact},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			mergePolicyRead = func(_ context.Context, endpoint string) ([]byte, error) {
				switch {
				case endpoint == "repos/acme/private":
					return []byte(`{"default_branch":"main"}`), nil
				case strings.HasSuffix(endpoint, "/protection"):
					if test.protection != nil {
						return nil, test.protection
					}
					return []byte(`{}`), nil
				case strings.Contains(endpoint, "/rules/branches/"):
					if test.rules != nil {
						return nil, test.rules
					}
					return []byte(`[]`), nil
				default:
					return nil, errors.New("unexpected endpoint " + endpoint)
				}
			}
			repo := inspectMergePolicyRepository(context.Background(), "acme/private")
			if repo.Disposition != "error" || repo.Error == "" {
				t.Fatalf("repository = %#v", repo)
			}
		})
	}
}

func TestApplyMergePolicyRechecksPlanGateBeforePatch(t *testing.T) {
	originalRead, originalExecute := mergePolicyRead, mergePolicyExecute
	t.Cleanup(func() { mergePolicyRead, mergePolicyExecute = originalRead, originalExecute })
	repositoryBody := []byte(`{"default_branch":"main"}`)
	exact := errors.New("gh: " + githubPolicyPlanGateMessage + " (HTTP 403)")
	classicReads := 0
	mergePolicyRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch {
		case endpoint == "repos/acme/private":
			return repositoryBody, nil
		case strings.HasSuffix(endpoint, "/protection"):
			classicReads++
			if classicReads == 1 {
				return nil, exact
			}
			return nil, errors.New("gh: Forbidden (HTTP 403)")
		case strings.Contains(endpoint, "/rules/branches/"):
			return nil, exact
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	mutated := false
	mergePolicyExecute = func(context.Context, ...string) githubobserver.CommandResponse {
		mutated = true
		return githubobserver.CommandResponse{}
	}
	report := mergePolicyReport{Repositories: []mergePolicyRepository{{
		Repository: "acme/private", DefaultBranch: "main", Disposition: "drift",
		ObservedSHA: mustRepositoryPolicySHA(t, repositoryBody), ProtectionSHA: mergePolicyUnavailableByPlan, RulesSHA: mergePolicyUnavailableByPlan,
	}}}
	if err := applyMergePolicy(context.Background(), &report, 1, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if mutated || report.Repositories[0].Disposition != "blocked" || classicReads != 2 {
		t.Fatalf("mutated=%v reads=%d repository=%#v", mutated, classicReads, report.Repositories[0])
	}
}

func TestApplyMergePolicyAllowsStableCorroboratedPlanGate(t *testing.T) {
	originalRead, originalExecute := mergePolicyRead, mergePolicyExecute
	t.Cleanup(func() { mergePolicyRead, mergePolicyExecute = originalRead, originalExecute })
	repositoryBody := []byte(`{"default_branch":"main"}`)
	exact := errors.New("gh: " + githubPolicyPlanGateMessage + " (HTTP 403)")
	mergePolicyRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch {
		case endpoint == "repos/acme/private":
			return repositoryBody, nil
		case strings.HasSuffix(endpoint, "/protection"), strings.Contains(endpoint, "/rules/branches/"):
			return nil, exact
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	mutations := 0
	mergePolicyExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutations++
		if got := strings.Join(args, " "); !strings.Contains(got, "--method PATCH repos/acme/private") {
			return githubobserver.CommandResponse{Err: errors.New("unexpected mutation " + got)}
		}
		return githubobserver.CommandResponse{}
	}
	report := mergePolicyReport{Repositories: []mergePolicyRepository{{
		Repository: "acme/private", DefaultBranch: "main", Disposition: "drift",
		ObservedSHA: mustRepositoryPolicySHA(t, repositoryBody), ProtectionSHA: mergePolicyUnavailableByPlan, RulesSHA: mergePolicyUnavailableByPlan,
	}}}
	if err := applyMergePolicy(context.Background(), &report, 1, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if mutations != 1 || report.Repositories[0].Disposition != "applied" {
		t.Fatalf("mutations=%d repository=%#v", mutations, report.Repositories[0])
	}
}

func TestApplyClassicLinearHistoryUsesFullPreservingUpdateAndRecordsPartialResult(t *testing.T) {
	originalRead, originalExecute := mergePolicyRead, mergePolicyExecute
	t.Cleanup(func() { mergePolicyRead, mergePolicyExecute = originalRead, originalExecute })
	repositoryBody := []byte(`{"default_branch":"main"}`)
	protectionBody := []byte(`{
		"required_status_checks":{"url":"ignored","strict":true,"contexts":["CI"],"contexts_url":"ignored","checks":[{"context":"Build","app_id":12345},{"context":"Portable","app_id":null}]},
		"enforce_admins":{"url":"ignored","enabled":true},
		"required_pull_request_reviews":{"url":"ignored","dismissal_restrictions":{"url":"ignored","users":[{"login":"octocat","id":1}],"teams":[{"slug":"reviewers","id":2}],"apps":[{"slug":"review-app","id":3}]},"dismiss_stale_reviews":true,"require_code_owner_reviews":true,"required_approving_review_count":2,"require_last_push_approval":true,"bypass_pull_request_allowances":{"users":[{"login":"maintainer"}],"teams":[{"slug":"release"}],"apps":[{"slug":"release-app"}]}},
		"restrictions":{"url":"ignored","users":[{"login":"deployer"}],"teams":[{"slug":"platform"}],"apps":[{"slug":"deploy-app"}]},
		"required_linear_history":{"enabled":true},"allow_force_pushes":{"enabled":true},"allow_deletions":{"enabled":false},"block_creations":{"enabled":true},"required_conversation_resolution":{"enabled":true},"lock_branch":{"enabled":false},"allow_fork_syncing":{"enabled":true},
		"required_signatures":{"url":"ignored","enabled":true},"url":"ignored"
	}`)
	mergePolicyRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch {
		case strings.HasSuffix(endpoint, "/protection"):
			return protectionBody, nil
		case strings.Contains(endpoint, "/rules/branches/"):
			return []byte(`[]`), nil
		default:
			return repositoryBody, nil
		}
	}
	var calls []string
	var protectionInput []byte
	mergePolicyExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		if strings.Contains(call, "--method PUT") {
			for index, arg := range args {
				if arg == "--input" && index+1 < len(args) {
					var err error
					protectionInput, err = os.ReadFile(args[index+1])
					if err != nil {
						return githubobserver.CommandResponse{Err: err}
					}
				}
			}
		}
		if strings.Contains(call, "--method PATCH") {
			return githubobserver.CommandResponse{Err: errors.New("patch failed")}
		}
		return githubobserver.CommandResponse{}
	}
	report := mergePolicyReport{ReportPath: filepath.Join(t.TempDir(), "merge-policy.json"), Repositories: []mergePolicyRepository{{
		Repository:    "acme/app",
		DefaultBranch: "main",
		Disposition:   "drift",
		ObservedSHA:   mustRepositoryPolicySHA(t, repositoryBody),
		ProtectionSHA: digestJSON(protectionBody),
		RulesSHA:      digestJSON([]byte(`[]`)),
		ClassicLinear: true,
	}}}
	if err := applyMergePolicy(context.Background(), &report, 1, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "--method PUT repos/acme/app/branches/main/protection --input") {
		t.Fatalf("calls = %#v", calls)
	}
	if strings.Contains(calls[0], "DELETE") || strings.Contains(calls[0], "required_linear_history") {
		t.Fatalf("classic protection must use the full parent update, not a nested delete: %q", calls[0])
	}
	var got, want any
	if err := json.Unmarshal(protectionInput, &got); err != nil {
		t.Fatal(err)
	}
	expected := `{"required_status_checks":{"strict":true,"contexts":["CI"],"checks":[{"context":"Build","app_id":12345},{"context":"Portable","app_id":null}]},"enforce_admins":true,"required_pull_request_reviews":{"dismissal_restrictions":{"users":["octocat"],"teams":["reviewers"],"apps":["review-app"]},"dismiss_stale_reviews":true,"require_code_owner_reviews":true,"required_approving_review_count":2,"require_last_push_approval":true,"bypass_pull_request_allowances":{"users":["maintainer"],"teams":["release"],"apps":["release-app"]}},"restrictions":{"users":["deployer"],"teams":["platform"],"apps":["deploy-app"]},"required_linear_history":false,"allow_force_pushes":true,"allow_deletions":false,"block_creations":true,"required_conversation_resolution":true,"lock_branch":false,"allow_fork_syncing":true}`
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("protection payload = %s\nwant %s", protectionInput, expected)
	}
	persisted, err := os.ReadFile(report.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), "removed_classic_required_linear_history") || report.Repositories[0].Disposition != "error" {
		t.Fatalf("partial receipt = %s repository=%#v", persisted, report.Repositories[0])
	}
}

func TestRunMergePolicyResumeCarriesPartialActions(t *testing.T) {
	originalDiscover, originalRead, originalExecute := mergePolicyDiscover, mergePolicyRead, mergePolicyExecute
	t.Cleanup(func() {
		mergePolicyDiscover, mergePolicyRead, mergePolicyExecute = originalDiscover, originalRead, originalExecute
	})
	reportDir := t.TempDir()
	path := filepath.Join(reportDir, "merge-policy.json")
	previous := mergePolicyReport{ReportPath: path, Repositories: []mergePolicyRepository{{Repository: "acme/app", AppliedActions: []string{"removed_classic_required_linear_history"}}}}
	if err := persistMergePolicyReport(previous); err != nil {
		t.Fatal(err)
	}
	mergePolicyDiscover = func(string, string, []string, []string, bool) ([]discover.Repo, error) {
		return []discover.Repo{{Org: "acme", Name: "app", Remote: true}}, nil
	}
	repositoryBody := []byte(`{"default_branch":"main","allow_merge_commit":false,"allow_squash_merge":true,"allow_rebase_merge":true}`)
	mergePolicyRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch {
		case strings.Contains(endpoint, "/rules/branches/"):
			return []byte(`[]`), nil
		case strings.HasSuffix(endpoint, "/protection"):
			return nil, errors.New("gh: Not Found (HTTP 404)")
		default:
			return repositoryBody, nil
		}
	}
	mergePolicyExecute = func(context.Context, ...string) githubobserver.CommandResponse {
		return githubobserver.CommandResponse{}
	}
	report, err := runMergePolicy(context.Background(), mergePolicyOptions{apply: true, resume: true, parallel: 1, reportDir: reportDir}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	actions := report.Repositories[0].AppliedActions
	if !slices.Contains(actions, "removed_classic_required_linear_history") || !slices.Contains(actions, "updated_repository_merge_settings") {
		t.Fatalf("resumed actions = %#v", actions)
	}
}

func TestOrganizationRulesetIsAuditOnlyAndBlocksRepositoryFallback(t *testing.T) {
	originalRead, originalExecute := mergePolicyRead, mergePolicyExecute
	t.Cleanup(func() { mergePolicyRead, mergePolicyExecute = originalRead, originalExecute })
	mergePolicyRead = func(context.Context, string) ([]byte, error) { return []byte(`{"default_branch":"main"}`), nil }
	mutated := false
	mergePolicyExecute = func(context.Context, ...string) githubobserver.CommandResponse {
		mutated = true
		return githubobserver.CommandResponse{}
	}
	repositoryBody := []byte(`{"default_branch":"main"}`)
	report := mergePolicyReport{Repositories: []mergePolicyRepository{{Repository: "acme/app", DefaultBranch: "main", Disposition: "drift", ObservedSHA: mustRepositoryPolicySHA(t, repositoryBody), ProtectionSHA: digestJSON(repositoryBody), Rulesets: []mergePolicyRuleRef{{Type: "pull_request", SourceType: "Organization", Source: "acme", ID: 7, Methods: []string{"squash"}}}}}}
	buildMergePolicyRulesetPlan(context.Background(), &report)
	if len(report.Rulesets) != 1 || report.Rulesets[0].Disposition != "blocked" || !strings.Contains(report.Rulesets[0].Error, "audit-only") {
		t.Fatalf("ruleset plan = %#v", report.Rulesets)
	}
	if err := applyMergePolicy(context.Background(), &report, 1, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if mutated || report.Repositories[0].Disposition != "blocked" {
		t.Fatalf("mutated=%v repository=%#v", mutated, report.Repositories[0])
	}
}

func TestApplyMergePolicyBoundsRepositoryMutationsAndCheckpoints(t *testing.T) {
	originalRead, originalExecute := mergePolicyRead, mergePolicyExecute
	t.Cleanup(func() { mergePolicyRead, mergePolicyExecute = originalRead, originalExecute })
	body := []byte(`{"default_branch":"main"}`)
	mergePolicyRead = func(_ context.Context, endpoint string) ([]byte, error) {
		if strings.Contains(endpoint, "/rules/branches/") {
			return []byte(`[]`), nil
		}
		if strings.Contains(endpoint, "/protection") {
			return nil, errors.New("gh: Not Found (HTTP 404)")
		}
		return body, nil
	}
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	var active, maximum atomic.Int64
	mergePolicyExecute = func(context.Context, ...string) githubobserver.CommandResponse {
		current := active.Add(1)
		for observed := maximum.Load(); current > observed && !maximum.CompareAndSwap(observed, current); observed = maximum.Load() {
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
		return githubobserver.CommandResponse{}
	}
	report := mergePolicyReport{ReportPath: filepath.Join(t.TempDir(), "merge-policy.json")}
	for index := range 3 {
		report.Repositories = append(report.Repositories, mergePolicyRepository{
			Repository:    fmt.Sprintf("acme/app-%d", index),
			DefaultBranch: "main",
			Disposition:   "drift",
			ObservedSHA:   mustRepositoryPolicySHA(t, body),
			ProtectionSHA: "none",
			RulesSHA:      digestJSON([]byte(`[]`)),
		})
	}
	done := make(chan error, 1)
	go func() { done <- applyMergePolicy(context.Background(), &report, 2, &bytes.Buffer{}) }()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("two repository mutations did not run concurrently")
		}
	}
	select {
	case <-entered:
		t.Fatal("--parallel 2 allowed a third repository mutation")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum concurrent mutations = %d, want 2", got)
	}
	persisted, err := os.ReadFile(report.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint mergePolicyReport
	if err := json.Unmarshal(persisted, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Summary.Applied != 3 {
		t.Fatalf("checkpoint applied = %d, want 3", checkpoint.Summary.Applied)
	}
}

func TestApplyMergePolicyStopsAdmissionAfterCheckpointFailure(t *testing.T) {
	originalRead, originalExecute, originalPersist := mergePolicyRead, mergePolicyExecute, mergePolicyPersist
	t.Cleanup(func() {
		mergePolicyRead, mergePolicyExecute, mergePolicyPersist = originalRead, originalExecute, originalPersist
	})
	body := []byte(`{"default_branch":"main"}`)
	mergePolicyRead = func(_ context.Context, endpoint string) ([]byte, error) {
		if strings.Contains(endpoint, "/rules/branches/") {
			return []byte(`[]`), nil
		}
		if strings.Contains(endpoint, "/protection") {
			return nil, errors.New("gh: Not Found (HTTP 404)")
		}
		return body, nil
	}
	started := make(chan string, 3)
	releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{})
	mergePolicyExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		command := strings.Join(args, " ")
		switch {
		case strings.Contains(command, "repos/acme/app-0"):
			started <- "app-0"
			<-releaseFirst
		case strings.Contains(command, "repos/acme/app-1"):
			started <- "app-1"
			<-releaseSecond
		default:
			started <- "app-2"
		}
		return githubobserver.CommandResponse{}
	}
	checkpointFailed := make(chan struct{})
	var persistCalls atomic.Int64
	mergePolicyPersist = func(mergePolicyReport) error {
		if persistCalls.Add(1) == 2 {
			close(checkpointFailed)
			return errors.New("checkpoint unavailable")
		}
		return nil
	}
	report := mergePolicyReport{ReportPath: "injected-checkpoint"}
	for index := range 3 {
		report.Repositories = append(report.Repositories, mergePolicyRepository{
			Repository:    fmt.Sprintf("acme/app-%d", index),
			DefaultBranch: "main",
			Disposition:   "drift",
			ObservedSHA:   mustRepositoryPolicySHA(t, body),
			ProtectionSHA: "none",
			RulesSHA:      digestJSON([]byte(`[]`)),
		})
	}
	done := make(chan error, 1)
	go func() { done <- applyMergePolicy(context.Background(), &report, 2, &bytes.Buffer{}) }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("initial bounded mutations did not start")
		}
	}
	close(releaseFirst)
	select {
	case <-checkpointFailed:
	case <-time.After(time.Second):
		t.Fatal("injected checkpoint failure was not observed")
	}
	select {
	case repository := <-started:
		t.Fatalf("repository %s started after checkpoint failure", repository)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseSecond)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "checkpoint unavailable") {
		t.Fatalf("error = %v", err)
	}
	if report.Repositories[2].Disposition != "drift" {
		t.Fatalf("unadmitted repository = %#v", report.Repositories[2])
	}
	if got := persistCalls.Load(); got != 3 {
		t.Fatalf("checkpoint calls = %d, want initial, failed, and drained retry", got)
	}
}

func TestEnterpriseRuleThatAllowsMergeLeavesOnlyRepositorySettingsDrift(t *testing.T) {
	original := mergePolicyRead
	t.Cleanup(func() { mergePolicyRead = original })
	mergePolicyRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"default_branch":"main","allow_merge_commit":false,"allow_squash_merge":true,"allow_rebase_merge":true,"merge_commit_title":"MERGE_MESSAGE","merge_commit_message":"PR_TITLE"}`), nil
		case "repos/acme/app/branches/main/protection":
			return nil, errors.New("gh: Not Found (HTTP 404)")
		case "repos/acme/app/rules/branches/main?per_page=100":
			return []byte(`[{"type":"pull_request","ruleset_source_type":"Enterprise","ruleset_source":"acme-enterprise","ruleset_id":19769717,"parameters":{"allowed_merge_methods":["merge","squash","rebase"]}}]`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	repo := inspectMergePolicyRepository(context.Background(), "acme/app")
	if repo.Disposition != "drift" || len(repo.Conflicts) != 0 {
		t.Fatalf("repository = %#v", repo)
	}
	report := mergePolicyReport{Repositories: []mergePolicyRepository{repo}}
	buildMergePolicyRulesetPlan(context.Background(), &report)
	if len(report.Rulesets) != 0 {
		t.Fatalf("enterprise rule that already allows merge must not produce a shared plan: %#v", report.Rulesets)
	}
}

func mustRepositoryPolicySHA(t *testing.T, body []byte) string {
	t.Helper()
	_, sha, err := decodeRepositoryPolicy(body)
	if err != nil {
		t.Fatal(err)
	}
	return sha
}
