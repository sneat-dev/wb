package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/testenv"
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
	if [ -f "$S/pr-view-fail-count" ]; then
	  failures=$(cat "$S/pr-view-fail-count")
	  if [ "$failures" -gt 0 ]; then
	    printf '%s' "$((failures - 1))" >"$S/pr-view-fail-count"
	    echo 'gh: Bad Gateway (HTTP 502)' >&2
	    exit 1
	  fi
	fi
    printf '{"number":41,"node_id":"PR_kwDOtest41","state":"%s","draft":false,"locked":false,"title":"candidate","body":"","merged":%s,"merge_commit_sha":"%s","mergeable":true,"mergeable_state":"clean","head":{"ref":"%s","sha":"%s","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}\n' \
      "$state" "$merged" "$merge_sha" "$head_ref" "$head" ;;
  'pr view https://example.test/acme/app/pull/41 --repo acme/app --json state,mergedAt,mergeCommit,headRefOid,baseRefName')
	if [ -f "$S/pr-view-fail-count" ]; then
	  failures=$(cat "$S/pr-view-fail-count")
	  if [ "$failures" -gt 0 ]; then
	    printf '%s' "$((failures - 1))" >"$S/pr-view-fail-count"
	    echo 'gh: Bad Gateway (HTTP 502)' >&2
	    exit 1
	  fi
	fi
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
  'api --paginate repos/acme/app/commits/'*'/pulls')
    if [ -n "${WB_TEST_EXISTING_PR_HEAD:-}" ] && [ "$2" != "repos/acme/app/commits/${WB_TEST_EXISTING_PR_HEAD}/pulls" ]; then
      printf '%s\n' '[]'
    else
      printf '%s\n' "${WB_TEST_EXISTING_PR_JSON:-[]}"
    fi ;;
  *'/pulls?per_page=100 --include'|*'/pulls?per_page=100')
    # WB_TEST_EXISTING_PR_HEAD, when set, scopes the canned response to the
    # exact head this call queried - a real GitHub commits/{sha}/pulls
    # answers per-sha; without this every head in a rev-list walk would
    # otherwise be told it belongs to the same one canned pull request.
    if [ -n "${WB_TEST_EXISTING_PR_HEAD:-}" ] && [ "$2" != "repos/acme/app/commits/${WB_TEST_EXISTING_PR_HEAD}/pulls?per_page=100" ]; then
      printf '%s\n' '[]'
    else
      printf '%s\n' "${WB_TEST_EXISTING_PR_JSON:-[]}"
    fi ;;
  'api repos/acme/app/git/commits/'*)
    # commitTreeSHA (M4): reads a commit's tree by exact SHA from GitHub's
    # own remote state - the fallback verifyUpdateBranchMergeProof uses when
    # the commit is not yet reachable locally (a deleted branch, for
    # example) so a deleted PR branch does not block proving an
    # update-branch merge's tree.
    #
    # Minor 5 (review round on #614): WB_TEST_COMMIT_TREE_TRANSIENT, when its
    # marker file exists, simulates this one read failing transiently on
    # every attempt (a saturated host, never a "not found") so the
    # in-process retry budget genuinely exhausts and
    # verifyUpdateBranchMergeProof's caller sees IsTransientReadFailure(err)
    # - proving the failure is surfaced as retryable, never flattened into
    # an ordinary "not proved".
    if [ -f "${WB_TEST_COMMIT_TREE_TRANSIENT:-/nonexistent}" ]; then
      echo "gh: connection reset by peer" >&2
      exit 1
    fi
    sha="${2#*git/commits/}"
    tree=$(git --git-dir="$WB_TEST_REMOTE" show -s --format=%T "$sha" 2>/dev/null || true)
    if [ -z "$tree" ]; then printf '{"message":"Not Found"}\n'; exit 1; fi
    printf '{"sha":"%s","tree":{"sha":"%s"}}\n' "$sha" "$tree" ;;
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
	if [ -f "$S/merge-transient" ]; then
	  echo 'gh: Bad Gateway (HTTP 502)' >&2
	  exit 1
	fi
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
	if [ -f "$S/merge-transient-after-success" ]; then
	  if [ -f "$S/repeated-transient-reads" ]; then printf '20' >"$S/pr-view-fail-count"; fi
	  echo 'gh: Bad Gateway (HTTP 502)' >&2
	  exit 1
	fi
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
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(withEmptyActionsRuns(script)), 0o755); err != nil {
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

func TestLandWorktreeMergePullRequestLeavesTransientMergeWritePending(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "transient-merge-source", "feature/transient-merge", "transient.txt", "transient\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	gh.writeState(t, "merge-transient", "1")

	pending, err := ResumeWorktreeMerge(context.Background(), wmEngineLandOptions(fixture, receipt.ReceiptPath))
	if err == nil || !errors.Is(err, githubobserver.ErrTransientMutationOutcomeUnknown) {
		t.Fatalf("transient merge write error = %v, want resumable unknown-outcome error", err)
	}
	if pending.Status != WorktreeMergeChecksPending {
		t.Fatalf("transient merge write status = %s, want %s", pending.Status, WorktreeMergeChecksPending)
	}
	if !strings.Contains(err.Error(), "wb worktree merge resume "+receipt.ReceiptPath) {
		t.Fatalf("transient merge write error lacks exact resume command: %v", err)
	}
	persisted, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if readErr != nil || persisted.Status != WorktreeMergeChecksPending {
		t.Fatalf("persisted transient receipt = %+v err=%v", persisted, readErr)
	}
}

func TestLandWorktreeMergePullRequestAdoptsTransientWriteThatSucceeded(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "transient-success-source", "feature/transient-success", "transient-success.txt", "transient success\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	gh.writeState(t, "merge-transient-after-success", "1")

	landed, err := ResumeWorktreeMerge(context.Background(), wmEngineLandOptions(fixture, receipt.ReceiptPath))
	if err != nil {
		t.Fatalf("adopt transient merge write success: receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded || landed.LandingSHA == "" {
		t.Fatalf("adopted transient merge write = %+v, want landed receipt", landed)
	}
}

func TestLandWorktreeMergeRepeatedTransientRecoveryStaysPending(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "repeated-transient-source", "feature/repeated-transient", "repeated.txt", "repeated transient\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	gh.writeState(t, "merge-transient-after-success", "1")
	gh.writeState(t, "repeated-transient-reads", "1")
	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)

	first, firstErr := ResumeWorktreeMerge(context.Background(), options)
	if firstErr == nil || first.Status != WorktreeMergeChecksPending {
		t.Fatalf("first transient recovery = %+v err=%v", first, firstErr)
	}
	second, secondErr := ResumeWorktreeMerge(context.Background(), options)
	if secondErr == nil || !IsTransientGitHubFailure(secondErr) || second.Status != WorktreeMergeChecksPending {
		t.Fatalf("second transient recovery = %+v err=%v, want checks_pending", second, secondErr)
	}
	gh.writeState(t, "pr-view-fail-count", "0")
	third, thirdErr := ResumeWorktreeMerge(context.Background(), options)
	if thirdErr != nil || third.Status != WorktreeMergeLanded || third.LandingSHA == "" {
		t.Fatalf("third recovery did not adopt the exact merged PR: %+v err=%v", third, thirdErr)
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
	// #584 round 3: this wait's slice is hard-capped at 8 minutes regardless
	// of --timeout, so the resume advice must not claim a --timeout budget
	// larger than that (inaccurate advice, unlike `wb pr land`'s own
	// checks-pending resume). It must say plainly that each resume only
	// advances by one more capped slice.
	if strings.Contains(pending.Failure, "--timeout") {
		t.Fatalf("checks-pending failure must not print an inaccurate --timeout budget: %q", pending.Failure)
	}
	if !strings.Contains(pending.Failure, "capped at 8m") {
		t.Fatalf("checks-pending failure must say each resume observes one more capped slice: %q", pending.Failure)
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
// covers red-team findings M4/M-A: a foreign push (never made through this
// receipt's own flow, and never fetched into the candidate worktree) landed
// a genuinely new commit on the candidate branch, GitHub merged that exact
// foreign head, and GitHub then deleted the candidate branch the way it
// deletes a merged pull request's source branch by default. The M4 descent
// check must prove the merged head descends from the recorded candidate via
// the GitHub compare API — not a local `git merge-base`, which would fail
// outright on a deleted branch whose head was never fetched locally at all.
// Resume must report landed, not conflict.
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
	// A genuine foreign push: a THROWAWAY clone (never published.Candidate.Worktree,
	// and never fetched into it) pushes one more real commit onto the
	// candidate branch. This is not a commit this receipt's own flow ever
	// produced or recorded.
	foreignClone := t.TempDir()
	runEngineGit(t, fixture.githubDir, "clone", fixture.repository.CloneURL, foreignClone)
	runEngineGit(t, foreignClone, "config", "user.name", "Foreign Pusher")
	runEngineGit(t, foreignClone, "config", "user.email", "foreign@example.test")
	runEngineGit(t, foreignClone, "checkout", published.Candidate.Branch)
	writeEngineFile(t, filepath.Join(foreignClone, "foreign-push-extra.txt"), "pushed by someone else\n")
	runEngineGit(t, foreignClone, "add", "foreign-push-extra.txt")
	runEngineGit(t, foreignClone, "commit", "-m", "a foreign push WB never made or recorded")
	foreignHead := strings.TrimSpace(runEngineGit(t, foreignClone, "rev-parse", "HEAD"))
	runEngineGit(t, foreignClone, "push", "origin", published.Candidate.Branch)
	gh.writeState(t, "head", foreignHead)

	// GitHub merges the exact foreign head, then deletes the branch — the
	// default for a merged pull request's source branch.
	runEngineGit(t, fixture.canonical, "checkout", "main")
	runEngineGit(t, fixture.canonical, "fetch", "origin", published.Candidate.Branch)
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", "origin/"+published.Candidate.Branch)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	runEngineGit(t, fixture.canonical, "push", "origin", "--delete", published.Candidate.Branch)
	gh.writeState(t, "merged", "true")
	gh.writeState(t, "pr-state", "CLOSED")
	gh.writeState(t, "merged-by", "github-actions")

	// published.Candidate.Worktree never fetched the foreign push or the
	// delete: its local object database has neither the foreign head nor
	// (after the delete) any remote ref for the branch at all, exactly the
	// state a local `git merge-base` cannot reason about.

	options := wmEngineLandOptions(fixture, published.ReceiptPath)
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("resume after a foreign push, a GitHub-side merge, and branch deletion did not land: receipt=%+v err=%v", landed, err)
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

// setUpWorktreeMergeB1CrashState publishes a candidate, then simulates the
// exact B1 crash window: an update-branch advance was recorded onto the
// receipt (persist-first ordering, M3) but its best-effort local
// fast-forward never happened — a crash, a kill, a dirty worktree at the
// time. The candidate worktree is left checked out at the OLD candidate
// (P) while the receipt, and the PR's real head, both name the NEW
// candidate (U). It returns the receipt (still naming U) and U itself.
func setUpWorktreeMergeB1CrashState(t *testing.T, fixture engineFixture, gh *wmEngineGH, published WorktreeMergeReceipt) (WorktreeMergeReceipt, string) {
	t.Helper()
	previousCandidateSHA := published.Candidate.SHA
	previousTargetSHA := published.TargetSHA

	// Perform the update-branch merge in a THROWAWAY clone, never in
	// published.Candidate.Worktree itself — that worktree must stay at the
	// old candidate P, exactly as a crashed fast-forward would leave it.
	clone := t.TempDir()
	runEngineGit(t, fixture.githubDir, "clone", fixture.repository.CloneURL, clone)
	runEngineGit(t, clone, "config", "user.name", "WB Test")
	runEngineGit(t, clone, "config", "user.email", "wb@example.test")
	runEngineGit(t, clone, "checkout", published.Candidate.Branch)
	runEngineGit(t, clone, "merge", "--no-ff", "-m", "merge main", "origin/main")
	newCandidateSHA := strings.TrimSpace(runEngineGit(t, clone, "rev-parse", "HEAD"))
	runEngineGit(t, clone, "push", "origin", published.Candidate.Branch)
	newTargetSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "refs/heads/main"))

	published.TargetRefreshes = append(published.TargetRefreshes, WorktreeMergeTargetRefresh{
		RecordedAt: time.Now().UTC(), PreviousTargetSHA: previousTargetSHA, NewTargetSHA: newTargetSHA,
		PreviousCandidateSHA: previousCandidateSHA, NewCandidateSHA: newCandidateSHA,
	})
	published.TargetSHA = newTargetSHA
	published.Candidate.SHA = newCandidateSHA
	published.PublishedCandidateSHA = newCandidateSHA
	if err := persistWorktreeMergeReceipt(published); err != nil {
		t.Fatal(err)
	}
	gh.writeState(t, "head", newCandidateSHA)

	// The candidate worktree itself is left exactly where it was before the
	// advance — still at P — which is the crash state this test exists for.
	if got := strings.TrimSpace(runEngineGit(t, published.Candidate.Worktree, "rev-parse", "HEAD")); got != previousCandidateSHA {
		t.Fatalf("setup broke its own precondition: worktree HEAD = %s, want the pre-advance candidate %s", got, previousCandidateSHA)
	}
	return published, newCandidateSHA
}

// TestResumeWorktreeMergePullRequestRecoversB1CrashWithPROpen covers
// red-team finding B1: a receipt persisted at the new candidate U while the
// worktree is left at the old candidate P (a crashed or failed best-effort
// fast-forward), with the pull request still open at exact head U. Resume
// must fast-forward the worktree and land, not report Conflict.
func TestResumeWorktreeMergePullRequestRecoversB1CrashWithPROpen(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "b1-open-source", "feature/b1-open", "b1-open.txt", "b1 open\n")
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
	crashed, newCandidateSHA := setUpWorktreeMergeB1CrashState(t, fixture, gh, published)
	// The PR stays open at exact head U — gh's "head" state already reports
	// it via setUpWorktreeMergeB1CrashState.

	options := wmEngineLandOptions(fixture, crashed.ReceiptPath)
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("resume did not recover the B1 crash state (PR open): receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded {
		t.Fatalf("landed receipt status = %s, want landed_cleanup_pending: %+v", landed.Status, landed)
	}
	if got := strings.TrimSpace(runEngineGit(t, crashed.Candidate.Worktree, "rev-parse", "HEAD")); got != newCandidateSHA {
		t.Fatalf("candidate worktree was not fast-forwarded past the B1 crash: got=%s want=%s", got, newCandidateSHA)
	}
}

// TestResumeWorktreeMergePullRequestRecoversB1CrashWithPRMerged covers B1's
// second required case named in the brief: the same persisted-at-U,
// worktree-left-at-P crash state, but GitHub already merged the pull
// request at exact head U while WB was away. Resume must still fast-forward
// and land, not report Conflict on a change GitHub already landed.
func TestResumeWorktreeMergePullRequestRecoversB1CrashWithPRMerged(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "b1-merged-source", "feature/b1-merged", "b1-merged.txt", "b1 merged\n")
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
	crashed, newCandidateSHA := setUpWorktreeMergeB1CrashState(t, fixture, gh, published)
	// GitHub already merged the exact new candidate U while WB was away.
	runEngineGit(t, fixture.canonical, "checkout", "main")
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", "origin/"+crashed.Candidate.Branch)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	gh.writeState(t, "merged", "true")
	gh.writeState(t, "pr-state", "CLOSED")

	options := wmEngineLandOptions(fixture, crashed.ReceiptPath)
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("resume did not recover the B1 crash state (PR merged): receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded {
		t.Fatalf("landed receipt status = %s, want landed_cleanup_pending: %+v", landed.Status, landed)
	}
	if got := strings.TrimSpace(runEngineGit(t, crashed.Candidate.Worktree, "rev-parse", "HEAD")); got != newCandidateSHA {
		t.Fatalf("candidate worktree was not fast-forwarded past the B1 crash: got=%s want=%s", got, newCandidateSHA)
	}
}

// TestResumeWorktreeMergePullRequestRecoversB1CrashWithPRMergedAndDeletedBranch
// covers the required test gap named in #614: B1 with the pull request
// merged was tested only while the branch still existed. GitHub commonly
// deletes a pull request's branch the instant it merges, and the local
// candidate worktree's best-effort fast-forward (fastForwardWorktreeToUpdatedHead)
// fetches that branch BY NAME - so once it is gone, the fast-forward cannot
// succeed. The receipt's own durable record (Candidate.SHA, TargetSHA,
// TargetRefreshes) already names the right state without it: the landing
// must still complete, and the failed fast-forward must be reported as a
// note (M6), never silently dropped and never treated as a Conflict.
func TestResumeWorktreeMergePullRequestRecoversB1CrashWithPRMergedAndDeletedBranch(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "b1-merged-deleted-source", "feature/b1-merged-deleted", "b1-merged-deleted.txt", "b1 merged deleted\n")
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
	// Advance main with a genuine new commit BEFORE the crash-state setup
	// merges it into the candidate: without this, the candidate (branched
	// from main) already contains everything main has, and the setup's own
	// "--no-ff merge origin/main" is a no-op ("Already up to date") that
	// never actually advances the candidate at all - silently defeating
	// this test's whole premise before it reaches the deleted-branch fetch.
	{
		tree := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main^{tree}"))
		parent := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main"))
		commit := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "commit-tree", tree, "-p", parent, "-m", "advance main before B1 crash"))
		runEngineGit(t, fixture.repository.CloneURL, "update-ref", "refs/heads/main", commit)
	}
	crashed, newCandidateSHA := setUpWorktreeMergeB1CrashState(t, fixture, gh, published)
	// GitHub already merged the exact new candidate U while WB was away, AND
	// deleted its branch - both happen together in the ordinary flow.
	runEngineGit(t, fixture.canonical, "fetch", "origin")
	runEngineGit(t, fixture.canonical, "checkout", "main")
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", "origin/main")
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", "origin/"+crashed.Candidate.Branch)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	gh.writeState(t, "merged", "true")
	gh.writeState(t, "pr-state", "CLOSED")
	runEngineGit(t, fixture.repository.CloneURL, "update-ref", "-d", "refs/heads/"+crashed.Candidate.Branch)

	options := wmEngineLandOptions(fixture, crashed.ReceiptPath)
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("resume did not recover the B1 crash state (PR merged, branch deleted): receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded {
		t.Fatalf("landed receipt status = %s, want landed_cleanup_pending: %+v", landed.Status, landed)
	}
	if landed.Candidate.SHA != newCandidateSHA {
		t.Fatalf("receipt candidate = %s, want the recorded new candidate %s", landed.Candidate.SHA, newCandidateSHA)
	}
	// The branch is gone, so the local worktree's own fast-forward cannot
	// succeed - the landing must proceed from the receipt's durable record
	// regardless, and the failed attempt must be noted (M6), not dropped.
	if landed.LocalSync == "" {
		t.Fatal("LocalSync is empty, want a note explaining the fast-forward could not run against a deleted branch")
	}
	if got := strings.TrimSpace(runEngineGit(t, crashed.Candidate.Worktree, "rev-parse", "HEAD")); got == newCandidateSHA {
		t.Fatalf("fixture invariant broken: worktree HEAD %s should not have reached the new candidate without the branch to fetch it from", got)
	}
}

// TestResumeWorktreeMergePullRequestAdoptsUnrecordedAdvanceAfterGitHubDeletesTheBranch
// is the M4 reproduction named in #614: a server-side update-branch merge
// happened but was never recorded (the M5/M-A crash window), and by the time
// `wb worktree merge resume` runs, GitHub has both merged the pull request
// AND deleted its branch - the ordinary "GitHub deletes on merge" behavior.
// adoptServerUpdatedWorktreeMergeHead's own branch-name fetch
// (worktree_merge_pr_land.go, red-team finding M4) then failed outright, so
// the unrecorded advance was never adopted, TargetSHA stayed stale, and a
// later reconcile walked past it into an unrelated already-merged pull
// request's own history, reporting it "absorbed" by this landing. It must
// not be.
func TestResumeWorktreeMergePullRequestAdoptsUnrecordedAdvanceAfterGitHubDeletesTheBranch(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "m4-deleted-branch-source", "feature/m4-deleted-branch", "m4-deleted-branch.txt", "m4\n")
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
	staleTarget := published.TargetSHA

	// An unrelated pull request lands on main after staleTarget - the exact
	// shape M1 (#602) closed for the RECORDED-advance case. A stale
	// TargetSHA here would surface it as absorbed by this landing.
	runEngineGit(t, fixture.canonical, "checkout", "-b", "unrelated-m4", "main")
	writeEngineFile(t, filepath.Join(fixture.canonical, "unrelated-m4.txt"), "unrelated\n")
	runEngineGit(t, fixture.canonical, "add", "-A")
	runEngineGit(t, fixture.canonical, "commit", "-m", "feat: unrelated change")
	unrelatedTip := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "checkout", "main")
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "Merge pull request #99 from acme/unrelated-m4", "unrelated-m4")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	freshTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))

	t.Setenv("WB_TEST_EXISTING_PR_HEAD", unrelatedTip)
	t.Setenv("WB_TEST_EXISTING_PR_JSON", fmt.Sprintf(
		`[{"number":99,"html_url":"https://example.test/acme/app/pull/99","state":"closed","merged":true,"head":{"sha":%q,"ref":"unrelated-m4"},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}]`,
		unrelatedTip))

	// Perform the update-branch merge in a THROWAWAY clone - never in
	// published.Candidate.Worktree itself - so the receipt's own advance
	// stays unrecorded, exactly the M5/M-A crash window this scenario
	// shares. Then GitHub merges the exact new head and deletes the branch.
	clone := t.TempDir()
	runEngineGit(t, fixture.githubDir, "clone", fixture.repository.CloneURL, clone)
	runEngineGit(t, clone, "config", "user.name", "WB Test")
	runEngineGit(t, clone, "config", "user.email", "wb@example.test")
	runEngineGit(t, clone, "checkout", published.Candidate.Branch)
	runEngineGit(t, clone, "merge", "--no-ff", "-m", "merge main", "origin/main")
	mergedSHA := strings.TrimSpace(runEngineGit(t, clone, "rev-parse", "HEAD"))
	runEngineGit(t, clone, "push", "origin", published.Candidate.Branch)
	gh.writeState(t, "head", mergedSHA)

	runEngineGit(t, fixture.canonical, "fetch", "origin")
	runEngineGit(t, fixture.canonical, "checkout", "main")
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", "origin/main")
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", "origin/"+published.Candidate.Branch)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	gh.writeState(t, "merged", "true")
	gh.writeState(t, "pr-state", "CLOSED")
	runEngineGit(t, fixture.repository.CloneURL, "update-ref", "-d", "refs/heads/"+published.Candidate.Branch)
	// The receipt itself still names the OLD candidate head and the stale
	// target - exactly the unrecorded crash state, now compounded by the
	// deleted branch.

	options := wmEngineLandOptions(fixture, published.ReceiptPath)
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("resume did not recover after GitHub merged and deleted the branch: receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded {
		t.Fatalf("landed receipt status = %s, want landed_cleanup_pending: %+v", landed.Status, landed)
	}
	if landed.Candidate.SHA != mergedSHA {
		t.Fatalf("resume did not adopt the exact unrecorded merge advance: got=%s want=%s", landed.Candidate.SHA, mergedSHA)
	}
	if landed.TargetSHA != freshTarget {
		t.Fatalf("TargetSHA did not advance past the update-branch merge's target parent (M4): got=%s want=%s (staleTarget was %s)",
			landed.TargetSHA, freshTarget, staleTarget)
	}
	for _, pr := range landed.SourcePullRequests {
		if pr.Number == 99 {
			t.Fatalf("unrelated already-merged pull request #99 was reported as absorbed by this landing: %+v", landed.SourcePullRequests)
		}
	}
}
