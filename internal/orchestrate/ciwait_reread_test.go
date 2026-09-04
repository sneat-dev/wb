package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const rereadTestHead = "0123456789012345678901234567890123456789"

// installRereadTestGH puts a fake gh on PATH and pins the observer state dir
// so conditional-request caching stays hermetic per test.
func installRereadTestGH(t *testing.T, script string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	state := filepath.Join(t.TempDir(), "observations")
	t.Setenv("WB_CI_REREAD_STATE", state)
	return state
}

func rereadTestObservations(t *testing.T, state string) int {
	t.Helper()
	contents, err := os.ReadFile(state)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(contents)))
	if err != nil {
		t.Fatalf("parse observation counter %q: %v", contents, err)
	}
	return count
}

// checksBearingRereadScript serves a direct target with one terminal check
// run and an enumerated empty required policy. linkExpression lets the churn
// test vary the check-run link (and therefore the terminal fingerprint) per
// observation.
func checksBearingRereadScript(linkExpression string) string {
	return `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"` + rereadTestHead + `"}}'; exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'; exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then
  echo '[]'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  count=0; if [ -f "$WB_CI_REREAD_STATE" ]; then count=$(cat "$WB_CI_REREAD_STATE"); fi
  count=$((count + 1)); printf '%s' "$count" > "$WB_CI_REREAD_STATE"
  link=` + linkExpression + `
  echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","html_url":"'"$link"'","app":{"id":42}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`
}

func TestWaitForCommitChecksShortensOnlyTheChecksBearingConfirmingReread(t *testing.T) {
	state := installRereadTestGH(t, checksBearingRereadScript(`https://ci.example/run/1`))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var nextPolls []time.Duration
	result, err := WaitForCommitChecks(ctx, PullRequestWaitOptions{
		Repository: "acme/app", Target: "main", Head: rereadTestHead,
		Slice: 30 * time.Second, CheckPollInterval: 8 * time.Second,
		StableRereadDelay: 100 * time.Millisecond,
		Progress: func(progress PullRequestWaitProgress) {
			if progress.NextPoll > 0 {
				nextPolls = append(nextPolls, progress.NextPoll)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PullRequestWaitPassed || result.StableObservations != 2 {
		t.Fatalf("result = %+v", result)
	}
	if observations := rereadTestObservations(t, state); observations != 2 {
		t.Fatalf("observed the exact head %d times, want one terminal observation plus one confirming reread", observations)
	}
	if got, want := nextPolls, []time.Duration{100 * time.Millisecond}; !slices.Equal(got, want) {
		t.Fatalf("requested waits = %v, want %v", got, want)
	}
}

func TestWaitForCommitChecksKeepsFullCadenceBeforeNoApplicableChecksReread(t *testing.T) {
	state := installRereadTestGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"`+rereadTestHead+`"}}'; exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/docs/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'; exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/docs/rules/branches/main?per_page=100'; then
  echo '[]'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  count=0; if [ -f "$WB_CI_REREAD_STATE" ]; then count=$(cat "$WB_CI_REREAD_STATE"); fi
  count=$((count + 1)); printf '%s' "$count" > "$WB_CI_REREAD_STATE"
  echo '{"total_count":0,"check_runs":[]}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	interval := 700 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var nextPolls []time.Duration
	result, err := WaitForCommitChecks(ctx, PullRequestWaitOptions{
		Repository: "acme/docs", Target: "main", Head: rereadTestHead,
		Slice: 30 * time.Second, CheckPollInterval: interval,
		// A misapplied shortened delay would confirm in single-digit
		// milliseconds; the empty no-applicable-checks receipt must instead
		// wait the full poll interval, because that gap is its only
		// time-based guard against CI that simply has not registered yet.
		StableRereadDelay: 5 * time.Millisecond,
		Progress: func(progress PullRequestWaitProgress) {
			if progress.NextPoll > 0 {
				nextPolls = append(nextPolls, progress.NextPoll)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PullRequestWaitPassed || result.StableObservations != 2 || len(result.Checks) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if observations := rereadTestObservations(t, state); observations != 2 {
		t.Fatalf("observed the exact head %d times, want exactly two full-cadence observations", observations)
	}
	if got, want := nextPolls, []time.Duration{interval}; !slices.Equal(got, want) {
		t.Fatalf("requested waits = %v, want %v", got, want)
	}
}

func TestWaitForCommitChecksFallsBackToPollCadenceOnFingerprintChurn(t *testing.T) {
	state := installRereadTestGH(t, checksBearingRereadScript(`"https://ci.example/run/$count"`))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var nextPolls []time.Duration
	result, err := WaitForCommitChecks(ctx, PullRequestWaitOptions{
		Repository: "acme/app", Target: "main", Head: rereadTestHead,
		Slice: 30 * time.Second, CheckPollInterval: 3 * time.Second,
		StableRereadDelay: 50 * time.Millisecond,
		Progress: func(progress PullRequestWaitProgress) {
			if progress.NextPoll > 0 {
				nextPolls = append(nextPolls, progress.NextPoll)
				if len(nextPolls) == 2 {
					// The live waiter reports NextPoll immediately before its
					// production timer. Cancel at that point: the requested second
					// full-cadence wait is the behavior under test, not its duration.
					cancel()
				}
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PullRequestWaitFailed || !strings.Contains(result.Reason, context.Canceled.Error()) {
		t.Fatalf("churning waiter must stop only because the test canceled its second full-cadence wait, got %+v", result)
	}
	// One shortened reread is allowed after the first terminal observation;
	// once the fingerprint churns, every later wait must return to the full
	// poll cadence. Cancellation immediately after the second reported wait
	// makes the exact production sequence deterministic under subprocess load.
	if observations := rereadTestObservations(t, state); observations != 2 {
		t.Fatalf("observed the exact head %d times, want two observations before the full-cadence wait", observations)
	}
	if got, want := nextPolls, []time.Duration{50 * time.Millisecond, 3 * time.Second}; !slices.Equal(got, want) {
		t.Fatalf("requested waits = %v, want %v", got, want)
	}
}
