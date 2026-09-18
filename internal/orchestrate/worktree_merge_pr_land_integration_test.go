package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wmEngineGH is a scripted GitHub for the worktree-merge PR route's shared
// engine (landWorktreeMergePullRequest / awaitLandablePullRequest /
// mergeOrAdoptAutoMerge). Unlike installWorktreeMergePublishOnlyPRGH, it
// answers every call the shared engine itself makes: PUT merge, PUT
// update-branch, the GraphQL auto-merge mutation, and a real
// required_status_checks fence — so the route can be driven all the way to
// a genuine "landed" outcome, not stopped at the fence the older, narrower
// fixture always hit.
type wmEngineGH struct {
	fixture engineFixture
	state   string
	log     string
	pr      string
}

func installWorktreeMergeEngineGH(t *testing.T, fixture engineFixture, candidateSHA, candidateBranch string) *wmEngineGH {
	t.Helper()
	// The fake GitHub commits directly in the remote (update-branch merges,
	// "GitHub merges while WB is away"), so the bare remote needs a git
	// identity of its own: a CI runner has no global one to fall back on.
	runEngineGit(t, fixture.repository.CloneURL, "config", "user.name", "WB Test")
	runEngineGit(t, fixture.repository.CloneURL, "config", "user.email", "wb@example.test")

	root := t.TempDir()
	gh := &wmEngineGH{
		fixture: fixture,
		state:   filepath.Join(root, "state"),
		log:     filepath.Join(root, "gh.log"),
		pr:      "https://example.test/acme/app/pull/41",
	}
	if err := os.MkdirAll(gh.state, 0o755); err != nil {
		t.Fatal(err)
	}
	gh.writeState(t, "pr-state", "OPEN")
	gh.writeState(t, "merged", "false")
	gh.writeState(t, "head", candidateSHA)
	gh.writeState(t, "head-ref", candidateBranch)
	gh.writeState(t, "check-conclusion", "success")

	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$WB_TEST_GH_LOG"
S="$WB_TEST_STATE"
head_ref=$(cat "$S/head-ref")
# The candidate branch's real ref is the source of truth for the pull
# request's observed head, exactly as real GitHub reports whatever was last
# pushed to it - a plain "git push" to the branch (no gh call at all) must be
# visible here too, not only the update-branch/merge endpoints below that
# also happen to write $S/head.
if git --git-dir="$WB_TEST_REMOTE" show-ref --verify --quiet "refs/heads/$head_ref"; then
  head=$(git --git-dir="$WB_TEST_REMOTE" rev-parse "refs/heads/$head_ref")
else
  head=$(cat "$S/head")
fi
state=$(cat "$S/pr-state")
merged=$(cat "$S/merged")
merge_sha=""
if [ "$merged" = true ]; then merge_sha=$(git --git-dir="$WB_TEST_REMOTE" rev-parse refs/heads/main); fi
case "$*" in
  'api repos/acme/app/branches/main --include'|'api repos/acme/app/branches/main')
    printf '%s\n' '{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"CI","app_id":42}]}}}' ;;
  'api repos/acme/app/branches/main/protection/required_status_checks --include'|'api repos/acme/app/branches/main/protection/required_status_checks')
    if [ -f "$S/unfenced" ]; then strict=false; else strict=true; fi
    printf '{"strict":%s,"contexts":[],"checks":[{"context":"CI","app_id":42}]}\n' "$strict" ;;
  'api repos/acme/app/rules/branches/main?per_page=100 --include'|'api repos/acme/app/rules/branches/main?per_page=100') printf '%s\n' '[]' ;;
  'pr list --head '*' --base main --state open --json url --jq .[0].url')
    # Once this fixture's own 'pr create' has answered once, the PR genuinely
    # exists at this branch (per pr-created below): report it, exactly as a
    # real GitHub would, so a later resume adopts it instead of opening a
    # second pull request for the same still-open candidate.
    if [ "$state" = OPEN ] && [ -f "$S/pr-created" ]; then
      printf '%s\n' "$WB_TEST_PR"
    else
      printf '%s\n' "${WB_TEST_ADOPT_URL:-}"
    fi ;;
  'pr create --base main --head '*)
    printf '1' >"$S/pr-created"
    printf '%s\n' "$WB_TEST_PR" ;;
  'api repos/acme/app/pulls/41 --include'|'api repos/acme/app/pulls/41')
    printf '{"number":41,"node_id":"PR_kwDOtest41","state":"%s","draft":false,"locked":false,"title":"candidate","body":"","merged":%s,"merge_commit_sha":"%s","mergeable":true,"mergeable_state":"clean","head":{"ref":"%s","sha":"%s","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}\n' \
      "$state" "$merged" "$merge_sha" "$head_ref" "$head" ;;
  'pr view https://example.test/acme/app/pull/41 --repo acme/app --json state,mergedAt,mergeCommit,headRefOid,baseRefName')
    if [ "$merged" = true ]; then
      printf '{"state":"MERGED","mergedAt":"2026-09-18T00:00:00Z","headRefOid":"%s","baseRefName":"main","mergeCommit":{"oid":"%s"}}\n' "$head" "$merge_sha"
    else
      printf '{"state":"%s","mergedAt":"","headRefOid":"%s","baseRefName":"main","mergeCommit":{"oid":""}}\n' "$state" "$head"
    fi ;;
  'pr view https://example.test/acme/app/pull/41 --repo acme/app --json state,headRefOid,baseRefName')
    printf '{"state":"%s","headRefOid":"%s","baseRefName":"main"}\n' "$state" "$head" ;;
  *'/check-runs?per_page=100 --include'|*'/check-runs?per_page=100')
    if [ -f "$S/advance-on-checks" ]; then
      rm -f "$S/advance-on-checks"
      tip=$(git --git-dir="$WB_TEST_REMOTE" rev-parse refs/heads/main)
      next=$(git --git-dir="$WB_TEST_REMOTE" commit-tree "$(git --git-dir="$WB_TEST_REMOTE" rev-parse "$tip^{tree}")" -p "$tip" -m "landed mid-wait")
      git --git-dir="$WB_TEST_REMOTE" update-ref refs/heads/main "$next"
    fi
    printf '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"%s","app":{"id":42}}]}\n' "$(cat "$S/check-conclusion")" ;;
  *'/status?per_page=100 --include'|*'/status?per_page=100') printf '%s\n' '{"total_count":0,"statuses":[]}' ;;
  'api --paginate repos/acme/app/commits/'*'/pulls') printf '%s\n' "${WB_TEST_EXISTING_PR_JSON:-[]}" ;;
  *'/pulls?per_page=100 --include'|*'/pulls?per_page=100') printf '%s\n' "${WB_TEST_EXISTING_PR_JSON:-[]}" ;;
  'api repos/acme/app/commits/'*)
    sha="${2#*commits/}"
    parents=$(git --git-dir="$WB_TEST_REMOTE" log -1 --pretty=%P "$sha" 2>/dev/null || true)
    parents_json=""
    for p in $parents; do
      if [ -n "$parents_json" ]; then parents_json="$parents_json,"; fi
      parents_json="$parents_json{\"sha\":\"$p\"}"
    done
    printf '{"sha":"%s","parents":[%s]}\n' "$sha" "$parents_json" ;;
  'api repos/acme/app --include'|'api repos/acme/app')
    printf '%s\n' "${WB_TEST_REPO_JSON:-{\"allow_merge_commit\":true,\"allow_squash_merge\":false,\"allow_rebase_merge\":false}}" ;;
  'api repos/acme/app/git/ref/heads/main --include'|'api repos/acme/app/git/ref/heads/main')
    printf '{"object":{"sha":"%s"}}\n' "$(git --git-dir="$WB_TEST_REMOTE" rev-parse refs/heads/main)" ;;
  'api repos/acme/app/git/ref/heads/'*)
    ref="${2#*git/ref/heads/}"; ref="${ref% --include}"
    if git --git-dir="$WB_TEST_REMOTE" show-ref --verify --quiet "refs/heads/$ref"; then
      printf '{"object":{"sha":"%s"}}\n' "$(git --git-dir="$WB_TEST_REMOTE" rev-parse "refs/heads/$ref")"
    else
      printf '{"message":"Not Found"}\n'; exit 1
    fi ;;
  'api --method DELETE repos/acme/app/git/refs/heads/'*)
    ref="${4#*git/refs/heads/}"
    git --git-dir="$WB_TEST_REMOTE" update-ref -d "refs/heads/$ref" ;;
  'api repos/acme/app/compare/'*)
    pair="${2#*compare/}"
    left="${pair%%...*}"
    right="${pair#*...}"
    if git --git-dir="$WB_TEST_REMOTE" merge-base --is-ancestor "$left" "$right" 2>/dev/null; then
      status=ahead
      if [ "$(git --git-dir="$WB_TEST_REMOTE" rev-parse "$left")" = "$(git --git-dir="$WB_TEST_REMOTE" rev-parse "$right")" ]; then status=identical; fi
    else
      status=behind
    fi
    printf '{"status":"%s","base_commit":{"sha":"%s"},"merge_base_commit":{"sha":"%s"}}\n' \
      "$status" "$(git --git-dir="$WB_TEST_REMOTE" rev-parse "$left")" "$(git --git-dir="$WB_TEST_REMOTE" merge-base "$left" "$right" 2>/dev/null || git --git-dir="$WB_TEST_REMOTE" rev-parse "$left")" ;;
  'api --method PUT repos/acme/app/pulls/41/merge'*)
    if [ "$(cat "$S/merged")" = true ]; then
      printf '{"message":"Pull Request is not mergeable"}\n'; exit 1
    fi
    requested=""
    for arg in "$@"; do
      case "$arg" in sha=*) requested="${arg#sha=}" ;; esac
    done
    if [ -n "$requested" ] && [ "$requested" != "$head" ]; then
      printf '{"message":"Head branch was modified. Review and try the merge again."}\n'; exit 1
    fi
    printf '%s' "$*" >"$S/merge-args"
    git --git-dir="$WB_TEST_REMOTE" update-ref refs/heads/main "$head"
    printf 'true' >"$S/merged"
    printf 'CLOSED' >"$S/pr-state"
    printf '%s\n' "$(whoami 2>/dev/null || echo wb-test)" >"$S/merged-by"
    printf '{"sha":"%s","merged":true,"message":"Pull Request successfully merged"}\n' "$head" ;;
  'api graphql'*)
    case "$*" in
      *enablePullRequestAutoMerge*)
        if [ -f "$S/auto-merge-unavailable" ]; then
          printf '{"errors":[{"message":"Pull request Auto merge is not allowed for this repository"}]}\n' >&2
          exit 1
        fi
        printf 'armed' >"$S/auto-merge"
        printf '%s' "$*" >"$S/auto-merge-args"
        if [ -f "$S/github-merges-on-arm" ]; then
          git --git-dir="$WB_TEST_REMOTE" update-ref refs/heads/main "$head"
          printf 'true' >"$S/merged"
          printf 'CLOSED' >"$S/pr-state"
          printf 'github-actions' >"$S/merged-by"
        fi
        printf '{"data":{"enablePullRequestAutoMerge":{"pullRequest":{"autoMergeRequest":{"enabledAt":"2026-09-18T00:00:00Z"}}}}}\n' ;;
      *) echo "unexpected graphql: $*" >&2; exit 2 ;;
    esac ;;
  'api --method PUT repos/acme/app/pulls/41/update-branch'*)
    if [ -f "$S/update-conflict" ]; then
      printf '{"message":"merge conflict between base and head"}\n' >&2; exit 1
    fi
    expected=""
    for arg in "$@"; do
      case "$arg" in expected_head_sha=*) expected="${arg#expected_head_sha=}" ;; esac
    done
    if [ -n "$expected" ] && [ "$expected" != "$head" ]; then
      printf '{"message":"expected head sha didn'"'"'t match current head ref"}\n' >&2; exit 1
    fi
    merged_commit=$(git --git-dir="$WB_TEST_REMOTE" commit-tree "$(git --git-dir="$WB_TEST_REMOTE" rev-parse "$head^{tree}")" \
      -p "$head" -p "$(git --git-dir="$WB_TEST_REMOTE" rev-parse refs/heads/main)" -m "Merge main into $head_ref")
    git --git-dir="$WB_TEST_REMOTE" update-ref "refs/heads/$head_ref" "$merged_commit"
    printf '%s' "$merged_commit" >"$S/head"
    printf 'updated' >"$S/update-branch"
    printf '{"message":"Updating pull request branch.","url":"https://api.github.com/repos/acme/app/pulls/41"}\n' ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(withEmptyActionsRuns(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_STATE", gh.state)
	t.Setenv("WB_TEST_GH_LOG", gh.log)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	t.Setenv("WB_TEST_PR", gh.pr)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return gh
}

func (gh *wmEngineGH) writeState(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(gh.state, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (gh *wmEngineGH) readState(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(gh.state, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func (gh *wmEngineGH) ghLog(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(gh.log)
	if err != nil {
		return ""
	}
	return string(raw)
}

func wmEngineLandOptions(fixture engineFixture, receiptPath string) WorktreeMergeLandOptions {
	return WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receiptPath, Route: WorktreeMergeRoutePullRequest,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	}
}

// TestLandWorktreeMergePullRequestRouteDelegatesToTheSharedEngine drives an
// ordinary PR-route landing end to end through ResumeWorktreeMerge (from a
// freshly prepared, unpublished receipt) and asserts the engine-delegation
// contract itself: the gh log shows the shared engine's own calls -
// `api --method PUT .../pulls/41/merge` with `sha=`, and never the retired
// `pr merge` CLI route - auto-merge is armed before the wait, the receipt
// records AutoMergeArmed, and the landing completes through post-target
// checks, canonical sync, and (with Cleanup requested) asset cleanup.
func TestLandWorktreeMergePullRequestRouteDelegatesToTheSharedEngine(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "engine-delegates-source", "feature/engine-delegates", "engine-delegates.txt", "delegates\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)

	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	options.Cleanup = true
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("landing via the shared engine failed: receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeComplete {
		t.Fatalf("landed receipt status = %s, want complete: %+v", landed.Status, landed)
	}
	if !landed.AutoMergeArmed {
		t.Fatalf("receipt did not record AutoMergeArmed: %+v", landed)
	}
	// MergedBy is only recorded when GitHub's own auto-merge won the race;
	// an ordinary WB-driven merge (this test) leaves it empty by design -
	// the GitHub-mid-wait scenario below is what proves MergedBy itself.
	log := gh.ghLog(t)
	if !strings.Contains(log, "api --method PUT repos/acme/app/pulls/41/merge") || !strings.Contains(log, "sha=") {
		t.Fatalf("gh log did not show the shared engine's PUT merge call:\n%s", log)
	}
	if strings.Contains(log, "pr merge") {
		t.Fatalf("gh log shows the retired `pr merge` CLI route:\n%s", log)
	}
	if gh.readState(t, "auto-merge") != "armed" {
		t.Fatalf("auto-merge was never armed")
	}
}

// TestLandWorktreeMergePullRequestGitHubMergesDuringTheWaitReportsLanded
// covers auto-merge winning the race while WB is still polling checks: the
// route must still resolve to landed, with MergedBy set and post-target
// checks, canonical sync, and cleanup all still running.
func TestLandWorktreeMergePullRequestGitHubMergesDuringTheWaitReportsLanded(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "merges-on-arm-source", "feature/merges-on-arm", "merges-on-arm.txt", "arm\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	gh.writeState(t, "github-merges-on-arm", "1")

	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("landing did not tolerate GitHub merging mid-wait: receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded {
		t.Fatalf("landed receipt status = %s, want landed_cleanup_pending: %+v", landed.Status, landed)
	}
	if landed.MergedBy != "github auto-merge" {
		t.Fatalf("receipt did not record the GitHub-side merger: %+v", landed)
	}
	if landed.LandingSHA == "" || landed.CanonicalSync == "" {
		t.Fatalf("post-target checks/sync did not run after a GitHub-side merge: %+v", landed)
	}
	log := gh.ghLog(t)
	if strings.Contains(log, "pr merge") {
		t.Fatalf("gh log shows the retired `pr merge` CLI route:\n%s", log)
	}
}

// TestLandWorktreeMergePullRequestUpdatesABehindCandidate covers the
// update-branch path: the candidate is behind main, the shared engine
// updates it server-side, and the receipt must record the exact new
// Candidate.SHA/PublishedCandidateSHA/TargetSHA plus a TargetRefreshes
// entry, with the local candidate worktree fast-forwarded to match.
func TestLandWorktreeMergePullRequestUpdatesABehindCandidate(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "behind-source", "feature/behind", "behind.txt", "behind\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	// A candidate already behind at resume-start is refreshed by WB's own
	// pre-existing local rebase-and-repush of the published PR branch
	// (refreshPublishedWorktreeMergeCandidateTarget), never by the shared
	// engine's server-side update-branch call. To exercise that call
	// specifically, main must advance only after the shared engine's wait
	// has already begun: advance-on-checks moves it from inside the first
	// check-runs poll, exactly as another landing would mid-wait.
	gh.writeState(t, "advance-on-checks", "1")

	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("landing did not tolerate an update-branch advance: receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded {
		t.Fatalf("landed receipt status = %s, want landed_cleanup_pending: %+v", landed.Status, landed)
	}
	if len(landed.TargetRefreshes) == 0 {
		t.Fatalf("receipt did not record a TargetRefreshes entry for the update-branch advance: %+v", landed)
	}
	refresh := landed.TargetRefreshes[0]
	if landed.Candidate.SHA != refresh.NewCandidateSHA || landed.PublishedCandidateSHA != refresh.NewCandidateSHA || landed.TargetSHA != refresh.NewTargetSHA {
		t.Fatalf("receipt did not adopt the update-branch advance exactly: receipt=%+v refresh=%+v", landed, refresh)
	}
	if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); got != refresh.NewCandidateSHA {
		t.Fatalf("candidate worktree was not fast-forwarded to the update-branch advance: got=%s want=%s", got, refresh.NewCandidateSHA)
	}
	if gh.readState(t, "update-branch") != "updated" {
		t.Fatalf("update-branch was never called")
	}
}

// TestLandWorktreeMergePullRequestChecksPendingKeepsAutoMergeArmed covers a
// wait that times out while checks are still pending: the route must fail
// with WorktreeMergeChecksPending and a message that says auto-merge stays
// armed.
func TestLandWorktreeMergePullRequestChecksPendingKeepsAutoMergeArmed(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "pending-source", "feature/pending", "pending.txt", "pending\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	gh.writeState(t, "check-conclusion", "")
	gh.writeState(t, "check-conclusion-status", "in_progress")

	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	options.Timeout = 200 * time.Millisecond
	pending, err := ResumeWorktreeMerge(context.Background(), options)
	if err == nil || pending.Status != WorktreeMergeChecksPending {
		t.Fatalf("pending checks did not stop at checks_pending: receipt=%+v err=%v", pending, err)
	}
	if !strings.Contains(pending.Failure, "auto-merge is armed") {
		t.Fatalf("checks-pending failure did not say auto-merge stays armed: %q", pending.Failure)
	}
	if !pending.AutoMergeArmed {
		t.Fatalf("receipt did not record AutoMergeArmed while checks were pending: %+v", pending)
	}
}

// TestLandWorktreeMergePullRequestUpdateConflictGivesConflictWithResumeGuidance
// covers the update-branch call itself failing with a genuine conflict: the
// route must report WorktreeMergeConflict with resume guidance naming the
// receipt.
func TestLandWorktreeMergePullRequestUpdateConflictGivesConflictWithResumeGuidance(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "update-conflict-source", "feature/update-conflict", "update-conflict.txt", "conflict\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	// As in the behind-candidate test above: a candidate already behind at
	// resume-start is refreshed by WB's own pre-existing local rebase, never
	// by the shared engine. advance-on-checks moves main only once the
	// engine's own wait has begun, so its own update-branch call - not the
	// local rebase - is what hits the conflict.
	gh.writeState(t, "advance-on-checks", "1")
	gh.writeState(t, "update-conflict", "1")

	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	conflicted, err := ResumeWorktreeMerge(context.Background(), options)
	if err == nil || conflicted.Status != WorktreeMergeConflict {
		t.Fatalf("update-branch conflict did not report conflict: receipt=%+v err=%v", conflicted, err)
	}
	if !strings.Contains(conflicted.Failure, "wb worktree merge resume") {
		t.Fatalf("conflict failure did not carry resume guidance: %q", conflicted.Failure)
	}
}

// TestResumeWorktreeMergePullRequestUpdateRecordedContinues covers resume
// scenario (a): the update-branch advance was already recorded on the
// receipt (TargetRefreshes non-empty, matching Candidate.SHA), the PR is
// still open at that exact head, and resume simply continues to a landing.
func TestResumeWorktreeMergePullRequestUpdateRecordedContinues(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "recorded-update-source", "feature/recorded-update", "recorded-update.txt", "recorded\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	// Publish first (stop-before-merge), so a PR genuinely exists at the
	// exact candidate head before simulating the already-recorded advance.
	published, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("publish-only stop failed: %+v %v", published, err)
	}
	// Simulate an update-branch merge that happened and was already
	// recorded (persist-then-fast-forward, M3's non-crash path): advance the
	// remote candidate branch to a real merge commit over main, and record
	// the matching receipt fields as adoptWorktreeMergeUpdateBranchAdvance
	// would have.
	runEngineGit(t, published.Candidate.Worktree, "fetch", "origin", "main")
	runEngineGit(t, published.Candidate.Worktree, "merge", "--no-ff", "-m", "merge main", "origin/main")
	mergedSHA := strings.TrimSpace(runEngineGit(t, published.Candidate.Worktree, "rev-parse", "HEAD"))
	runEngineGit(t, published.Candidate.Worktree, "push", "origin", published.Candidate.Branch)
	published.TargetRefreshes = append(published.TargetRefreshes, WorktreeMergeTargetRefresh{
		RecordedAt: time.Now().UTC(), PreviousTargetSHA: published.TargetSHA, NewTargetSHA: published.TargetSHA,
		PreviousCandidateSHA: published.Candidate.SHA, NewCandidateSHA: mergedSHA,
	})
	published.Candidate.SHA = mergedSHA
	published.PublishedCandidateSHA = mergedSHA
	if err := persistWorktreeMergeReceipt(published); err != nil {
		t.Fatal(err)
	}
	gh.writeState(t, "head", mergedSHA)

	options := wmEngineLandOptions(fixture, published.ReceiptPath)
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("resume with a recorded update-branch advance did not continue: receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded {
		t.Fatalf("landed receipt status = %s, want landed_cleanup_pending: %+v", landed.Status, landed)
	}
}

// TestResumeWorktreeMergePullRequestForeignPushThenGitHubMergeIsLanded
// covers M4: a foreign push moved the candidate worktree's local branch
// pointer away from the receipt's recorded head (so a naive comparison
// would call it a conflict), but GitHub itself already merged the exact
// recorded candidate. Resume must report landed, not conflict.
func TestResumeWorktreeMergePullRequestForeignPushThenGitHubMergeIsLanded(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "foreign-push-source", "feature/foreign-push", "foreign-push.txt", "foreign\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	published, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("publish-only stop failed: %+v %v", published, err)
	}
	// GitHub merges the exact recorded candidate.
	runEngineGit(t, fixture.canonical, "checkout", "main")
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", "origin/"+published.Candidate.Branch)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	gh.writeState(t, "merged", "true")
	gh.writeState(t, "pr-state", "CLOSED")
	gh.writeState(t, "merged-by", "github-actions")

	options := wmEngineLandOptions(fixture, published.ReceiptPath)
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("resume after a foreign push and a GitHub-side merge did not land: receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded {
		t.Fatalf("landed receipt status = %s, want landed_cleanup_pending: %+v", landed.Status, landed)
	}
}

// TestResumeWorktreeMergePullRequestMergedWhileAwayLandsWithSyncAndCleanup
// covers resume scenario (d): GitHub's armed auto-merge landed the exact
// published candidate while WB was not running at all (no wait in
// progress), the candidate worktree is still present, and a plain resume
// must recognize the already-merged head, then still run post-target
// checks, canonical sync, and (with Cleanup requested) asset cleanup.
func TestResumeWorktreeMergePullRequestMergedWhileAwayLandsWithSyncAndCleanup(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "merged-away-source", "feature/merged-away", "merged-away.txt", "away\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	published, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("publish-only stop failed: %+v %v", published, err)
	}
	// GitHub's own auto-merge (armed by an earlier, now-gone WB run) landed
	// the exact recorded candidate while nothing was watching: main already
	// sits at the candidate head, and the PR already reports merged/closed.
	runEngineGit(t, fixture.canonical, "checkout", "main")
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", "origin/"+published.Candidate.Branch)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	gh.writeState(t, "merged", "true")
	gh.writeState(t, "pr-state", "CLOSED")

	options := wmEngineLandOptions(fixture, published.ReceiptPath)
	options.Cleanup = true
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("resume after an unattended GitHub auto-merge did not land: receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeComplete {
		t.Fatalf("landed receipt status = %s, want complete: %+v", landed.Status, landed)
	}
	// The candidate already sits ff-only on main by local ancestry alone, so
	// this resume proves the landing from that local proof without ever
	// asking GitHub who merged it (no PUT merge/graphql call in the log) -
	// MergedBy is therefore this scenario's genuinely-empty case; the
	// GitHub-side-merger scenario is what TestLandWorktreeMergePullRequestGitHubMergesDuringTheWaitReportsLanded proves.
	if landed.CanonicalSync == "" || len(landed.CleanedTasks) == 0 {
		t.Fatalf("post-target sync/cleanup did not run after an unattended GitHub-side merge: %+v", landed)
	}
}

// TestResumeWorktreeMergePullRequestAdoptsAnUnrecordedServerMergeAdvance
// covers red-team finding M5's positive case (resume scenario (b), adopted
// branch): an update-branch merge happened on the server but was never
// recorded on the receipt (WB crashed between the write and the persist).
// A real ordinary merge of the recorded candidate and the current target -
// first parent the candidate, second parent an ancestor of the target, tree
// exactly the ordinary merge of the two - must be adopted on resume.
func TestResumeWorktreeMergePullRequestAdoptsAnUnrecordedServerMergeAdvance(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "unrecorded-update-source", "feature/unrecorded-update", "unrecorded-update.txt", "unrecorded\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	published, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("publish-only stop failed: %+v %v", published, err)
	}
	// Advance main, then perform the exact merge an update-branch call would
	// have performed server-side - directly on the remote, with no receipt
	// update at all, simulating the crash M5 exists to recover from.
	writeEngineFile(t, filepath.Join(fixture.canonical, "advance-main-unrecorded.txt"), "advance\n")
	runEngineGit(t, fixture.canonical, "add", "advance-main-unrecorded.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "advance main")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	runEngineGit(t, published.Candidate.Worktree, "fetch", "origin", "main")
	runEngineGit(t, published.Candidate.Worktree, "merge", "--no-ff", "-m", "merge main", "origin/main")
	mergedSHA := strings.TrimSpace(runEngineGit(t, published.Candidate.Worktree, "rev-parse", "HEAD"))
	runEngineGit(t, published.Candidate.Worktree, "push", "origin", published.Candidate.Branch)
	gh.writeState(t, "head", mergedSHA)
	// The receipt itself still names the OLD candidate head - exactly the
	// unrecorded crash state.

	options := wmEngineLandOptions(fixture, published.ReceiptPath)
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("resume did not adopt the unrecorded server-side merge advance: receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded {
		t.Fatalf("landed receipt status = %s, want landed_cleanup_pending: %+v", landed.Status, landed)
	}
	if landed.Candidate.SHA != mergedSHA {
		t.Fatalf("resume did not adopt the exact unrecorded merge advance: got=%s want=%s", landed.Candidate.SHA, mergedSHA)
	}
}

// TestResumeWorktreeMergePullRequestDoesNotAdoptAMismatchedTreeAsUpdateBranch
// covers red-team finding M5's negative case: the pull request's head has
// the recorded candidate as its first parent and an ancestor of the target
// as its second parent - the shape an update-branch merge has - but its
// tree is NOT the ordinary merge of those two parents (some other change
// rode along). Adoption must refuse this head; the ordinary conflict
// handling must judge it instead of a silent adoption.
func TestResumeWorktreeMergePullRequestDoesNotAdoptAMismatchedTreeAsUpdateBranch(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "mismatched-tree-source", "feature/mismatched-tree", "mismatched-tree.txt", "mismatch\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	published, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("publish-only stop failed: %+v %v", published, err)
	}
	writeEngineFile(t, filepath.Join(fixture.canonical, "advance-main-mismatch.txt"), "advance\n")
	runEngineGit(t, fixture.canonical, "add", "advance-main-mismatch.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "advance main")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	// Perform the mismatched-tree merge in a throwaway clone, not in
	// published.Candidate.Worktree itself: that worktree is WB's own tracked
	// checkout, and mutating its HEAD directly (instead of only the remote
	// branch a real foreign push would move) would feed WB's own local
	// drift/revalidation machinery a fact this test does not intend to set -
	// only the remote branch ref, and the fixture's derived "head", should
	// move.
	clone := t.TempDir()
	runEngineGit(t, fixture.githubDir, "clone", fixture.repository.CloneURL, clone)
	runEngineGit(t, clone, "config", "user.name", "WB Test")
	runEngineGit(t, clone, "config", "user.email", "wb@example.test")
	runEngineGit(t, clone, "checkout", published.Candidate.Branch)
	// A merge of the candidate and the new target, exactly the right parents
	// - but with one extra unrelated change riding along, so its tree is NOT
	// the ordinary merge-tree of those two parents.
	runEngineGit(t, clone, "merge", "--no-ff", "-m", "merge main", "origin/main")
	writeEngineFile(t, filepath.Join(clone, "rode-along.txt"), "rode along\n")
	runEngineGit(t, clone, "add", "rode-along.txt")
	runEngineGit(t, clone, "commit", "--amend", "--no-edit")
	mismatchedSHA := strings.TrimSpace(runEngineGit(t, clone, "rev-parse", "HEAD"))
	runEngineGit(t, clone, "push", "origin", published.Candidate.Branch)
	gh.writeState(t, "head", mismatchedSHA)

	options := wmEngineLandOptions(fixture, published.ReceiptPath)
	notAdopted, err := ResumeWorktreeMerge(context.Background(), options)
	if err == nil || notAdopted.Status != WorktreeMergeConflict {
		t.Fatalf("mismatched-tree head was adopted instead of judged as a conflict: receipt=%+v err=%v", notAdopted, err)
	}
	if notAdopted.Candidate.SHA == mismatchedSHA {
		t.Fatalf("mismatched-tree head was adopted onto the receipt: %+v", notAdopted)
	}
}
