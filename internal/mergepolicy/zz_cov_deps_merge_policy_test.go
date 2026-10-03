package mergepolicy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func cwDepsMergePolicyReportFixture() Report {
	return Report{
		SchemaVersion: mergePolicySchemaVersion, Mode: "check",
		Summary: Summary{Inspected: 4, Compliant: 1, Drift: 1, Blocked: 1, Errors: 1},
		Repositories: []Repository{
			{Repository: "acme/clean", Disposition: "compliant"},
			{Repository: "acme/drift", Disposition: "drift", Drift: []string{"allow_squash_merge=true"}},
			{Repository: "acme/conflict", Disposition: "blocked", Conflicts: []string{"repository ruleset 7 (acme): requires linear history"}},
			{Repository: "acme/broken", Disposition: "error", Error: "decode repository settings: unexpected EOF"},
		},
		Rulesets: []RulesetChange{{SourceType: "Organization", Source: "acme", ID: 9, Repositories: []string{"acme/a", "acme/b"}, Disposition: "planned"}},
	}
}

func TestCwDepsMergePolicyReportPath(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "policy")
	path, err := mergePolicyReportPath(Scope{}, explicit)
	if err != nil || path != filepath.Join(explicit, "merge-policy.json") {
		t.Fatalf("explicit report path = %q, %v", path, err)
	}
	if _, statErr := os.Stat(explicit); statErr != nil {
		t.Errorf("explicit report directory was not created: %v", statErr)
	}
	// An explicit path that cannot be a directory is an error.
	blocker := filepath.Join(t.TempDir(), "a-file")
	cwCovWriteFile(t, blocker, "not a directory\n")
	if _, err := mergePolicyReportPath(Scope{}, filepath.Join(blocker, "child")); err == nil {
		t.Error("a report directory beneath a file must fail")
	}
	// No explicit path defaults beneath the projects root's state home.
	ProjectsRoot := t.TempDir()
	t.Setenv(wbhome.EnvOverride, ProjectsRoot)
	home := filepath.Join(ProjectsRoot, ".wb")
	derived, err := mergePolicyReportPath(Scope{}, "")
	if err != nil || !strings.HasPrefix(derived, home) || !strings.HasSuffix(derived, "merge-policy.json") {
		t.Fatalf("derived report path = %q, %v", derived, err)
	}
}

func TestCwDepsSummarizeMergePolicyCountsEveryDisposition(t *testing.T) {
	report := cwDepsMergePolicyReportFixture()
	summarizeMergePolicy(&report)
	if report.Summary.Inspected != 4 || report.Summary.Compliant != 1 || report.Summary.Drift != 1 ||
		report.Summary.Blocked != 1 || report.Summary.Errors != 1 || report.Summary.Applied != 0 {
		t.Fatalf("summary = %+v", report.Summary)
	}
	applied := Report{Repositories: []Repository{{Disposition: "applied"}, {Disposition: "something-else"}}}
	summarizeMergePolicy(&applied)
	if applied.Summary.Applied != 1 || applied.Summary.Inspected != 2 {
		t.Fatalf("applied summary = %+v", applied.Summary)
	}
}

func TestCwDepsMergePolicySmallHelpers(t *testing.T) {
	if !allowsMerge([]string{"squash", "merge"}) {
		t.Error("allowsMerge must find merge among the methods")
	}
	if allowsMerge([]string{"squash", "rebase"}) || allowsMerge(nil) {
		t.Error("allowsMerge must not invent merge")
	}
	values := appendUnique([]string{"a", "b"}, "a")
	if len(values) != 2 {
		t.Errorf("appendUnique duplicated a value: %v", values)
	}
	values = appendUnique(values, "c")
	if len(values) != 3 || values[2] != "c" {
		t.Errorf("appendUnique did not append: %v", values)
	}
	sorted := []string{"a", "c", "e"}
	if !containsSorted(sorted, "c") || containsSorted(sorted, "b") || containsSorted(nil, "a") {
		t.Error("containsSorted misreported membership")
	}
	if digestJSON([]byte("x")) == digestJSON([]byte("y")) || len(digestJSON(nil)) != 64 {
		t.Error("digestJSON does not hash its input")
	}
	for name, test := range map[string]struct {
		response githubobserver.CommandResponse
		want     string
	}{
		"stderr wins": {githubobserver.CommandResponse{Stderr: []byte(" stderr detail "), Stdout: []byte("stdout")}, "stderr detail"},
		"stdout":      {githubobserver.CommandResponse{Stdout: []byte(" stdout detail ")}, "stdout detail"},
		"error":       {githubobserver.CommandResponse{Err: errors.New("command failed")}, "command failed"},
		"empty":       {githubobserver.CommandResponse{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := githubobserver.CommandDiagnostic(test.response); got != test.want {
				t.Fatalf("githubCommandMessage = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCwDepsDecodeRepositoryPolicyRoundTrips(t *testing.T) {
	raw, err := json.Marshal(githubRepositoryPolicy{DefaultBranch: "main", AllowMergeCommit: true,
		MergeCommitTitle: "PR_TITLE", MergeCommitMessage: "PR_BODY"})
	if err != nil {
		t.Fatal(err)
	}
	policy, digest, err := decodeRepositoryPolicy(raw)
	if err != nil || policy.DefaultBranch != "main" || !policy.AllowMergeCommit || len(digest) != 64 {
		t.Fatalf("decoded policy = %+v, %q, %v", policy, digest, err)
	}
	if _, _, err := decodeRepositoryPolicy([]byte("{not json")); err == nil {
		t.Fatal("a malformed repository policy must be refused")
	}
	// The digest tracks content, so two different policies cannot share one.
	other, _ := json.Marshal(githubRepositoryPolicy{DefaultBranch: "trunk"})
	if _, otherDigest, _ := decodeRepositoryPolicy(other); otherDigest == digest {
		t.Error("distinct policies produced the same digest")
	}
}

func TestCwDepsIsGitHubPolicyPlanGate(t *testing.T) {
	if isGitHubPolicyPlanGate(nil) {
		t.Error("no error is not a plan gate")
	}
	if isGitHubPolicyPlanGate(errors.New("HTTP 500 server error")) {
		t.Error("a non-403 error is not a plan gate")
	}
	if isGitHubPolicyPlanGate(errors.New("HTTP 403 Forbidden")) {
		t.Error("a 403 without the plan message is not a plan gate")
	}
	if !isGitHubPolicyPlanGate(errors.New("gh: " + githubPolicyPlanGateMessage + " (HTTP 403)")) {
		t.Error("the gh-formatted plan gate must be recognized")
	}
	if !isGitHubPolicyPlanGate(errors.New(`HTTP 403: {"message":"` + githubPolicyPlanGateMessage + `"}`)) {
		t.Error("the raw-API plan gate must be recognized")
	}
}

func TestCwDepsDiscoverRemoteMergePolicyFleetExactRepositories(t *testing.T) {
	service := New()

	t.Setenv(wbhome.EnvOverride, t.TempDir())
	repos, err := service.discoverRemoteMergePolicyFleet("", []string{"acme/a"}, []string{"acme/b", "acme/b", "zebra/c", " acme/d "}, false)
	if err != nil {
		t.Fatalf("exact repositories: %v", err)
	}
	var slugs []string
	for _, repo := range repos {
		if !repo.Remote {
			t.Errorf("%s was not marked remote", repo.Slug())
		}
		slugs = append(slugs, repo.Slug())
	}
	if strings.Join(slugs, ",") != "acme/b,acme/d,zebra/c" {
		t.Fatalf("exact repositories = %v", slugs)
	}
	if filtered, err := service.discoverRemoteMergePolicyFleet("acme/", nil, []string{"acme/b", "zebra/c"}, false); err != nil || len(filtered) != 1 || filtered[0].Slug() != "acme/b" {
		t.Fatalf("filtered exact repositories = %+v, %v", filtered, err)
	}
	for _, invalid := range []string{"noslash", "/name", "owner/", "owner/a/b"} {
		if _, err := service.discoverRemoteMergePolicyFleet("", nil, []string{invalid}, false); err == nil ||
			!strings.Contains(err.Error(), "invalid --repo") {
			t.Fatalf("invalid --repo %q error = %v", invalid, err)
		}
	}
}

// TestCwDepsDiscoverRemoteMergePolicyFleetStubsDiscovery drives the owner
// resolution and remote listing through the package's own injectable seams, so
// no live GitHub call is made.
func TestCwDepsDiscoverRemoteMergePolicyFleetStubsDiscovery(t *testing.T) {
	service := New()

	previousUser, previousOrgs, previousList := service.deps.AuthUser, service.deps.MemberOrgs, service.deps.ListRemote
	t.Cleanup(func() {
		service.deps.AuthUser, service.deps.MemberOrgs, service.deps.ListRemote = previousUser, previousOrgs, previousList
	})

	service.deps.AuthUser = func() (string, error) { return "cwcov-user", nil }
	service.deps.MemberOrgs = func() ([]string, error) { return []string{"zeta", "alpha"}, nil }
	service.deps.ListRemote = func(owner string) ([]discover.Repo, error) {
		return []discover.Repo{{Org: owner, Name: "app"}, {Org: owner, Name: "skip-me"}}, nil
	}
	repos, err := service.discoverRemoteMergePolicyFleet("", nil, nil, false)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	var slugs []string
	for _, repo := range repos {
		slugs = append(slugs, repo.Slug())
	}
	if strings.Join(slugs, ",") != "alpha/app,alpha/skip-me,cwcov-user/app,cwcov-user/skip-me,zeta/app,zeta/skip-me" {
		t.Fatalf("discovered = %v", slugs)
	}
	// A substring filter narrows the listing.
	filtered, err := service.discoverRemoteMergePolicyFleet("alpha/app", nil, nil, false)
	if err != nil || len(filtered) != 1 || filtered[0].Slug() != "alpha/app" {
		t.Fatalf("filtered = %+v, %v", filtered, err)
	}
	// includeUser resolves only the authenticated user, no orgs.
	onlyUser, err := service.discoverRemoteMergePolicyFleet("", nil, nil, true)
	if err != nil || len(onlyUser) != 2 {
		t.Fatalf("includeUser = %+v, %v", onlyUser, err)
	}
	// Explicit owners skip discovery entirely.
	explicit, err := service.discoverRemoteMergePolicyFleet("", []string{" beta ", ""}, nil, false)
	if err != nil || len(explicit) != 2 || !strings.Contains(explicit[0].Slug(), "beta/") {
		t.Fatalf("explicit owners = %+v, %v", explicit, err)
	}

	// A failing authentication is named, never treated as no owners.
	service.deps.AuthUser = func() (string, error) { return "", errors.New("gh missing") }
	if _, err := service.discoverRemoteMergePolicyFleet("", nil, nil, false); err == nil ||
		!strings.Contains(err.Error(), "resolve authenticated GitHub owner") {
		t.Fatalf("auth failure = %v", err)
	}
	if _, err := service.discoverRemoteMergePolicyFleet("", nil, nil, true); err == nil ||
		!strings.Contains(err.Error(), "resolve authenticated GitHub owner") {
		t.Fatalf("includeUser auth failure = %v", err)
	}
	// A failing org listing is named too.
	service.deps.AuthUser = func() (string, error) { return "cwcov-user", nil }
	service.deps.MemberOrgs = func() ([]string, error) { return nil, errors.New("orgs unavailable") }
	if _, err := service.discoverRemoteMergePolicyFleet("", nil, nil, false); err == nil ||
		!strings.Contains(err.Error(), "list authenticated GitHub organizations") {
		t.Fatalf("org listing failure = %v", err)
	}
	// A failing remote listing names the owner.
	service.deps.ListRemote = func(owner string) ([]discover.Repo, error) { return nil, errors.New("rate limited") }
	if _, err := service.discoverRemoteMergePolicyFleet("", []string{"acme"}, nil, false); err == nil ||
		!strings.Contains(err.Error(), "list GitHub repositories for acme") {
		t.Fatalf("remote listing failure = %v", err)
	}
}

// cwDepsMergePolicyGitHubBody builds the three GitHub responses the merge
// policy audit and apply paths consume, so both can run with no live call.
func cwDepsMergePolicyPolicyBody(drift bool) []byte {
	policy := githubRepositoryPolicy{DefaultBranch: "main", AllowMergeCommit: true,
		AllowSquashMerge: false, AllowRebaseMerge: false, MergeCommitTitle: "PR_TITLE", MergeCommitMessage: "PR_BODY"}
	if drift {
		policy.AllowMergeCommit = false
	}
	raw, _ := json.Marshal(policy)
	return raw
}

const cwDepsMergePolicyProtectionBody = `{
  "required_status_checks": {"strict": true, "checks": [{"context": "ci", "app_id": 7}]},
  "enforce_admins": {"enabled": true},
  "required_pull_request_reviews": {"dismiss_stale_reviews": true, "require_code_owner_reviews": false, "required_approving_review_count": 1},
  "restrictions": {"users": [{"login": "octocat"}], "teams": [{"slug": "core"}], "apps": [{"slug": "ci"}]},
  "required_linear_history": {"enabled": true},
  "allow_force_pushes": {"enabled": false},
  "allow_deletions": {"enabled": false},
  "block_creations": {"enabled": false},
  "required_conversation_resolution": {"enabled": true},
  "lock_branch": {"enabled": false},
  "allow_fork_syncing": {"enabled": true}
}`

const cwDepsMergePolicyRulesBody = `[
  {"type": "required_linear_history", "ruleset_source_type": "Repository", "ruleset_source": "acme/app", "ruleset_id": 7, "parameters": {}},
  {"type": "pull_request", "ruleset_source_type": "Repository", "ruleset_source": "acme/app", "ruleset_id": 8,
   "parameters": {"allowed_merge_methods": ["squash"]}}
]`

func cwDepsMergePolicyRulesetBody() []byte {
	return []byte(`{"name": "main policy", "target": "branch",
	  "rules": [
	    {"type": "required_linear_history"},
	    {"type": "pull_request", "parameters": {"required_approving_review_count": 1}},
	    {"type": "deletion"}
	  ]}`)
}

// cwDepsStubMergePolicyGitHub replaces the package's GitHub seams for the
// duration of a test and returns a call recorder.
func cwDepsStubMergePolicyGitHub(service *Service, t *testing.T, mutate func(call int, endpoint string) []byte) *[]string {
	t.Helper()
	previousRead, previousExecute, previousDiscover := service.deps.Read, service.deps.Execute, service.deps.Discover
	var calls []string
	counter := 0
	t.Cleanup(func() {
		service.deps.Read, service.deps.Execute, service.deps.Discover = previousRead, previousExecute, previousDiscover
	})
	service.deps.Discover = func(string, string, []string, []string, bool) ([]discover.Repo, error) {
		return []discover.Repo{{Org: "acme", Name: "app", Remote: true}}, nil
	}
	service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
		counter++
		calls = append(calls, endpoint)
		if mutate != nil {
			if body := mutate(counter, endpoint); body != nil {
				return body, nil
			}
		}
		switch {
		case strings.Contains(endpoint, "/rulesets/"):
			return cwDepsMergePolicyRulesetBody(), nil
		case strings.Contains(endpoint, "/rules/branches/"):
			return []byte(cwDepsMergePolicyRulesBody), nil
		case strings.Contains(endpoint, "/protection"):
			return []byte(cwDepsMergePolicyProtectionBody), nil
		default:
			return cwDepsMergePolicyPolicyBody(true), nil
		}
	}
	service.deps.Execute = func(context.Context, ...string) githubobserver.CommandResponse {
		return githubobserver.CommandResponse{}
	}
	return &calls
}
