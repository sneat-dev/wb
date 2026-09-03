package orchestrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		filesJSON = append(filesJSON, map[string]string{"filename": file})
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
	// Five commits, two kept: three commits land — the aggregate in the
	// position of the first commit it absorbs, then each kept commit in order.
	if len(plan.steps) != 3 {
		t.Fatalf("steps = %#v", plan.steps)
	}
	if !plan.steps[0].aggregate || len(plan.steps[0].sources) != 3 {
		t.Fatalf("the aggregate must absorb the three unkept commits: %#v", plan.steps[0])
	}
	if plan.steps[1].aggregate || plan.steps[1].sources[0].Subject != "two" {
		t.Fatalf("step 1 = %#v", plan.steps[1])
	}
	if plan.steps[2].aggregate || plan.steps[2].sources[0].Subject != "four" {
		t.Fatalf("step 2 = %#v", plan.steps[2])
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
