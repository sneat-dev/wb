package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/githubobserver"
)

func TestFleetMergePolicyHelpAndFlags(t *testing.T) {
	command := newFleetMergePolicyCmd()
	for _, name := range []string{"apply", "parallel", "report-dir", "resume", "format", "json"} {
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
	if got := command.Flags().Lookup("parallel").DefValue; got != "4" {
		t.Fatalf("--parallel default = %s", got)
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
	repos, err := discoverRemoteMergePolicyFleet("acme/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || !repos[0].Remote || repos[0].Slug() != "acme/app" {
		t.Fatalf("repos = %#v", repos)
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
	if got.Disposition != "blocked" || len(got.Drift) != 4 || len(got.Conflicts) != 3 {
		t.Fatalf("result = %#v", got)
	}
}

func TestRunMergePolicyApplyPlansBeforeMutationAndRefusesDrift(t *testing.T) {
	originalDiscover, originalRead, originalExecute := mergePolicyDiscover, mergePolicyRead, mergePolicyExecute
	t.Cleanup(func() {
		mergePolicyDiscover, mergePolicyRead, mergePolicyExecute = originalDiscover, originalRead, originalExecute
	})
	mergePolicyDiscover = func(string, string) ([]discover.Repo, error) {
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

func TestApplyRepositoryRulesetPreservesUnrelatedProtections(t *testing.T) {
	originalRead, originalExecute := mergePolicyRead, mergePolicyExecute
	t.Cleanup(func() { mergePolicyRead, mergePolicyExecute = originalRead, originalExecute })
	mergePolicyRead = func(context.Context, string) ([]byte, error) {
		return []byte(`{"id":7,"name":"default","target":"branch","enforcement":"active","conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},"rules":[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"CI"}]}},{"type":"pull_request","parameters":{"required_approving_review_count":2,"allowed_merge_methods":["squash","rebase"]}}],"bypass_actors":[]}`), nil
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
}

func TestInspectClassicProtectionTreats404AsNoneAndLinearHistoryAsConflict(t *testing.T) {
	original := mergePolicyRead
	t.Cleanup(func() { mergePolicyRead = original })
	mergePolicyRead = func(context.Context, string) ([]byte, error) { return nil, errors.New("gh: Not Found (HTTP 404)") }
	sha, conflicts, err := inspectClassicProtection(context.Background(), "acme/app", "main")
	if err != nil || sha != "none" || len(conflicts) != 0 {
		t.Fatalf("404 result = sha %q conflicts %#v err %v", sha, conflicts, err)
	}
	mergePolicyRead = func(context.Context, string) ([]byte, error) {
		return []byte(`{"required_linear_history":{"enabled":true}}`), nil
	}
	_, conflicts, err = inspectClassicProtection(context.Background(), "acme/app", "main")
	if err != nil || len(conflicts) != 1 || !strings.Contains(conflicts[0], "linear history") {
		t.Fatalf("linear result = conflicts %#v err %v", conflicts, err)
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
	report := mergePolicyReport{Repositories: []mergePolicyRepository{{Repository: "acme/app", DefaultBranch: "main", Disposition: "drift", ObservedSHA: digestJSON([]byte(`{"default_branch":"main"}`)), ProtectionSHA: digestJSON([]byte(`{"default_branch":"main"}`)), Rulesets: []mergePolicyRuleRef{{Type: "pull_request", SourceType: "Organization", Source: "acme", ID: 7, Methods: []string{"squash"}}}}}}
	buildMergePolicyRulesetPlan(context.Background(), &report)
	if len(report.Rulesets) != 1 || report.Rulesets[0].Disposition != "blocked" || !strings.Contains(report.Rulesets[0].Error, "audit-only") {
		t.Fatalf("ruleset plan = %#v", report.Rulesets)
	}
	applyMergePolicy(context.Background(), &report, &bytes.Buffer{})
	if mutated || report.Repositories[0].Disposition != "blocked" {
		t.Fatalf("mutated=%v repository=%#v", mutated, report.Repositories[0])
	}
}
