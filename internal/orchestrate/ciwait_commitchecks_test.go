package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// installCommitChecksTestGH puts a fake gh on PATH for the commitChecks and
// commitCheckRuns propagation tests below. It deliberately does not touch
// XDG_STATE_HOME retry bookkeeping the transient-read tests need, because
// every scenario here is an immediate, non-retryable GitHub answer (a
// malformed body or an authoritative HTTP error), never a signal-killed
// attempt.
func installCommitChecksTestGH(t *testing.T, script string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestCommitChecksReturnsHardFailureReadingPullRequestIdentity drives
// commitChecks's own "the pull request must still prove it points at the
// head being waited on" read into a hard, non-retryable failure (a malformed
// body, never a signal-killed attempt), deterministically hitting the
// `return nil, false, err.Error()` right after ReadPullRequest. CI's own
// flaky-coverage sweep found that statement covered on only 3 of 8 runs
// because the only previous coverage of it came from an accidental real
// transient failure, not a test built to hit it.
func TestCommitChecksReturnsHardFailureReadingPullRequestIdentity(t *testing.T) {
	installCommitChecksTestGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then
  echo 'not json'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	checks, pending, reason := commitChecks(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", PullRequest: "17", Head: strings.Repeat("a", 40),
	})
	if checks != nil || pending {
		t.Fatalf("commitChecks = checks=%#v pending=%t, want a hard failure with no checks", checks, pending)
	}
	if !strings.Contains(reason, "decode pull request") {
		t.Fatalf("reason = %q, want the wrapped ReadPullRequest decode failure", reason)
	}
}

// TestCommitChecksReturnsHardFailureFromCommitCheckRuns drives commitChecks's
// `if reason != ""` right after commitCheckRuns into its true branch every
// run, via an authoritative (non-retried) HTTP error on the check-runs
// endpoint. Found flaky at 3 of 8 runs for the same reason as the sibling
// tests in this file.
func TestCommitChecksReturnsHardFailureFromCommitCheckRuns(t *testing.T) {
	installCommitChecksTestGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo 'gh: Upgrade to access this repository (HTTP 403)' >&2; exit 1
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	checks, pending, reason := commitChecks(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", Head: strings.Repeat("a", 40),
	})
	if checks != nil || pending {
		t.Fatalf("commitChecks = checks=%#v pending=%t, want a hard failure with no checks", checks, pending)
	}
	if !strings.Contains(reason, "HTTP 403") {
		t.Fatalf("reason = %q, want the check-runs read failure propagated", reason)
	}
}

// TestCommitChecksReturnsHardFailureFromCommitStatuses drives commitChecks's
// `if reason != ""` right after commitStatuses into its true branch every
// run, via an authoritative (non-retried) HTTP error on the statuses
// endpoint, with the check-runs read itself succeeding empty. Found flaky at
// 3 of 8 runs for the same reason as the sibling tests in this file.
func TestCommitChecksReturnsHardFailureFromCommitStatuses(t *testing.T) {
	installCommitChecksTestGH(t, withEmptyActionsRuns(`#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":0,"check_runs":[]}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo 'gh: Upgrade to access this repository (HTTP 403)' >&2; exit 1
fi
echo "unexpected gh args: $*" >&2; exit 30
`))
	checks, pending, reason := commitChecks(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", Head: strings.Repeat("a", 40),
	})
	if checks != nil || pending {
		t.Fatalf("commitChecks = checks=%#v pending=%t, want a hard failure with no checks", checks, pending)
	}
	if !strings.Contains(reason, "HTTP 403") {
		t.Fatalf("reason = %q, want the statuses read failure propagated", reason)
	}
}

// TestCommitCheckRunsReturnsHardFailureReadingCheckRuns drives
// commitCheckRuns's own `if err != nil { return nil, false, err.Error() }`
// right after its githubGet into its true branch every run, via an
// authoritative (non-retried) HTTP error. Found flaky at 2 of 8 runs for the
// same reason as the sibling tests in this file.
func TestCommitCheckRunsReturnsHardFailureReadingCheckRuns(t *testing.T) {
	installCommitChecksTestGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo 'gh: Upgrade to access this repository (HTTP 403)' >&2; exit 1
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	checks, pending, reason := commitCheckRuns(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", Target: "main", Head: strings.Repeat("a", 40),
	})
	if checks != nil || pending {
		t.Fatalf("commitCheckRuns = checks=%#v pending=%t, want a hard failure with no checks", checks, pending)
	}
	if !strings.Contains(reason, "HTTP 403") {
		t.Fatalf("reason = %q, want the check-runs read failure propagated", reason)
	}
}

// TestCommitCheckRunsReturnsHardFailureFromActionsRunsForHead lets the
// check-runs read succeed empty, then drives commitCheckRuns's
// `if reason != ""` right after githubActionsRunsForHead into its true
// branch every run, via an authoritative (non-retried) HTTP error on the
// Actions runs endpoint. Found flaky at 1 of 8 runs for the same reason as
// the sibling tests in this file.
func TestCommitCheckRunsReturnsHardFailureFromActionsRunsForHead(t *testing.T) {
	installCommitChecksTestGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":0,"check_runs":[]}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo 'gh: Upgrade to access this repository (HTTP 403)' >&2; exit 1
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	checks, pending, reason := commitCheckRuns(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", Target: "main", Head: strings.Repeat("a", 40),
	})
	if checks != nil || pending {
		t.Fatalf("commitCheckRuns = checks=%#v pending=%t, want a hard failure with no checks", checks, pending)
	}
	if !strings.Contains(reason, "HTTP 403") {
		t.Fatalf("reason = %q, want the Actions-runs read failure propagated", reason)
	}
}
