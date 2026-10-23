package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func checkObservationPublishedView(t *testing.T, state, head, target string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"state": state, "headRefOid": head, "baseRefName": target})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

//nolint:paralleltest // PATH/XDG and private provider-record environment are process-wide; all Git mutations belong to this fixture.
func TestCheckObservationOwnerPublishedHandoffUsesNativeRefAndBoundedReads(t *testing.T) {
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "check-observation-source", "feature/check-observation", "observation.txt", "native publication\n")
	head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	base := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, source.WorktreeDir, "push", "origin", "HEAD:refs/heads/feature/check-observation")
	guard, err := worktrees.Guard(t.Context(), source.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: "main"})
	if err != nil || guard.Transient || guard.Branch != "feature/check-observation" {
		t.Fatalf("native source guard = %+v, %v", guard, err)
	}
	view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: source.WorktreeDir})
	if err != nil || view.Claim == nil || view.Claim.Task != "check-observation-source" || view.Claim.Base != "main" || view.Claim.Repository != fixture.repository.Slug || view.Claim.Branch != guard.Branch || view.Git.Head != head {
		t.Fatalf("native source claim = %+v, %v", view, err)
	}
	bin := t.TempDir()
	counter := filepath.Join(t.TempDir(), "observations")
	body := `#!/bin/sh
set -eu
case "$*" in
  'pr view https://example.test/acme/app/pull/17 --repo acme/app --json state,headRefOid,baseRefName')
    n=$(cat "$WB_TEST_S_COUNTER")
    n=$((n + 1))
    printf '%s' "$n" >"$WB_TEST_S_COUNTER"
    if [ "$WB_TEST_S_MODE" = error ]; then echo 'selected read denied' >&2; exit 1; fi
    if [ "$n" -le "$WB_TEST_S_STALE_READS" ]; then printf '%s\n' "$WB_TEST_S_OLD_VIEW"; else printf '%s\n' "$WB_TEST_S_VIEW"; fi ;;
  *) echo "unexpected S provider request: $*" >&2; exit 2 ;;
esac
`
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("WB_TEST_S_COUNTER", counter)
	receipt := WorktreeMergeReceipt{Repository: fixture.repository.Slug, Target: "main", TargetSHA: base, PullRequest: "https://example.test/acme/app/pull/17",
		Candidate: WorktreeMergeCandidate{Worktree: source.WorktreeDir, Branch: guard.Branch, SHA: head}}
	for _, name := range []string{"missing PR", "native ref error", "missing ref", "wrong ref", "read error", "malformed", "closed", "wrong base", "wrong head", "exact", "three stale then exact", "four stale", "failed push gate", "cancel stale wait"} {
		//nolint:paralleltest // Serial provider rows reuse one native read-only publication and process environment.
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(counter, []byte("0"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("WB_TEST_S_MODE", "view")
			t.Setenv("WB_TEST_S_STALE_READS", "0")
			t.Setenv("WB_TEST_S_VIEW", checkObservationPublishedView(t, "OPEN", head, "main"))
			t.Setenv("WB_TEST_S_OLD_VIEW", checkObservationPublishedView(t, "OPEN", base, "main"))
			r := receipt
			opts := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Timeout: 10 * time.Second, CheckPollInterval: 50 * time.Millisecond}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want, reads := "", 1
			var chosen *landOwnerFaultRunner
			sentinel := errors.New("selected native ref observation refused")
			switch name {
			case "missing PR":
				r.PullRequest, want, reads = "", "published handoff has no pull request", 0
			case "native ref error":
				args := []string{"ls-remote", "--heads", "origin", "refs/heads/" + r.Candidate.Branch}
				chosen = &landOwnerFaultRunner{Runner: defaultRunner, failure: sentinel, refuse: func(dir, command string, actual []string) bool {
					return dir == r.Candidate.Worktree && command == "git" && reflect.DeepEqual(actual, args)
				}}
				opts.run, want, reads = chosen, "read published candidate ref", 0
			case "missing ref":
				r.Candidate.Branch, want, reads = "feature/private-absent", "does not match preserved candidate", 0
			case "wrong ref":
				r.Candidate.Branch, want, reads = "main", "does not match preserved candidate", 0
			case "read error":
				t.Setenv("WB_TEST_S_MODE", "error")
				want = "read published pull-request identity"
			case "malformed":
				t.Setenv("WB_TEST_S_VIEW", "{")
				want = "decode published pull-request identity"
			case "closed":
				t.Setenv("WB_TEST_S_VIEW", checkObservationPublishedView(t, "CLOSED", head, "main"))
				want = "is CLOSED, not open"
			case "wrong base":
				t.Setenv("WB_TEST_S_VIEW", checkObservationPublishedView(t, "OPEN", head, "other"))
				want = "does not match target main"
			case "wrong head":
				t.Setenv("WB_TEST_S_VIEW", checkObservationPublishedView(t, "OPEN", base, "main"))
				want = "does not match preserved candidate"
			case "three stale then exact", "four stale", "failed push gate", "cancel stale wait":
				r.PushGate = &WorktreeMergePushGateReceipt{Status: "passed", LocalSHA: head, PreviousRemoteSHA: base}
				t.Setenv("WB_TEST_S_STALE_READS", "4")
				want = "does not match preserved candidate"
				switch name {
				case "three stale then exact":
					t.Setenv("WB_TEST_S_STALE_READS", "3")
					want, reads = "", 4
				case "four stale":
					reads = 4
				case "failed push gate":
					r.PushGate.Status = "failed"
				default:
					want = "wait for published pull-request head"
					opts.Progress = func(e progress.Event) {
						if e.Phase == "verify_pull_request_head" && e.State == progress.Waiting {
							cancel()
						}
					}
				}
			}
			before := r
			if r.PushGate != nil {
				g := *r.PushGate
				before.PushGate = &g
			}
			err := verifyPublishedWorktreeMergePullRequest(ctx, r, opts)
			if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
				t.Fatalf("handoff = %v, want %q", err, want)
			}
			if chosen != nil && (!chosen.refused || chosen.later != 0 || !errors.Is(err, sentinel)) {
				t.Fatalf("exact native negative = %+v, %v", chosen, err)
			}
			if name == "cancel stale wait" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation identity lost: %v", err)
			}
			if got, parseErr := strconv.Atoi(mustReadEngineFile(t, counter)); parseErr != nil || got != reads {
				t.Fatalf("hosted observations = %d, %v; want %d", got, parseErr, reads)
			}
			if !reflect.DeepEqual(r, before) {
				t.Fatal("read-only handoff changed receipt")
			}
			if remote := strings.Fields(runEngineGit(t, source.WorktreeDir, "ls-remote", "--heads", "origin", "refs/heads/"+guard.Branch)); len(remote) != 2 || remote[0] != head {
				t.Fatalf("native published ref changed: %v", remote)
			}
		})
	}
}

//nolint:paralleltest // PATH/XDG/provider-record environment is process-wide; all advertised commits are derived from one private native origin.
func TestCheckObservationOwnerWaitKeepsNativePolicyAndDirectReread(t *testing.T) {
	fixture := newExplicitRootEngineFixture(t)
	head := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.repository.CloneURL, "update-ref", "refs/heads/release", head)
	bin := t.TempDir()
	body := `#!/bin/sh
set -eu
case "$2" in
  repos/acme/app/branches/main) echo '{"protected":false,"protection":{}}' ;;
  'repos/acme/app/rules/branches/main?per_page=100') echo '[]' ;;
  repos/acme/app/pulls/17) printf '%s\n' "$WB_TEST_S_DIRECT_PR" ;;
  repos/acme/app/git/ref/heads/main) printf '{"object":{"sha":"%s"}}\n' "$WB_TEST_S_HEAD" ;;
  "repos/acme/app/actions/runs?head_sha=$WB_TEST_S_HEAD&per_page=100") printf '%s\n' "$WB_TEST_S_RUNS" ;;
  "repos/acme/app/commits/$WB_TEST_S_HEAD/check-runs?per_page=100") printf '%s\n' "$WB_TEST_S_CHECKS" ;;
  "repos/acme/app/commits/$WB_TEST_S_HEAD/status?per_page=100") echo '{"total_count":0,"statuses":[]}' ;;
  *) echo "unexpected S wait request: $*" >&2; exit 2 ;;
esac
`
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("WB_TEST_S_HEAD", head)
	pr := `{"number":17,"state":"open","head":{"ref":"main","sha":"` + head + `","repo":{"full_name":"acme/app"}},"base":{"ref":"release","repo":{"full_name":"acme/app"}}}`
	runs := `{"total_count":1,"workflow_runs":[{"id":15,"workflow_id":300,"head_sha":"` + head + `","head_branch":"main","event":"pull_request","status":"completed","conclusion":"success","created_at":"2026-09-27T09:00:00Z","check_suite_id":305,"pull_requests":[{"number":17,"base":{"ref":"release"}}]}]}`
	checks := `{"total_count":2,"check_runs":[{"id":1,"name":"Required checks passed","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":305}},{"id":2,"name":"Tests and coverage (8 shards)","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":305}}]}`
	receipt := WorktreeMergeReceipt{Repository: fixture.repository.Slug, Target: "main", ReceiptPath: filepath.Join(t.TempDir(), "receipt.json"), ValidationDeferral: &WorktreeMergeValidationDeferral{
		Route: WorktreeMergeRouteDirect, CandidateSHA: head, DirectCIPullRequest: "17", DirectCIPullRequestNumber: 17, DirectCIBase: "release", DirectCIWorkflowID: 300}}
	for _, name := range []string{"invalid interval", "malformed deferral", "invalid identity", "ordinary passed", "direct passed", "direct final PR refusal", "direct skipped job"} {
		//nolint:paralleltest // Each row changes the real child gh provider records in the process environment.
		t.Run(name, func(t *testing.T) {
			t.Setenv("WB_TEST_S_DIRECT_PR", pr)
			t.Setenv("WB_TEST_S_RUNS", runs)
			t.Setenv("WB_TEST_S_CHECKS", checks)
			r := receipt
			deferral := *receipt.ValidationDeferral
			r.ValidationDeferral = &deferral
			opts := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, WaitSlice: 10 * time.Second, CheckPollInterval: 100 * time.Millisecond}
			want, status := "", githubchecks.PullRequestWaitPassed
			switch name {
			case "invalid interval":
				opts.CheckPollInterval = opts.WaitSlice
				want, status = "must be shorter than wait slice", ""
			case "malformed deferral":
				r.ValidationDeferral.CandidateSHA = "wrong"
				want, status = "direct CI deferral is not pinned", githubchecks.PullRequestWaitFailed
			case "invalid identity":
				r.Repository = ""
				want, status = "repository, target, and exact head are required", ""
			case "ordinary passed":
				r.ValidationDeferral = nil
			case "direct final PR refusal":
				t.Setenv("WB_TEST_S_DIRECT_PR", strings.Replace(pr, `"state":"open"`, `"state":"closed"`, 1))
				want, status = "direct CI pull request identity changed", githubchecks.PullRequestWaitFailed
			case "direct skipped job":
				t.Setenv("WB_TEST_S_CHECKS", strings.Replace(checks, `"conclusion":"success"`, `"conclusion":"skipped"`, 1))
				want, status = "did not execute successfully", githubchecks.PullRequestWaitFailed
			}
			var events []progress.Event
			opts.Progress = func(e progress.Event) { events = append(events, e) }
			result, err := waitForWorktreeMergeChecks(t.Context(), r, opts, "", head, true)
			if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) || result.Status != status {
				t.Fatalf("wait=%+v, %v; want status=%s error=%q", result, err, status, want)
			}
			if err == nil && (result.Head != head || result.StableObservations < 2 || len(result.Checks) != 2) {
				t.Fatalf("native observation stability or exact checks absent: %+v", result)
			}
			if name == "direct final PR refusal" && result.Reason != err.Error() {
				t.Fatal("direct reread diagnostic differs from retained reason")
			}
			if want == "" && len(events) == 0 {
				t.Fatal("native waiter did not report an observation")
			}
			if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != head {
				t.Fatalf("read-only wait moved native target to %s", got)
			}
		})
	}
}
