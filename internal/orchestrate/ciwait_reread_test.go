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
	var waits []time.Duration
	result, err := WaitForCommitChecks(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", Target: "main", Head: rereadTestHead,
		Slice: 30 * time.Second, CheckPollInterval: 8 * time.Second,
		StableRereadDelay: 100 * time.Millisecond,
		Progress: func(progress PullRequestWaitProgress) {
			if progress.NextPoll > 0 {
				waits = append(waits, progress.NextPoll)
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
	// Assert the actual timer duration, not total runtime: fake gh subprocesses
	// can be slow under coverage or a busy machine without changing the cadence.
	if !slices.Equal(waits, []time.Duration{100 * time.Millisecond}) {
		t.Fatalf("confirming reread waits = %v, want only the shortened delay", waits)
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
	var waits []time.Duration
	result, err := WaitForCommitChecks(context.Background(), PullRequestWaitOptions{
		Repository: "acme/docs", Target: "main", Head: rereadTestHead,
		Slice: 30 * time.Second, CheckPollInterval: interval,
		// A misapplied shortened delay would confirm in single-digit
		// milliseconds; the empty no-applicable-checks receipt must instead
		// wait the full poll interval, because that gap is its only
		// time-based guard against CI that simply has not registered yet.
		StableRereadDelay: 5 * time.Millisecond,
		Progress: func(progress PullRequestWaitProgress) {
			if progress.NextPoll > 0 {
				waits = append(waits, progress.NextPoll)
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
	if !slices.Equal(waits, []time.Duration{interval}) {
		t.Fatalf("no-applicable-checks reread waits = %v, want only the full poll interval %s", waits, interval)
	}
}

func TestWaitForCommitChecksFallsBackToPollCadenceOnFingerprintChurn(t *testing.T) {
	state := installRereadTestGH(t, checksBearingRereadScript(`"https://ci.example/run/$count"`))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interval := 200 * time.Millisecond
	shortReread := 5 * time.Millisecond
	var waits []time.Duration
	result, err := WaitForCommitChecks(ctx, PullRequestWaitOptions{
		Repository: "acme/app", Target: "main", Head: rereadTestHead,
		// The slice is a watchdog only. The callback owns completion after it
		// observes the cadence this regression is about.
		Slice: 30 * time.Second, CheckPollInterval: interval,
		StableRereadDelay: shortReread,
		Progress: func(progress PullRequestWaitProgress) {
			if progress.NextPoll == 0 {
				return
			}
			waits = append(waits, progress.NextPoll)
			if len(waits) == 3 {
				cancel()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PullRequestWaitFailed || !strings.Contains(result.Reason, context.Canceled.Error()) {
		t.Fatalf("callback-cancelled churn wait = %+v", result)
	}
	// One shortened reread is allowed after the first terminal observation.
	// Once its fingerprint churns, every later wait must be the full cadence.
	// This observes the scheduler's actual requested waits rather than fitting
	// subprocess observations into a wall-clock slice under machine load.
	if !slices.Equal(waits, []time.Duration{shortReread, interval, interval}) {
		t.Fatalf("churn reread waits = %v, want short then full cadence", waits)
	}
	if observations := rereadTestObservations(t, state); observations != 3 {
		t.Fatalf("observed the exact head %d times, want the three scheduled observations", observations)
	}
}
