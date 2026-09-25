package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCloseSupersededWorktreeMergePullRequestExhaustsVerifyRetriesOnPersistentTransientRead
// proves closeSupersededWorktreeMergePullRequest's own verify-retry loop
// (worktree_merge_ack.go) does not retry forever: a post-close read that
// stays transiently broken for the whole verifyAttempts budget (a
// persistently saturated host, simulated by killing every `gh` invocation
// with SIGKILL, which is exactly what makes githubobserver report
// ErrTransientRetriesExhausted / IsTransientReadFailure) is refused, naming
// the pull request, after exactly verifyAttempts-1 sleeps of exactly
// verifyDelay each — sleep is the injected recorder, so none of those
// waits is real; only the unrelated, already-seamed githubobserver retry
// beneath each individual read costs real (bounded, small) wall time.
//
//nolint:paralleltest // calls t.Setenv via installTransientReadTestGH, which Go's testing package forbids combined with t.Parallel
func TestCloseSupersededWorktreeMergePullRequestExhaustsVerifyRetriesOnPersistentTransientRead(t *testing.T) {
	installTransientReadTestGH(t, `#!/bin/sh
if [ "$1" = api ] && [ "$2" = '--method' ] && [ "$3" = PATCH ]; then
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'repos/acme/app/pulls/41'; then
  kill -9 $$
fi
echo "unexpected gh args: $*" >&2; exit 30
`)

	var slept []time.Duration
	err := closeSupersededWorktreeMergePullRequest(context.Background(), "acme/app", "41", func(d time.Duration) { slept = append(slept, d) })
	if err == nil {
		t.Fatal("closeSupersededWorktreeMergePullRequest succeeded despite a persistently broken verify read")
	}
	if !strings.Contains(err.Error(), "verify superseded pull request 41 was closed") {
		t.Fatalf("error = %q, want it to name the verify-read failure", err.Error())
	}
	const verifyAttempts = 3
	if len(slept) != verifyAttempts-1 {
		t.Fatalf("closeSupersededWorktreeMergePullRequest slept %d times, want %d (once before each retry, never after the final attempt)", len(slept), verifyAttempts-1)
	}
	for _, d := range slept {
		if d != 500*time.Millisecond {
			t.Fatalf("closeSupersededWorktreeMergePullRequest slept %v, want every wait to be the 500ms verify delay", slept)
		}
	}
}

// TestPinPullRequestViewToHeadRetriesUntilHeadMatches proves the poll loop
// re-reads the pull request exactly as many times as it takes GitHub's
// read-after-write to catch up with pushedHead, sleeping pinPullRequestViewPollDelay
// (never a real wait — sleep is the injected recorder) before each re-read,
// and stopping the instant the reported head matches.
//
//nolint:paralleltest // calls t.Setenv via installTransientReadTestGH, which Go's testing package forbids combined with t.Parallel
func TestPinPullRequestViewToHeadRetriesUntilHeadMatches(t *testing.T) {
	state := filepath.Join(t.TempDir(), "reads")
	installTransientReadTestGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q 'repos/acme/app/pulls/41'; then
  n=0
  if [ -f "`+state+`" ]; then n=$(cat "`+state+`"); fi
  n=$((n + 1))
  printf '%s' "$n" > "`+state+`"
  if [ "$n" -lt 3 ]; then
    printf '{"number":41,"head":{"sha":"stale-sha"}}\n'
  else
    printf '{"number":41,"head":{"sha":"pushed-sha"}}\n'
  fi
  exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`)

	var slept []time.Duration
	view := PullRequestView{Number: 41}
	view.Head.SHA = "original-sha"

	got, err := pinPullRequestViewToHead(context.Background(), "acme/app", "41", "pushed-sha", view, func(d time.Duration) { slept = append(slept, d) })
	if err != nil {
		t.Fatalf("pinPullRequestViewToHead: %v", err)
	}
	if got.Head.SHA != "pushed-sha" {
		t.Fatalf("pinPullRequestViewToHead returned head %q, want pushed-sha", got.Head.SHA)
	}
	// A sleep precedes every re-read, including the first: two stale reads
	// ("stale-sha") then a third that reports pushed-sha, so exactly three.
	if len(slept) != 3 {
		t.Fatalf("pinPullRequestViewToHead slept %d times, want 3 (one before each of the three re-reads)", len(slept))
	}
	for _, d := range slept {
		if d != pinPullRequestViewPollDelay {
			t.Fatalf("pinPullRequestViewToHead slept %v, want every wait to be pinPullRequestViewPollDelay (%s)", slept, pinPullRequestViewPollDelay)
		}
	}
	raw, readErr := os.ReadFile(state)
	if readErr != nil || strings.TrimSpace(string(raw)) != "3" {
		t.Fatalf("gh was read %s times, want 3 (two stale reads then the matching one)", string(raw))
	}
}

// TestPinPullRequestViewToHeadReturnsErrorAfterExhaustingAttempts proves the
// retry-exhausted branch: a pull request that never reports pushedHead (a
// stuck or permanently stale read-after-write) is refused by name, after
// every attempt, not retried forever.
//
//nolint:paralleltest // calls t.Setenv via installTransientReadTestGH, which Go's testing package forbids combined with t.Parallel
func TestPinPullRequestViewToHeadReturnsErrorAfterExhaustingAttempts(t *testing.T) {
	installTransientReadTestGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q 'repos/acme/app/pulls/41'; then
  printf '{"number":41,"head":{"sha":"stale-sha"}}\n'
  exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`)

	var slept []time.Duration
	view := PullRequestView{Number: 41}
	view.Head.SHA = "original-sha"

	_, err := pinPullRequestViewToHead(context.Background(), "acme/app", "41", "pushed-sha", view, func(d time.Duration) { slept = append(slept, d) })
	if err == nil {
		t.Fatal("pinPullRequestViewToHead succeeded despite the head never matching pushedHead")
	}
	if !strings.Contains(err.Error(), "acme/app#41") || !strings.Contains(err.Error(), "stale-sha") || !strings.Contains(err.Error(), "pushed-sha") {
		t.Fatalf("error = %q, want it to name the pull request, the reported head, and the pushed head", err.Error())
	}
	// attempts is 5, and the loop runs while attempt < attempts starting at
	// 1: four iterations, each sleeping once before its re-read.
	if len(slept) != 4 {
		t.Fatalf("pinPullRequestViewToHead slept %d times, want 4", len(slept))
	}
	for _, d := range slept {
		if d != pinPullRequestViewPollDelay {
			t.Fatalf("pinPullRequestViewToHead slept %v, want every wait to be pinPullRequestViewPollDelay (%s)", slept, pinPullRequestViewPollDelay)
		}
	}
}
