package main

import (
	"bytes"
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
	"github.com/spf13/cobra"
)

func cwDepsMergePolicyReportFixture() mergePolicyReport {
	return mergePolicyReport{
		SchemaVersion: mergePolicySchemaVersion, Mode: "check",
		Summary: mergePolicySummary{Inspected: 4, Compliant: 1, Drift: 1, Blocked: 1, Errors: 1},
		Repositories: []mergePolicyRepository{
			{Repository: "acme/clean", Disposition: "compliant"},
			{Repository: "acme/drift", Disposition: "drift", Drift: []string{"allow_squash_merge=true"}},
			{Repository: "acme/conflict", Disposition: "blocked", Conflicts: []string{"repository ruleset 7 (acme): requires linear history"}},
			{Repository: "acme/broken", Disposition: "error", Error: "decode repository settings: unexpected EOF"},
		},
		Rulesets: []mergePolicyRulesetChange{{SourceType: "Organization", Source: "acme", ID: 9, Repositories: []string{"acme/a", "acme/b"}, Disposition: "planned"}},
	}
}

func TestCwDepsPrintMergePolicyReportRendersEveryDisposition(t *testing.T) {
	report := cwDepsMergePolicyReportFixture()
	var out bytes.Buffer
	if err := printMergePolicyReport(&out, report); err != nil {
		t.Fatalf("printMergePolicyReport: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"Merge policy: check",
		"4 repositories · 1 compliant · 1 drift · 1 blocked · 1 errors · 0 applied",
		"compliant acme/clean",
		"merge commits only; PR title + PR body",
		"drift     acme/drift",
		"blocked   acme/conflict",
		"error     acme/broken",
		"decode repository settings",
		"organization ruleset 9 (2 selected)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("merge policy report missing %q:\n%s", want, text)
		}
	}
	// A persisted report names its own path.
	report.ReportPath = "/tmp/merge-policy.json"
	out.Reset()
	if err := printMergePolicyReport(&out, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Report: /tmp/merge-policy.json") {
		t.Errorf("report path missing:\n%s", out.String())
	}
	if err := printMergePolicyReport(cwDepsFailingWriter{}, report); err == nil {
		t.Error("a failed report write must be surfaced")
	}
}

func TestCwDepsMergePolicyReportPath(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "policy")
	path, err := mergePolicyReportPath(explicit)
	if err != nil || path != filepath.Join(explicit, "merge-policy.json") {
		t.Fatalf("explicit report path = %q, %v", path, err)
	}
	if _, statErr := os.Stat(explicit); statErr != nil {
		t.Errorf("explicit report directory was not created: %v", statErr)
	}
	// An explicit path that cannot be a directory is an error.
	blocker := filepath.Join(t.TempDir(), "a-file")
	cwCovWriteFile(t, blocker, "not a directory\n")
	if _, err := mergePolicyReportPath(filepath.Join(blocker, "child")); err == nil {
		t.Error("a report directory beneath a file must fail")
	}
	// No explicit path defaults beneath the WB home.
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)
	previousRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousRoot })
	derived, err := mergePolicyReportPath("")
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
	applied := mergePolicyReport{Repositories: []mergePolicyRepository{{Disposition: "applied"}, {Disposition: "something-else"}}}
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
			if got := githubCommandMessage(test.response); got != test.want {
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
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	repos, err := discoverRemoteMergePolicyFleet("", []string{"acme/a"}, []string{"acme/b", "acme/b", "zebra/c", " acme/d "}, false)
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
	if filtered, err := discoverRemoteMergePolicyFleet("acme/", nil, []string{"acme/b", "zebra/c"}, false); err != nil || len(filtered) != 1 || filtered[0].Slug() != "acme/b" {
		t.Fatalf("filtered exact repositories = %+v, %v", filtered, err)
	}
	for _, invalid := range []string{"noslash", "/name", "owner/", "owner/a/b"} {
		if _, err := discoverRemoteMergePolicyFleet("", nil, []string{invalid}, false); err == nil ||
			!strings.Contains(err.Error(), "invalid --repo") {
			t.Fatalf("invalid --repo %q error = %v", invalid, err)
		}
	}
}

// TestCwDepsDiscoverRemoteMergePolicyFleetStubsDiscovery drives the owner
// resolution and remote listing through the package's own injectable seams, so
// no live GitHub call is made.
func TestCwDepsDiscoverRemoteMergePolicyFleetStubsDiscovery(t *testing.T) {
	previousUser, previousOrgs, previousList := mergePolicyAuthUser, mergePolicyMemberOrgs, mergePolicyListRemote
	t.Cleanup(func() {
		mergePolicyAuthUser, mergePolicyMemberOrgs, mergePolicyListRemote = previousUser, previousOrgs, previousList
	})

	mergePolicyAuthUser = func() (string, error) { return "cwcov-user", nil }
	mergePolicyMemberOrgs = func() ([]string, error) { return []string{"zeta", "alpha"}, nil }
	mergePolicyListRemote = func(owner string) ([]discover.Repo, error) {
		return []discover.Repo{{Org: owner, Name: "app"}, {Org: owner, Name: "skip-me"}}, nil
	}
	repos, err := discoverRemoteMergePolicyFleet("", nil, nil, false)
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
	filtered, err := discoverRemoteMergePolicyFleet("alpha/app", nil, nil, false)
	if err != nil || len(filtered) != 1 || filtered[0].Slug() != "alpha/app" {
		t.Fatalf("filtered = %+v, %v", filtered, err)
	}
	// includeUser resolves only the authenticated user, no orgs.
	onlyUser, err := discoverRemoteMergePolicyFleet("", nil, nil, true)
	if err != nil || len(onlyUser) != 2 {
		t.Fatalf("includeUser = %+v, %v", onlyUser, err)
	}
	// Explicit owners skip discovery entirely.
	explicit, err := discoverRemoteMergePolicyFleet("", []string{" beta ", ""}, nil, false)
	if err != nil || len(explicit) != 2 || !strings.Contains(explicit[0].Slug(), "beta/") {
		t.Fatalf("explicit owners = %+v, %v", explicit, err)
	}

	// A failing authentication is named, never treated as no owners.
	mergePolicyAuthUser = func() (string, error) { return "", errors.New("gh missing") }
	if _, err := discoverRemoteMergePolicyFleet("", nil, nil, false); err == nil ||
		!strings.Contains(err.Error(), "resolve authenticated GitHub owner") {
		t.Fatalf("auth failure = %v", err)
	}
	if _, err := discoverRemoteMergePolicyFleet("", nil, nil, true); err == nil ||
		!strings.Contains(err.Error(), "resolve authenticated GitHub owner") {
		t.Fatalf("includeUser auth failure = %v", err)
	}
	// A failing org listing is named too.
	mergePolicyAuthUser = func() (string, error) { return "cwcov-user", nil }
	mergePolicyMemberOrgs = func() ([]string, error) { return nil, errors.New("orgs unavailable") }
	if _, err := discoverRemoteMergePolicyFleet("", nil, nil, false); err == nil ||
		!strings.Contains(err.Error(), "list authenticated GitHub organizations") {
		t.Fatalf("org listing failure = %v", err)
	}
	// A failing remote listing names the owner.
	mergePolicyListRemote = func(owner string) ([]discover.Repo, error) { return nil, errors.New("rate limited") }
	if _, err := discoverRemoteMergePolicyFleet("", []string{"acme"}, nil, false); err == nil ||
		!strings.Contains(err.Error(), "list GitHub repositories for acme") {
		t.Fatalf("remote listing failure = %v", err)
	}
}

func TestCwDepsRequestedMergePolicyOwnersReadsTheRootOrg(t *testing.T) {
	command := newFleetMergePolicyCmd()
	if got := requestedMergePolicyOwners(command, []string{"local"}); len(got) != 1 || got[0] != "local" {
		t.Fatalf("command-local owners = %v", got)
	}
	root := &cobra.Command{Use: "wb"}
	root.PersistentFlags().StringArray("org", nil, "additional owner")
	mergePolicy := newFleetMergePolicyCmd()
	root.AddCommand(mergePolicy)
	if err := root.PersistentFlags().Set("org", "root-org"); err != nil {
		t.Fatal(err)
	}
	previous := extraOrgs
	extraOrgs = []string{"root-org"}
	t.Cleanup(func() { extraOrgs = previous })
	got := requestedMergePolicyOwners(mergePolicy, []string{"local"})
	if len(got) != 2 || got[1] != "root-org" {
		t.Fatalf("root owners = %v", got)
	}
}

func TestCwDepsMergePolicyCommandUsageRefusals(t *testing.T) {
	root := t.TempDir()
	cwCovFakeGH(t, "cwcov-user", nil, `[]`)
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	tests := map[string]struct {
		args []string
		want string
	}{
		"repo with org":        {[]string{"--repo", "acme/app", "--org", "acme"}, "--repo cannot be combined"},
		"zero parallel":        {[]string{"--parallel", "0"}, "--parallel must be between"},
		"resume without apply": {[]string{"--resume", "--report-dir", "/tmp/r"}, "--resume requires --apply"},
		"apply without scope":  {[]string{"--apply"}, "--apply requires explicit"},
		"invalid repo":         {[]string{"--repo", "not-a-slug"}, "invalid --repo"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			stdout, _, err := cwCovExec(t, root, newFleetMergePolicyCmd, test.args...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("args %v error = %v, want %q\n%s", test.args, err, test.want, stdout)
			}
		})
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
func cwDepsStubMergePolicyGitHub(t *testing.T, mutate func(call int, endpoint string) []byte) *[]string {
	t.Helper()
	previousRead, previousExecute, previousDiscover := mergePolicyRead, mergePolicyExecute, mergePolicyDiscover
	var calls []string
	counter := 0
	t.Cleanup(func() {
		mergePolicyRead, mergePolicyExecute, mergePolicyDiscover = previousRead, previousExecute, previousDiscover
	})
	mergePolicyDiscover = func(string, string, []string, []string, bool) ([]discover.Repo, error) {
		return []discover.Repo{{Org: "acme", Name: "app", Remote: true}}, nil
	}
	mergePolicyRead = func(_ context.Context, endpoint string) ([]byte, error) {
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
	mergePolicyExecute = func(context.Context, ...string) githubobserver.CommandResponse {
		return githubobserver.CommandResponse{}
	}
	return &calls
}

func TestCwDepsMergePolicyApplyWithStubbedGitHub(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	reportDir := filepath.Join(t.TempDir(), "reports")
	calls := cwDepsStubMergePolicyGitHub(t, nil)

	stdout, _, err := cwCovExec(t, t.TempDir(), newFleetMergePolicyCmd,
		"--apply", "--repo", "acme/app", "--parallel", "1", "--report-dir", reportDir, "--format", "json")
	if err != nil {
		t.Fatalf("merge-policy --apply: %v\n%s", err, stdout)
	}
	var report mergePolicyReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("merge-policy JSON: %v\n%s", err, stdout)
	}
	if report.Mode != "apply" || report.Summary.Applied != 1 || report.Summary.Compliant != 0 {
		t.Fatalf("applied report = %+v", report)
	}
	if len(report.Repositories) != 1 || report.Repositories[0].Disposition != "applied" {
		t.Fatalf("repository = %+v", report.Repositories)
	}
	for _, want := range []string{"updated_repository_merge_settings", "removed_classic_required_linear_history"} {
		found := false
		for _, action := range report.Repositories[0].AppliedActions {
			if action == want {
				found = true
			}
		}
		if !found {
			t.Errorf("applied actions %v missing %q", report.Repositories[0].AppliedActions, want)
		}
	}
	// Both repository rulesets carry policy that has to change: one requires
	// linear history and one only admits squash merges.
	if len(report.Rulesets) != 2 {
		t.Fatalf("ruleset changes = %+v", report.Rulesets)
	}
	for _, change := range report.Rulesets {
		if change.Disposition != "applied" {
			t.Errorf("ruleset change = %+v", change)
		}
	}
	if _, statErr := os.Stat(filepath.Join(reportDir, "merge-policy.json")); statErr != nil {
		t.Errorf("the applied report was not persisted: %v", statErr)
	}
	if calls == nil || len(*calls) == 0 {
		t.Error("the GitHub read seam was never exercised")
	}
}

func TestCwDepsMergePolicyApplyBlocksARepositoryThatChanged(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	// The repository settings differ on the re-read inside the apply phase, so
	// the plan is refused rather than applied against a moved target.
	cwDepsStubMergePolicyGitHub(t, func(call int, endpoint string) []byte {
		if endpoint == "repos/acme/app" && call > 2 {
			return cwDepsMergePolicyPolicyBody(false)
		}
		return nil
	})
	stdout, _, err := cwCovExec(t, t.TempDir(), newFleetMergePolicyCmd,
		"--apply", "--repo", "acme/app", "--parallel", "1", "--format", "json")
	if err == nil {
		t.Fatalf("a repository that changed after planning must be blocked:\n%s", stdout)
	}
	var report mergePolicyReport
	if jsonErr := json.Unmarshal([]byte(stdout), &report); jsonErr != nil {
		t.Fatalf("merge-policy JSON: %v\n%s", jsonErr, stdout)
	}
	if report.Summary.Blocked == 0 {
		t.Fatalf("report = %+v", report.Summary)
	}
}

func TestCwDepsMergePolicyApplyReportsAMutationFailure(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	previousExecute := mergePolicyExecute
	t.Cleanup(func() { mergePolicyExecute = previousExecute })
	cwDepsStubMergePolicyGitHub(t, nil)
	mergePolicyExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		if len(args) > 2 && args[2] == "PATCH" {
			return githubobserver.CommandResponse{Stderr: []byte("gh: patch refused"), Err: errors.New("exit status 1")}
		}
		return githubobserver.CommandResponse{}
	}
	stdout, _, err := cwCovExec(t, t.TempDir(), newFleetMergePolicyCmd,
		"--apply", "--repo", "acme/app", "--parallel", "1", "--format", "json")
	if err == nil {
		t.Fatalf("a refused mutation must be an error:\n%s", stdout)
	}
	var report mergePolicyReport
	if jsonErr := json.Unmarshal([]byte(stdout), &report); jsonErr != nil {
		t.Fatalf("merge-policy JSON: %v\n%s", jsonErr, stdout)
	}
	if report.Summary.Errors == 0 || !strings.Contains(report.Repositories[0].Error, "patch refused") {
		t.Fatalf("report = %+v", report.Repositories)
	}
}
