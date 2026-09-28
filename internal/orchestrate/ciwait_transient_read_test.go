package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

// installTransientReadTestGH puts a fake gh on PATH and pins the observer
// state dir so retries stay hermetic per test, without the reread-specific
// observation counter installRereadTestGH also wires up.
func installTransientReadTestGH(t *testing.T, script string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(withEmptyActionsRuns(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A direct-target CI observation whose target-head read is killed by signal
// exactly once (a saturated host, exactly the incident this AC exists to
// close) must still pass: the in-process retry recovers before the caller
// ever sees a failure, and no second `wb ci wait` invocation is required.
func TestWaitForCommitChecksRecoversFromASignalKilledTargetHeadReadThenSucceeds(t *testing.T) {
	dir := t.TempDir()
	failedOnce := filepath.Join(dir, "failed-once")
	installTransientReadTestGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  if [ ! -f "`+failedOnce+`" ]; then
    touch "`+failedOnce+`"
    kill -9 $$
  fi
  echo '{"object":{"sha":"`+rereadTestHead+`"}}'; exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'; exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then
  echo '[]'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	result, err := WaitForCommitChecks(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", Target: "main", Head: rereadTestHead,
		Slice: 30 * time.Second, CheckPollInterval: 8 * time.Second,
		StableRereadDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PullRequestWaitPassed {
		t.Fatalf("result = %+v, want the signal-killed read to recover in-process and pass", result)
	}
	if _, statErr := os.Stat(failedOnce); statErr != nil {
		t.Fatal("the fake gh never saw the killed-then-retried attempt")
	}
	if result.Evidence["github_read_retries"] == "" {
		t.Fatalf("evidence = %#v, want the recovered retry recorded on the wait result, matching what `wb pr land` already records", result.Evidence)
	}
}

// A target-head read that stays transiently broken for the whole in-process
// retry budget (a persistently saturated host) must leave the wait pending —
// resumable in another foreground slice — never a hard failure. Only an
// authoritative GitHub answer (404, 401, ordinary 403, drift) is terminal.
func TestWaitForCommitChecksTreatsExhaustedTransientReadAsPendingNotFailed(t *testing.T) {
	installTransientReadTestGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  kill -9 $$
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	result, err := WaitForCommitChecks(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", Target: "main", Head: rereadTestHead,
		Slice: 30 * time.Second, CheckPollInterval: 8 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PullRequestWaitPending {
		t.Fatalf("result = %+v, want pending after exhausting in-process retries on a persistent transient failure", result)
	}
	if !strings.Contains(result.Reason, "resume the same exact target identity") {
		t.Fatalf("reason = %q, want resumable guidance", result.Reason)
	}
}

// TestCIWaitTransientReadRecordsRetryTelemetry closes the asymmetry the
// reviewer flagged: `wb pr land` (via LandPullRequest) has always recorded
// github_read_retries evidence for an in-process transient GitHub read
// recovery, but WaitForCommitChecks — the engine underneath `wb ci wait` —
// never wired up githubobserver.WithRetryTelemetry, so the identical recovery
// went unrecorded there. Both entry points must now report the same evidence
// key for the same kind of recovery.
func TestCIWaitTransientReadRecordsRetryTelemetry(t *testing.T) {
	dir := t.TempDir()
	failedOnce := filepath.Join(dir, "failed-once")
	installTransientReadTestGH(t, `#!/bin/sh
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  if [ ! -f "`+failedOnce+`" ]; then
    touch "`+failedOnce+`"
    kill -9 $$
  fi
  echo '{"protected":false,"protection":{}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"`+rereadTestHead+`"}}'; exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then
  echo '[]'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	result, err := WaitForCommitChecks(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", Target: "main", Head: rereadTestHead,
		Slice: 30 * time.Second, CheckPollInterval: 8 * time.Second,
		StableRereadDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PullRequestWaitPassed {
		t.Fatalf("result = %+v, want the signal-killed branch-policy read to recover in-process and pass", result)
	}
	if _, statErr := os.Stat(failedOnce); statErr != nil {
		t.Fatal("the fake gh never saw the killed-then-retried attempt")
	}
	if result.Evidence["github_read_retries"] == "" {
		t.Fatalf("evidence = %#v, want github_read_retries recorded on the ci-wait result the same way LandPullRequest already records it", result.Evidence)
	}
}

// TestWaitForCommitChecksTreatsExhaustedTransientPullRequestIdentityReadAsPendingNotFailed
// forces the candidate (pull-request) branch's own read of the pull request's
// exact head and target to exhaust its in-process transient-retry budget
// every single time, deterministically driving the `if reason != ""` pending
// branch right after pullRequestIdentity inside WaitForCommitChecks — a
// statement CI's own flaky-coverage sweep found covered on only 1 of 8 runs
// because the only prior coverage of it came from an accidental real
// transient failure racing the slice deadline, not from a test built to hit
// it. This test hits it on every run instead of relying on that race.
func TestWaitForCommitChecksTreatsExhaustedTransientPullRequestIdentityReadAsPendingNotFailed(t *testing.T) {
	installTransientReadTestGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then
  kill -9 $$
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	result, err := WaitForCommitChecks(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", PullRequest: "17", Target: "main", Head: rereadTestHead,
		Slice: 30 * time.Second, CheckPollInterval: 8 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PullRequestWaitPending {
		t.Fatalf("result = %+v, want pending after exhausting in-process retries reading the pull request's exact identity", result)
	}
	if !strings.Contains(result.Reason, "resume the same exact target identity") {
		t.Fatalf("reason = %q, want resumable guidance", result.Reason)
	}
}

// TestWaitForCommitChecksTreatsExhaustedTransientCandidateAncestryReadAsPendingNotFailed
// lets the pull-request identity read and the exact target-head read both
// succeed, then forces the candidate branch's read proving the candidate
// contains the current target (the `/compare/` ancestry check) to exhaust
// its in-process transient-retry budget every single time. This
// deterministically drives the third `if reason != ""` pending branch inside
// WaitForCommitChecks's candidate path (right after candidateContainsTarget)
// — flaky-coverage sweep found it covered on only 6 of 8 runs for the same
// reason as the sibling test above.
func TestWaitForCommitChecksTreatsExhaustedTransientCandidateAncestryReadAsPendingNotFailed(t *testing.T) {
	installTransientReadTestGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then
  echo '{"number":17,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"`+rereadTestHead+`","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/'; then
  kill -9 $$
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	result, err := WaitForCommitChecks(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", PullRequest: "17", Target: "main", Head: rereadTestHead,
		Slice: 30 * time.Second, CheckPollInterval: 8 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PullRequestWaitPending {
		t.Fatalf("result = %+v, want pending after exhausting in-process retries proving candidate ancestry against the target", result)
	}
	if !strings.Contains(result.Reason, "resume the same exact target identity") {
		t.Fatalf("reason = %q, want resumable guidance", result.Reason)
	}
	if result.ObservedTargetHead != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("ObservedTargetHead = %q, want the matched exact target head to have already been recorded", result.ObservedTargetHead)
	}
}

// TestWaitForCommitChecksTreatsExhaustedTransientCommitChecksReadAsPendingNotFailed
// lets a direct-target wait's own target-head read succeed, then forces the
// commit's check-runs read inside commitChecks to exhaust its in-process
// transient-retry budget every single time. This deterministically drives
// the fourth `if reason != ""` pending branch inside WaitForCommitChecks,
// right after the direct-target/candidate branches converge and call
// commitChecks — sibling to the three transient-read branches above, and
// like them previously covered only by an accidental real transient failure
// racing the slice deadline (sneat-dev/wb#646 task-4 resume, CI run
// 36001509348's coverage ratchet: internal/orchestrate/ciwait.go:193-194).
//
//nolint:paralleltest // calls t.Setenv via installTransientReadTestGH, which Go's testing package forbids combined with t.Parallel
func TestWaitForCommitChecksTreatsExhaustedTransientCommitChecksReadAsPendingNotFailed(t *testing.T) {
	installTransientReadTestGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"`+rereadTestHead+`"}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  kill -9 $$
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	result, err := WaitForCommitChecks(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", Target: "main", Head: rereadTestHead,
		Slice: 30 * time.Second, CheckPollInterval: 8 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PullRequestWaitPending {
		t.Fatalf("result = %+v, want pending after exhausting in-process retries reading the commit's check runs", result)
	}
	if !strings.Contains(result.Reason, "resume the same exact target identity") {
		t.Fatalf("reason = %q, want resumable guidance", result.Reason)
	}
	if result.ObservedTargetHead != rereadTestHead {
		t.Fatalf("ObservedTargetHead = %q, want the matched exact target head to have already been recorded", result.ObservedTargetHead)
	}
}

// TestPullRequestCommitParentsDecodesTheParentSHAsFromGitHub covers
// worktree_merge_pr_land.go's pullRequestCommitParents: its one GitHub read
// (a plain `gh api repos/.../commits/<sha>`) and the parents it decodes from
// the response had no unit-tier test reaching them at all -- every existing
// caller-level test for the update-branch-proof path scripts the fake gh
// only up to the local git/merge-tree steps that resolve the ordinary case
// without ever needing a live commit-parents read. installTransientReadTestGH
// (this file) already puts a fake gh on PATH without needing
// runnertest.AllowRealProcess, since it goes through a direct exec.Command
// in internal/githubobserver rather than through task-24's guarded runner.
//
//nolint:paralleltest // calls t.Setenv via installTransientReadTestGH, which Go's testing package forbids combined with t.Parallel
func TestPullRequestCommitParentsDecodesTheParentSHAsFromGitHub(t *testing.T) {
	installTransientReadTestGH(t, `#!/bin/sh
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/commits/deadbeefcafe' ]; then
  echo '{"sha":"deadbeefcafe","parents":[{"sha":"parent1sha"},{"sha":"parent2sha"}]}'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	parents, err := pullRequestCommitParents(context.Background(), "acme/app", "deadbeefcafe")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"parent1sha", "parent2sha"}; !slices.Equal(parents, want) {
		t.Fatalf("parents = %v, want %v", parents, want)
	}
}

// TestCommitTreeSHAReadsTheTreeSHAFromGitHub covers
// worktree_merge_stranded.go's commitTreeSHA: the same kind of gap as
// pullRequestCommitParents above, a plain `gh api repos/.../git/commits/<sha>`
// read whose decoded tree SHA no unit-tier test exercised.
//
//nolint:paralleltest // calls t.Setenv via installTransientReadTestGH, which Go's testing package forbids combined with t.Parallel
func TestCommitTreeSHAReadsTheTreeSHAFromGitHub(t *testing.T) {
	installTransientReadTestGH(t, `#!/bin/sh
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/git/commits/deadbeefcafe' ]; then
  echo '{"sha":"deadbeefcafe","tree":{"sha":"treeshavalue"}}'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	tree, err := commitTreeSHA(context.Background(), "acme/app", "deadbeefcafe")
	if err != nil {
		t.Fatal(err)
	}
	if tree != "treeshavalue" {
		t.Fatalf("tree = %q, want %q", tree, "treeshavalue")
	}
}

// TestPullRequestCommitParentsSurfacesADecodeError covers
// pullRequestCommitParents' own json.Unmarshal error return: a `gh api`
// call that succeeds (exit 0) but returns a body GitHub's own commit schema
// never produces is a decode failure, not an absent commit, so it must come
// back as an error rather than an empty parent list.
//
//nolint:paralleltest // calls t.Setenv via installTransientReadTestGH, which Go's testing package forbids combined with t.Parallel
func TestPullRequestCommitParentsSurfacesADecodeError(t *testing.T) {
	installTransientReadTestGH(t, `#!/bin/sh
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/commits/deadbeefcafe' ]; then
  echo 'not valid json'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	_, err := pullRequestCommitParents(context.Background(), "acme/app", "deadbeefcafe")
	if err == nil {
		t.Fatal("err = nil, want a decode error for a body that is not valid JSON")
	}
	if !strings.Contains(err.Error(), "decode commit parents") {
		t.Fatalf("err = %v, want it to name the decode failure", err)
	}
}

// TestCommitTreeSHASurfacesADecodeError covers commitTreeSHA's own
// json.Unmarshal error return, the same shape as the test above.
//
//nolint:paralleltest // calls t.Setenv via installTransientReadTestGH, which Go's testing package forbids combined with t.Parallel
func TestCommitTreeSHASurfacesADecodeError(t *testing.T) {
	installTransientReadTestGH(t, `#!/bin/sh
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/git/commits/deadbeefcafe' ]; then
  echo 'not valid json'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	_, err := commitTreeSHA(context.Background(), "acme/app", "deadbeefcafe")
	if err == nil {
		t.Fatal("err = nil, want a decode error for a body that is not valid JSON")
	}
	if !strings.Contains(err.Error(), "decode commit") {
		t.Fatalf("err = %v, want it to name the decode failure", err)
	}
}
