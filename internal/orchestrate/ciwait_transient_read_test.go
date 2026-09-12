package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(withEmptyActionsRuns(script)), 0o755); err != nil {
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
