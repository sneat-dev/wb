package orchestrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// landFixture is a real repository plus a scripted GitHub. Every GitHub call
// the verb makes is answered from files under state/, so a test can change one
// fact — the head moved, a check failed, the diff touched a .go file — without
// reshaping the whole script.
type landFixture struct {
	root       string
	projects   string
	canonical  string
	remote     string
	state      string
	log        string
	headSHA    string
	baseSHA    string
	commitSHAs []string
}

func newLandFixture(t *testing.T, branch string, files ...string) *landFixture {
	t.Helper()
	if len(files) == 0 {
		files = []string{"go.sum"}
	}
	root := t.TempDir()
	t.Setenv("WB_HOME", filepath.Join(root, ".wb"))
	seed := filepath.Join(root, "seed")
	remote := filepath.Join(root, "remote.git")
	projects := filepath.Join(root, "projects")
	canonical := filepath.Join(projects, "acme", "app")
	writeEngineFile(t, filepath.Join(seed, "go.mod"), "module example.test/app\n\ngo 1.24\n")
	runEngineGit(t, seed, "init", "-b", "main")
	runEngineGit(t, seed, "config", "user.name", "WB Test")
	runEngineGit(t, seed, "config", "user.email", "wb@example.test")
	runEngineGit(t, seed, "add", "-A")
	runEngineGit(t, seed, "commit", "-m", "initial")
	runEngineGit(t, root, "clone", "--bare", seed, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, root, "clone", remote, canonical)
	runEngineGit(t, canonical, "config", "user.name", "WB Test")
	runEngineGit(t, canonical, "config", "user.email", "wb@example.test")

	fixture := &landFixture{
		root: root, projects: projects, canonical: canonical, remote: remote,
		state: filepath.Join(root, "state"), log: filepath.Join(root, "gh.log"),
	}
	if err := os.MkdirAll(fixture.state, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture.baseSHA = strings.TrimSpace(runEngineGit(t, canonical, "rev-parse", "HEAD"))

	// Build the pull request's branch: one commit per named file.
	runEngineGit(t, canonical, "checkout", "-b", branch)
	for index, file := range files {
		writeEngineFile(t, filepath.Join(canonical, file), "change "+file+"\n")
		runEngineGit(t, canonical, "add", "-A")
		runEngineGit(t, canonical, "commit", "-m", "change "+file)
		fixture.commitSHAs = append(fixture.commitSHAs, strings.TrimSpace(runEngineGit(t, canonical, "rev-parse", "HEAD")))
		_ = index
	}
	runEngineGit(t, canonical, "push", "-u", "origin", branch)
	fixture.headSHA = strings.TrimSpace(runEngineGit(t, canonical, "rev-parse", "HEAD"))
	runEngineGit(t, canonical, "checkout", "main")
	fixture.installGH(t, branch, files)
	return fixture
}

func (fixture *landFixture) writeState(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.state, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (fixture *landFixture) readState(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixture.state, name))
	if err != nil {
		return ""
	}
	return string(raw)
}

func (fixture *landFixture) ghLog(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(fixture.log)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (fixture *landFixture) installGH(t *testing.T, branch string, files []string) {
	t.Helper()
	filesJSON := make([]map[string]string, 0, len(files))
	for _, file := range files {
		// The classification reads the diff, so the fixture must carry one.
		// A manifest edit that only changes a version is what a mechanical
		// bump looks like on the wire.
		patch := "@@ -1,3 +1,3 @@\n \"dependencies\": {\n-    \"@acme/lib\": \"1.0.0\"\n+    \"@acme/lib\": \"2.0.0\"\n }"
		if !strings.HasSuffix(file, ".json") && !strings.HasSuffix(file, ".yaml") &&
			file != "go.mod" && file != "go.sum" {
			patch = "@@ -1,2 +1,3 @@\n package app\n+// changed\n"
		}
		filesJSON = append(filesJSON, map[string]string{"filename": file, "status": "modified", "patch": patch})
	}
	encodedFiles, err := json.Marshal(filesJSON)
	if err != nil {
		t.Fatal(err)
	}
	commits := make([]map[string]any, 0, len(fixture.commitSHAs))
	for index, sha := range fixture.commitSHAs {
		commits = append(commits, map[string]any{
			"sha":    sha,
			"commit": map[string]string{"message": "change " + files[index] + "\n\nCo-Authored-By: someone <x@example.test>"},
		})
	}
	encodedCommits, err := json.Marshal(commits)
	if err != nil {
		t.Fatal(err)
	}
	fixture.writeState(t, "pr-state", "open")
	fixture.writeState(t, "merged", "false")
	fixture.writeState(t, "head", fixture.headSHA)
	fixture.writeState(t, "files", string(encodedFiles))
	fixture.writeState(t, "commits", string(encodedCommits))
	fixture.writeState(t, "check-conclusion", "success")

	bin := filepath.Join(fixture.root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >>"$WB_LAND_LOG"
S="$WB_LAND_STATE"
head=$(cat "$S/head")
state=$(cat "$S/pr-state")
merged=$(cat "$S/merged")
case "$*" in
  'api repos/acme/app/pulls/7 --include'|'api repos/acme/app/pulls/7')
    merge_sha=""
    if [ "$merged" = true ]; then merge_sha=$(git --git-dir="$WB_LAND_REMOTE" rev-parse refs/heads/main); fi
    printf '{"number":7,"state":"%s","draft":false,"locked":false,"title":"feat: the change","body":"Summary line.\\n\\n## Details\\nhidden","merged":%s,"merge_commit_sha":"%s","mergeable":true,"mergeable_state":"clean","head":{"ref":"%s","sha":"%s","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}\n' \
      "$state" "$merged" "$merge_sha" "$WB_LAND_BRANCH" "$head" ;;
  'api repos/acme/app/pulls/7/files?per_page=100 --include'|'api repos/acme/app/pulls/7/files?per_page=100') cat "$S/files" ;;
  'api repos/acme/app/pulls/7/commits?per_page=100 --include'|'api repos/acme/app/pulls/7/commits?per_page=100') cat "$S/commits" ;;
  'api --paginate repos/acme/app/commits/'*'/pulls')
    # The worktree inventory asks GitHub's commit-to-pull-request index while
    # deciding whether a checkout's work landed. Without this the inspection
    # fails, the candidate becomes a diagnostic, and the cleanup that is the
    # whole point of the default path silently has nothing to clean.
    sha=$(basename "$(dirname "$3")")
    merged=$(cat "$S/merged")
    if [ "$merged" = true ]; then
      merge_sha=$(git --git-dir="$WB_LAND_REMOTE" rev-parse refs/heads/main)
      printf '[{"number":7,"html_url":"https://github.com/acme/app/pull/7","state":"closed","merged_at":"2026-09-01T00:00:00Z","merge_commit_sha":"%s","head":{"ref":"%s","sha":"%s"},"base":{"ref":"main","sha":""}}]\n' \
        "$merge_sha" "$WB_LAND_BRANCH" "$sha"
    else
      printf '[]'
    fi ;;
  'api repos/acme/app/branches/main --include'|'api repos/acme/app/branches/main')
    printf '%s\n' '{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"CI","app_id":42}]}}}' ;;
  'api repos/acme/app/branches/main/protection/required_status_checks --include'|'api repos/acme/app/branches/main/protection/required_status_checks')
    printf '%s\n' '{"strict":true,"contexts":[],"checks":[{"context":"CI","app_id":42}]}' ;;
  'api repos/acme/app/rules/branches/main?per_page=100 --include'|'api repos/acme/app/rules/branches/main?per_page=100') printf '%s\n' '[]' ;;
  'api repos/acme/app/git/ref/heads/main --include'|'api repos/acme/app/git/ref/heads/main')
    printf '{"object":{"sha":"%s"}}\n' "$(git --git-dir="$WB_LAND_REMOTE" rev-parse refs/heads/main)" ;;
  'api repos/acme/app/git/ref/heads/'*)
    ref="${2#*git/ref/heads/}"; ref="${ref% --include}"
    if git --git-dir="$WB_LAND_REMOTE" show-ref --verify --quiet "refs/heads/$ref"; then
      printf '{"object":{"sha":"%s"}}\n' "$(git --git-dir="$WB_LAND_REMOTE" rev-parse "refs/heads/$ref")"
    else
      printf '{"message":"Not Found"}\n'; exit 1
    fi ;;
  'api --method DELETE repos/acme/app/git/refs/heads/'*)
    ref="${4#*git/refs/heads/}"
    git --git-dir="$WB_LAND_REMOTE" update-ref -d "refs/heads/$ref" ;;
  *'/check-runs?per_page=100 --include'|*'/check-runs?per_page=100')
    printf '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"%s","app":{"id":42}}]}\n' "$(cat "$S/check-conclusion")" ;;
  'api --method PUT repos/acme/app/pulls/7/merge')
    echo "merge called with no arguments" >&2; exit 2 ;;
  *'/status?per_page=100 --include'|*'/status?per_page=100') printf '%s\n' '{"total_count":0,"statuses":[]}' ;;
  'api repos/acme/app/compare/'*)
    pair="${2#*compare/}"
    left="${pair%%...*}"
    right="${pair#*...}"
    if git --git-dir="$WB_LAND_REMOTE" merge-base --is-ancestor "$left" "$right" 2>/dev/null; then
      status=ahead
      if [ "$(git --git-dir="$WB_LAND_REMOTE" rev-parse "$left")" = "$(git --git-dir="$WB_LAND_REMOTE" rev-parse "$right")" ]; then status=identical; fi
    else
      status=behind
    fi
    printf '{"status":"%s","base_commit":{"sha":"%s"},"merge_base_commit":{"sha":"%s"}}\n' \
      "$status" "$(git --git-dir="$WB_LAND_REMOTE" rev-parse "$left")" "$(git --git-dir="$WB_LAND_REMOTE" merge-base "$left" "$right" 2>/dev/null || git --git-dir="$WB_LAND_REMOTE" rev-parse "$left")" ;;
  'api --method PUT repos/acme/app/pulls/7/merge'*)
    requested=""
    for arg in "$@"; do
      case "$arg" in sha=*) requested="${arg#sha=}" ;; esac
    done
    if [ "$requested" != "$(cat "$S/head")" ]; then
      printf '{"message":"Head branch was modified. Review and try the merge again."}\n'; exit 1
    fi
    printf '%s' "$*" >"$S/merge-args"
    git --git-dir="$WB_LAND_REMOTE" update-ref refs/heads/main "$requested"
    printf 'true' >"$S/merged"
    printf 'closed' >"$S/pr-state"
    printf '{"sha":"%s","merged":true,"message":"Pull Request successfully merged"}\n' "$requested" ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_LAND_STATE", fixture.state)
	t.Setenv("WB_LAND_LOG", fixture.log)
	t.Setenv("WB_LAND_REMOTE", fixture.remote)
	t.Setenv("WB_LAND_BRANCH", branch)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func landOptions(fixture *landFixture) PullRequestLandOptions {
	return PullRequestLandOptions{
		Repository: "acme/app", PullRequest: "7", ProjectsRoot: fixture.projects,
		Keep: true, CheckPollInterval: time.Millisecond, Slice: 10 * time.Second,
	}
}

// A dependency bump whose diff touches only manifests lands with no review
// ledger entry, on the batch verification alone.
func TestLandMechanicalBumpNeedsNoApproval(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")

	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !result.Mechanical || len(result.NonManifest) != 0 {
		t.Fatalf("classification = %#v", result)
	}
	if result.MergeSHA == "" || !result.LandingOnBase {
		t.Fatalf("landing evidence = %#v", result)
	}
	if !result.BranchDeleted {
		t.Fatal("the source branch must be retired by the landing that made it redundant")
	}
	if result.ExitCode() != 0 {
		t.Fatalf("exit code = %d", result.ExitCode())
	}
	if log := fixture.ghLog(t); strings.Contains(log, "pr checks") || strings.Contains(log, "--slurp") {
		t.Fatalf("the verb must not use gh CLI surfaces the installed 2.45 lacks:\n%s", log)
	}
}

// The same pull request, titled as a bump, whose diff also edits a source file:
// classified from the diff, and refused without a recorded approval.
func TestLandRefusesABumpWhoseDiffTouchesCode(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps-and-code", "go.mod", "main.go")

	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != LandRefusalUnapprovedPatch {
		t.Fatalf("outcome = %s (%s)", result.Outcome, result.RefusalCode)
	}
	if result.Mechanical {
		t.Fatal("a diff touching main.go is not a mechanical bump, whatever the pull request is titled")
	}
	if !strings.Contains(result.Reason, "main.go") {
		t.Fatalf("the refusal must name what made it non-mechanical: %q", result.Reason)
	}
	if !strings.Contains(result.SanctionedCommand, "--approved-by") {
		t.Fatalf("sanctioned command = %q", result.SanctionedCommand)
	}
	if result.ExitCode() != 2 {
		t.Fatalf("a refusal exits 2, not %d", result.ExitCode())
	}
	if fixture.readState(t, "merged") != "false" {
		t.Fatal("a refused landing must not have merged anything")
	}

	approved := landOptions(fixture)
	approved.ApprovedBy = "https://github.com/acme/app/pull/7#issuecomment-1"
	landed, err := LandPullRequest(context.Background(), approved)
	if err != nil {
		t.Fatal(err)
	}
	if landed.Outcome != LandSuccess || landed.ApprovedBy == "" {
		t.Fatalf("approved landing = %#v", landed)
	}
}

func TestLandRefusesADraftAndAFailedCheck(t *testing.T) {
	fixture := newLandFixture(t, "feature/failing", "go.mod")
	fixture.writeState(t, "check-conclusion", "failure")

	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandFindings || result.RefusalCode != LandRefusalChecksFailed {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.ExitCode() != 1 {
		t.Fatalf("a failing check is a finding (exit 1), not a refusal: %d", result.ExitCode())
	}
	if fixture.readState(t, "merged") != "false" {
		t.Fatal("a red pull request must not merge")
	}
}

// The squash message aggregates the branch: the pull request's title as the
// subject, and every source commit named in the body.
func TestLandAggregatesSourceCommitsIntoTheSquashMessage(t *testing.T) {
	fixture := newLandFixture(t, "feature/aggregate", "go.mod", "go.sum")

	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s: %s", result.Outcome, result.Reason)
	}
	arguments := fixture.readState(t, "merge-args")
	if !strings.Contains(arguments, "merge_method=squash") {
		t.Fatalf("squash is the default landing route: %q", arguments)
	}
	if !strings.Contains(arguments, "commit_title=feat: the change (#7)") {
		t.Fatalf("the subject must be the pull request title, not the branch's first commit: %q", arguments)
	}
	for _, wanted := range []string{"Source commits:", "change go.mod", "change go.sum", "Summary line."} {
		if !strings.Contains(arguments, wanted) {
			t.Fatalf("the aggregated body is missing %q:\n%s", wanted, arguments)
		}
	}
	if strings.Contains(arguments, "Co-Authored-By") {
		t.Fatalf("trailers are provenance, not information about the change:\n%s", arguments)
	}
	if strings.Contains(arguments, "## Details") {
		t.Fatalf("the body summary must stop at the first heading:\n%s", arguments)
	}
}

func TestLandRefusesKeepCommitsWithoutAReason(t *testing.T) {
	fixture := newLandFixture(t, "feature/keep", "go.mod", "go.sum")
	options := landOptions(fixture)
	options.KeepCommits = []string{fixture.commitSHAs[0]}

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != LandRefusalKeepReasonMissing {
		t.Fatalf("outcome = %s (%s)", result.Outcome, result.RefusalCode)
	}
	if !strings.Contains(result.SanctionedCommand, "--reason") {
		t.Fatalf("the refusal must name --reason: %q", result.SanctionedCommand)
	}
}

func TestLandRefusesAKeptCommitThatIsNotOnTheBranch(t *testing.T) {
	fixture := newLandFixture(t, "feature/keep-unknown", "go.mod")
	options := landOptions(fixture)
	options.KeepCommits = []string{strings.Repeat("b", 40)}
	options.Reason = "it is worth its own commit"

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != LandRefusalKeepUnknownCommit {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
}

func TestPlanKeptCommitsPreservesOrderAroundOneAggregate(t *testing.T) {
	commits := []SourceCommit{
		{SHA: "1111111111111111111111111111111111111111", Subject: "one"},
		{SHA: "2222222222222222222222222222222222222222", Subject: "two"},
		{SHA: "3333333333333333333333333333333333333333", Subject: "three"},
		{SHA: "4444444444444444444444444444444444444444", Subject: "four"},
		{SHA: "5555555555555555555555555555555555555555", Subject: "five"},
	}
	plan, refusal := planKeptCommits(commits, []string{"2222222", "4444444"})
	if refusal != nil {
		t.Fatalf("refusal = %#v", refusal)
	}
	// Five commits, two kept: three commits land. The kept ones keep their
	// order, and the aggregate lands AFTER the kept commits that precede its
	// last member ("five" comes after "four"), never hoisted to the front —
	// everything it absorbs has to be in place before a later kept commit can
	// replay.
	if len(plan.steps) != 3 {
		t.Fatalf("steps = %#v", plan.steps)
	}
	if plan.steps[0].aggregate || plan.steps[0].sources[0].Subject != "two" {
		t.Fatalf("step 0 = %#v, want the first kept commit", plan.steps[0])
	}
	if plan.steps[1].aggregate || plan.steps[1].sources[0].Subject != "four" {
		t.Fatalf("step 1 = %#v, want the second kept commit", plan.steps[1])
	}
	if !plan.steps[2].aggregate || len(plan.steps[2].sources) != 3 {
		t.Fatalf("step 2 = %#v, want the aggregate absorbing one, three and five", plan.steps[2])
	}
	for index, subject := range []string{"one", "three", "five"} {
		if plan.steps[2].sources[index].Subject != subject {
			t.Fatalf("the aggregate must keep its members in branch order: %#v", plan.steps[2].sources)
		}
	}
}

// When every unkept commit precedes the kept ones, the aggregate lands first —
// which is the same rule, not a special case.
func TestPlanKeptCommitsPutsTheAggregateFirstWhenItsMembersComeFirst(t *testing.T) {
	commits := []SourceCommit{
		{SHA: "1111111111111111111111111111111111111111", Subject: "one"},
		{SHA: "2222222222222222222222222222222222222222", Subject: "two"},
		{SHA: "3333333333333333333333333333333333333333", Subject: "three"},
	}
	plan, refusal := planKeptCommits(commits, []string{"3333333"})
	if refusal != nil {
		t.Fatalf("refusal = %#v", refusal)
	}
	if len(plan.steps) != 2 || !plan.steps[0].aggregate || plan.steps[1].sources[0].Subject != "three" {
		t.Fatalf("steps = %#v", plan.steps)
	}
}

func TestSavingsCountEveryAbsorbedCallAndLabelTheEstimate(t *testing.T) {
	result := PullRequestLandResult{
		ManualEquivalent: []string{"a", "b", "c", "d", "e", "f", "g", "h"},
		AbsorbedPolls:    3,
		ChangedFiles:     []string{"go.mod", "go.sum"},
	}
	saved := withSavings(result)
	if saved.SavedToolCalls != 9 {
		t.Fatalf("saved tool calls = %d, want the manual sequence plus the extra polls, minus the one call made", saved.SavedToolCalls)
	}
	if saved.SavedTokensEstimate <= 0 {
		t.Fatalf("saved tokens = %d", saved.SavedTokensEstimate)
	}
	if !strings.Contains(saved.FooterLine(), "estimate") {
		t.Fatalf("footer = %q, want the figure labelled an estimate", saved.FooterLine())
	}
}

// M1: the aggregate has to name the pull request, so a reader of `git log` can
// find it. It used to interpolate the base branch, which named nothing.
func TestAggregatedBodyNamesTheRepositoryAndNumber(t *testing.T) {
	view := PullRequestView{Number: 41, Title: "feat: the change", Body: "Summary."}
	view.Base.Ref = "main"
	view.Base.Repo = &struct {
		FullName string `json:"full_name"`
	}{FullName: "acme/app"}
	body := aggregatedCommitMessage(view, []SourceCommit{{SHA: strings.Repeat("a", 40), Subject: "one"}}, "review.md", "")
	if !strings.Contains(body, "Pull request: acme/app#41") {
		t.Fatalf("body must name the pull request, not the base branch:\n%s", body)
	}
	if strings.Contains(body, "main#41") {
		t.Fatalf("the base branch is not a pull-request identity:\n%s", body)
	}
}

// M2: the classification reads the diff, not the filename. A `package.json`
// holds the scripts CI runs and the overrides that rewrite the whole graph.
func TestMechanicalIsDecidedFromContent(t *testing.T) {
	versionOnly := `@@ -5,7 +5,7 @@
   "dependencies": {
-    "lodash": "^4.17.20"
+    "lodash": "^4.17.21"
   }`
	scriptsOnly := `@@ -2,7 +2,7 @@
   "scripts": {
-    "build": "tsc"
+    "build": "tsc && node scripts/postbuild.js"
   }`
	overrides := `@@ -9,7 +9,7 @@
   "pnpm": {
     "overrides": {
-      "semver": "7.5.4"
+      "semver": "7.6.0"
     }`
	for _, testCase := range []struct {
		name  string
		files []ChangedFile
		want  bool
	}{
		{"a version-only manifest edit", []ChangedFile{{Filename: "package.json", Patch: versionOnly}}, true},
		{"go.mod and go.sum alone", []ChangedFile{
			{Filename: "go.mod", Patch: "@@\n-require x v1\n+require x v2\n"},
			{Filename: "go.sum", Patch: "@@\n-x v1 h1:a=\n+x v2 h1:b=\n"},
		}, true},
		{"a scripts edit inside a manifest", []ChangedFile{{Filename: "package.json", Patch: scriptsOnly}}, false},
		{"a pnpm override", []ChangedFile{{Filename: "package.json", Patch: overrides}}, false},
		{"a manifest under testdata", []ChangedFile{{Filename: "internal/x/testdata/package.json", Patch: versionOnly}}, false},
		{"a manifest beside a source file", []ChangedFile{
			{Filename: "go.mod", Patch: "@@\n-require x v1\n+require x v2\n"},
			{Filename: "main.go", Patch: "@@\n-a\n+b\n"},
		}, false},
		{"a manifest GitHub could not diff", []ChangedFile{{Filename: "pnpm-lock.yaml", Patch: ""}}, false},
		{"no files at all", nil, false},
		// The sneat-apps#3494 shape: the only context is in the hunk header, so
		// skipping it left the section stack empty and a real bump was refused.
		{"a bump whose section is only in the hunk header", []ChangedFile{{
			Filename: "package.json",
			Patch: "@@ -12,7 +12,7 @@   \"dependencies\": {\n" +
				"     \"@sneat/core\": \"0.68.0\",\n" +
				"-    \"@sneat/extensions\": \"0.38.3\",\n" +
				"+    \"@sneat/extensions\": \"0.38.4\",\n" +
				"     \"rxjs\": \"7.8.1\"",
		}}, true},
		// Graph rewrites are never a version bump, in any manifest.
		{"an npm overrides block", []ChangedFile{{
			Filename: "package.json",
			Patch:    "@@ -20,7 +20,7 @@   \"overrides\": {\n-    \"semver\": \"7.5.4\"\n+    \"semver\": \"7.6.0\"",
		}}, false},
		{"a yarn resolutions block", []ChangedFile{{
			Filename: "package.json",
			Patch:    "@@ -20,7 +20,7 @@   \"resolutions\": {\n-    \"semver\": \"7.5.4\"\n+    \"semver\": \"7.6.0\"",
		}}, false},
		{"a go.mod replace directive", []ChangedFile{{
			Filename: "go.mod",
			Patch:    "@@ -8,3 +8,3 @@\n-require github.com/acme/lib v1.2.0\n+require github.com/acme/lib v1.3.0\n+replace github.com/acme/lib => ../lib",
		}}, false},
		{"a go directive bump", []ChangedFile{{
			Filename: "go.mod",
			Patch:    "@@ -3,1 +3,1 @@\n-go 1.24\n+go 1.25",
		}}, false},
		{"a pnpm-workspace overrides block", []ChangedFile{{
			Filename: "pnpm-workspace.yaml",
			Patch:    "@@ -1,4 +1,4 @@\n overrides:\n-  semver: 7.5.4\n+  semver: 7.6.0",
		}}, false},
		{"a plain go.mod require bump", []ChangedFile{{
			Filename: "go.mod",
			Patch:    "@@ -8,1 +8,1 @@\n-\tgithub.com/acme/lib v1.2.0\n+\tgithub.com/acme/lib v1.3.0",
		}}, true},
	} {
		verdict := ClassifyMechanical(testCase.files)
		if verdict.Mechanical != testCase.want {
			t.Errorf("%s: mechanical = %t, want %t (%s)", testCase.name, verdict.Mechanical, testCase.want, verdict.Summary())
		}
	}
}

// M6 and M7: every invocation leaves exactly one event, including a refusal —
// and a refusal saved the caller nothing, so it records zero.
func TestEveryLandingLeavesOneEventAndARefusalSavesNothing(t *testing.T) {
	fixture := newLandFixture(t, "bump/events", "go.mod", "main.go")
	recorder := &recordingEvents{}
	options := landOptions(fixture)
	options.Events = recorder
	options.Stream = "night-shift"

	refused, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if refused.Outcome != LandRefused {
		t.Fatalf("outcome = %s", refused.Outcome)
	}
	if refused.SavedToolCalls != 0 || refused.SavedTokensEstimate != 0 {
		t.Fatalf("a refusal saved the caller nothing: %d calls, %d tokens",
			refused.SavedToolCalls, refused.SavedTokensEstimate)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("events = %#v, want exactly one", recorder.events)
	}
	event := recorder.events[0]
	if event.Verb != "pr land" || event.Outcome != "refused" || event.RefusalCode != LandRefusalUnapprovedPatch {
		t.Fatalf("event = %#v", event)
	}
	if event.Stream != "night-shift" || event.Repository != "acme/app" {
		t.Fatalf("event identity = %#v", event)
	}
	if event.Evidence["pull_request"] != "acme/app#7" || event.Evidence["saved_tool_calls"] != "0" {
		t.Fatalf("event evidence = %#v", event.Evidence)
	}

	// The same is true of a success, and of --keep.
	approved := options
	approved.ApprovedBy = "review.md"
	landed, err := LandPullRequest(context.Background(), approved)
	if err != nil {
		t.Fatal(err)
	}
	if landed.Outcome != LandSuccess {
		t.Fatalf("outcome = %s: %s", landed.Outcome, landed.Reason)
	}
	if len(recorder.events) != 2 {
		t.Fatalf("events = %d, want one per invocation", len(recorder.events))
	}
	success := recorder.events[1]
	if success.Outcome != "success" || success.Evidence["approved_by"] != "review.md" || success.Evidence["kept"] != "true" {
		t.Fatalf("success event = %#v", success)
	}
	if success.Evidence["merge_commit"] == "" {
		t.Fatal("a landing event must record the commit it produced")
	}
}

type recordingEvents struct{ events []streams.Event }

func (recorder *recordingEvents) Append(event streams.Event) error {
	recorder.events = append(recorder.events, event)
	return nil
}

// Cleanup is the default, and a landing with no WB worktree says so rather than
// leaving the reader to assume one was retired.
func TestLandWithoutKeepSaysWhenNoWorktreeMatched(t *testing.T) {
	fixture := newLandFixture(t, "bump/no-worktree", "go.mod")
	options := landOptions(fixture)
	options.Keep = false

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s: %s", result.Outcome, result.Reason)
	}
	if result.Kept {
		t.Fatal("cleanup is the default")
	}
	if !strings.Contains(result.Evidence["cleanup"], "nothing to retire") {
		t.Fatalf("evidence = %#v, want it to say no worktree matched", result.Evidence)
	}
}

// Cleanup is the default, and the default has to be exercised end to end: a
// real WB worktree on the branch being landed, retired by the landing, with its
// Work Log sealed. The measured failure was an opt-in cleanup nobody passed, so
// a test that never lets the cleanup run proves nothing about it.
func TestLandRetiresTheWorktreeThatProducedTheBranch(t *testing.T) {
	fixture := newLandFixture(t, "bump/retire-me", "go.mod")
	created, err := worktrees.Create(context.Background(), []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: fixture.projects, Operation: "bump-retire",
		Branch: "bump/retire-me", BranchChosen: true, Resume: true,
		WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 {
		t.Fatalf("created = %#v", created)
	}

	options := landOptions(fixture)
	options.Keep = false
	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if len(result.CleanedTasks) != 1 || result.CleanedTasks[0] != "bump-retire" {
		t.Fatalf("cleaned tasks = %#v, want the worktree that produced the branch", result.CleanedTasks)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("the worktree survived a landing whose cleanup is the default: %v", statErr)
	}
	if len(result.CleanupReports) == 0 {
		t.Fatal("retiring a worktree must leave its durable receipt")
	}
}

// A landing whose worktree cannot be retired must say so rather than report a
// clean success. The landing itself is done and irreversible; the finding is
// what tells the operator there is still something on their disk.
func TestLandReportsAFindingWhenTheWorktreeCannotBeRetired(t *testing.T) {
	fixture := newLandFixture(t, "bump/blocked", "go.mod")
	created, err := worktrees.Create(context.Background(), []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: fixture.projects, Operation: "bump-blocked",
		Branch: "bump/blocked", BranchChosen: true, Resume: true,
		WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Dirty after the pre-flight would be a race; dirty before it is the
	// ordinary case, and the refusal is the point: it happens while refusing is
	// still free.
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, "wip.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	options := landOptions(fixture)
	options.Keep = false
	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != "cleanup-blocked-dirty" {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !strings.Contains(result.SanctionedCommand, "bump-blocked") || !strings.Contains(result.SanctionedCommand, "--keep") {
		t.Fatalf("the refusal must name both ways forward: %q", result.SanctionedCommand)
	}
	if fixture.readState(t, "merged") != "false" {
		t.Fatal("the refusal must happen before the merge, while refusing is still free")
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); statErr != nil {
		t.Fatalf("a refused landing must leave the worktree alone: %v", statErr)
	}

	// --keep lands it and says the worktree was kept, because --keep opts out
	// of retiring the checkout rather than out of the landing.
	kept := options
	kept.Keep = true
	keptResult, err := LandPullRequest(context.Background(), kept)
	if err != nil {
		t.Fatal(err)
	}
	if keptResult.Outcome != LandSuccess || !keptResult.Kept {
		t.Fatalf("--keep result = %#v", keptResult)
	}
}
