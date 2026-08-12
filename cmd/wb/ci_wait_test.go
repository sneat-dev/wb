package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

const (
	ciWaitHead       = "0123456789012345678901234567890123456789"
	ciWaitTargetHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// ciWaitSliceBudget outlasts every observation these tests expect by orders
	// of magnitude. A slice deadline cancels the in-flight gh command, so a
	// budget a loaded runner can reach silently drops observations the receipts
	// below count.
	ciWaitSliceBudget = 5 * time.Minute
	// ciWaitSingleObservationInterval leaves WB no room for a second observation
	// within the budget: the poll-budget guard refuses to start one whose
	// interval would overrun the slice, so the slice ends on the observation
	// contract rather than on the clock and observes exactly once on any runner.
	ciWaitSingleObservationInterval = ciWaitSliceBudget - time.Millisecond
	// ciWaitRereadInterval keeps a terminal observation and its stable reread
	// back to back inside one slice.
	ciWaitRereadInterval = 100 * time.Millisecond
)

// ciWaitSliceArguments pins how many GitHub observations a foreground slice
// makes instead of leaving it to however many polls fit in a wall-clock budget.
// The first slice observes exactly once and resumes; later slices poll tightly
// so the terminal observation and its stable reread land in the same slice.
func ciWaitSliceArguments(identity []string, invocation int) []string {
	interval := ciWaitRereadInterval
	if invocation == 1 {
		interval = ciWaitSingleObservationInterval
	}
	return append(append([]string(nil), identity...), "--slice", ciWaitSliceBudget.String(), "--interval", interval.String())
}

// ciWaitObservations reads the fake gh receipt counter, which records one entry
// per observation of the exact head's checks.
func ciWaitObservations(t *testing.T, state string) int {
	t.Helper()
	contents, err := os.ReadFile(state)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read observation counter %s: %v", state, err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(contents)))
	if err != nil {
		t.Fatalf("parse observation counter %q: %v", contents, err)
	}
	return count
}

func TestCIWaitResumesForegroundSlicesUntilExactHeadPasses(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "checks")
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then
  echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"feature/integration"}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/feature/integration'; then
  echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then
  echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0
fi
if [ "$1" = pr ] && [ "$2" = checks ]; then
	case " $* " in
	  *" --required "*) echo '[{"name":"CI","bucket":"pending"}]'; exit 8;;
	esac
  count=0
  if [ -f "$WB_CI_WAIT_STATE" ]; then count=$(cat "$WB_CI_WAIT_STATE"); fi
  count=$((count + 1))
  printf '%s' "$count" > "$WB_CI_WAIT_STATE"
	if [ "${WB_CI_WAIT_INVOCATION:-0}" -lt 2 ]; then
    echo '[{"name":"CI","bucket":"pending"}]'
	# gh pr checks exits 8 while a JSON receipt contains pending checks.
	# WB must parse this receipt and return a resumable pending result.
    exit 8
  else
    echo '[{"name":"CI","bucket":"pass"}]'
  fi
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  if [ "${WB_CI_WAIT_INVOCATION:-0}" -lt 2 ]; then
    echo '{"total_count":1,"check_runs":[{"name":"CI","status":"in_progress","app":{"id":42}}]}'
  else
    echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'
  fi
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '^repos/acme/app/branches/feature%2Fintegration$'; then
  echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["CI"]}}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '^repos/acme/app/branches/feature%2Fintegration/protection/required_status_checks$'; then
  echo '{"strict":true,"contexts":["CI"],"checks":[]}'; exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/feature%2Fintegration?per_page=100'; then
  echo '[[]]'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_CI_WAIT_STATE", state)
	identity := []string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "feature/integration", "--head", ciWaitHead, "--json"}
	passedAt := 0
	for invocation := 1; invocation <= 3; invocation++ {
		t.Setenv("WB_CI_WAIT_INVOCATION", strconv.Itoa(invocation))
		before := ciWaitObservations(t, state)
		var stdout, stderr bytes.Buffer
		code := run(ciWaitSliceArguments(identity, invocation), &stdout, &stderr)
		var output ciWaitOutput
		if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
			t.Fatalf("slice %d output=%q: %v", invocation, stdout.String(), err)
		}
		observations := ciWaitObservations(t, state) - before
		if code == exitFindings && output.Status == "pending" {
			if len(output.ResumeArgs) == 0 {
				t.Fatalf("slice %d has no resume args: output=%+v stderr=%s", invocation, output, stderr.String())
			}
			joined := strings.Join(output.ResumeArgs, " ")
			for _, required := range []string{"--repo acme/app", "--pr 17", "--target feature/integration", "--head " + ciWaitHead, "--json"} {
				if !strings.Contains(joined, required) {
					t.Fatalf("slice %d resume=%q missing %q", invocation, joined, required)
				}
			}
			if observations != 1 {
				t.Fatalf("pending slice %d observed the exact head %d times, want exactly one bounded observation", invocation, observations)
			}
			continue
		}
		if code != exitOK || output.Status != "passed" || output.ObservedHead != ciWaitHead || output.ObservedTargetHead != ciWaitTargetHead || !output.CandidateContainsTarget || output.TargetFreshnessAuthority == "" || output.StableObservations != 2 {
			t.Fatalf("terminal slice = code %d output=%+v stderr=%s", code, output, stderr.String())
		}
		if observations != 2 {
			t.Fatalf("terminal slice %d observed the exact head %d times, want one terminal observation plus one stable reread", invocation, observations)
		}
		passedAt = invocation
		break
	}
	if passedAt < 2 {
		t.Fatalf("terminal receipt did not require multiple bounded foreground slices: passedAt=%d", passedAt)
	}
}

func TestCIWaitPullRequestJSONFailureOverridesGHExitCode(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then
  echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then
  echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0
fi
if [ "$1" = pr ] && [ "$2" = checks ]; then
  echo '[{"name":"CI","bucket":"fail"}]'
  exit 1
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":0,"check_runs":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", "20s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || len(output.Checks) != 1 || output.Checks[0].Bucket != "fail" || strings.Contains(output.Reason, "exit status 1") {
		t.Fatalf("JSON failure receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

func TestCIWaitResumesDirectTargetSlicesUntilExactHeadPasses(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "direct-checks")
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/feature/integration'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '^repos/acme/app/branches/feature%2Fintegration$'; then
  echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["lint","test"]}}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/feature%2Fintegration?per_page=100'; then
  echo '[[]]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  count=0
  if [ -f "$WB_CI_WAIT_STATE" ]; then count=$(cat "$WB_CI_WAIT_STATE"); fi
  count=$((count + 1))
  printf '%s' "$count" > "$WB_CI_WAIT_STATE"
  if [ "${WB_CI_WAIT_INVOCATION:-0}" -lt 2 ]; then
    echo '{"total_count":2,"check_runs":[{"name":"lint","status":"completed","conclusion":"success"},{"name":"test","status":"in_progress"}]}'
  else
    echo '{"total_count":2,"check_runs":[{"name":"test","status":"completed","conclusion":"success"},{"name":"lint","status":"completed","conclusion":"success"}]}'
  fi
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_CI_WAIT_STATE", state)
	identity := []string{"ci", "wait", "--repo", "acme/app", "--target", "feature/integration", "--head", ciWaitHead, "--json"}
	passedAt := 0
	for invocation := 1; invocation <= 3; invocation++ {
		t.Setenv("WB_CI_WAIT_INVOCATION", strconv.Itoa(invocation))
		before := ciWaitObservations(t, state)
		var stdout, stderr bytes.Buffer
		code := run(ciWaitSliceArguments(identity, invocation), &stdout, &stderr)
		var output ciWaitOutput
		if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
			t.Fatalf("slice %d output=%q: %v", invocation, stdout.String(), err)
		}
		observations := ciWaitObservations(t, state) - before
		if code == exitFindings && output.Status == "pending" {
			if len(output.ResumeArgs) == 0 {
				t.Fatalf("slice %d = code %d output=%+v stderr=%s", invocation, code, output, stderr.String())
			}
			if observations != 1 {
				t.Fatalf("pending direct slice %d observed the exact head %d times, want exactly one bounded observation", invocation, observations)
			}
			continue
		}
		if code != exitOK || output.Status != "passed" || output.ObservedHead != ciWaitHead || output.StableObservations != 2 {
			t.Fatalf("terminal direct slice = code %d output=%+v stderr=%s", code, output, stderr.String())
		}
		if observations != 2 {
			t.Fatalf("terminal direct slice %d observed the exact head %d times, want one terminal observation plus one stable reread", invocation, observations)
		}
		if len(output.Checks) != 2 || output.Checks[0].Name != "check-run:lint" || output.Checks[1].Name != "check-run:test" {
			t.Fatalf("direct checks were not deterministically sorted: %#v", output.Checks)
		}
		passedAt = invocation
		break
	}
	if passedAt < 2 {
		t.Fatalf("direct terminal receipt did not require multiple foreground slices: passedAt=%d", passedAt)
	}
}

func TestCIWaitDirectTargetStatusReceiptsBlockFailureAndAcceptStatusOnlyPass(t *testing.T) {
	for _, test := range []struct {
		name       string
		state      string
		wantStatus string
		wantCode   int
	}{
		{name: "status only pass", state: "success", wantStatus: "passed", wantCode: exitOK},
		{name: "status failure", state: "failure", wantStatus: "failed", wantCode: exitFindings},
	} {
		t.Run(test.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "bin")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["legacy"]}}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then
  echo '[[]]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":0,"check_runs":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":1,"statuses":[{"context":"legacy","state":"` + test.state + `"}]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
			writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			var stdout, stderr bytes.Buffer
			code := run([]string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", "10s", "--interval", "100ms", "--json"}, &stdout, &stderr)
			var output ciWaitOutput
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatal(err)
			}
			if code != test.wantCode || string(output.Status) != test.wantStatus || len(output.Checks) != 1 || output.Checks[0].Name != "status:legacy" {
				t.Fatalf("status receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
			}
			if test.wantStatus == "passed" && (output.StableObservations != 2 || len(output.RequiredChecks) != 1 || output.RequiredChecks[0].Name != "legacy") {
				t.Fatalf("status-only pass lacks authoritative stable receipt: %+v", output)
			}
		})
	}
}

func TestCIWaitWaitsForStableRereadAfterLateSuiteRegistration(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "late-suite")
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["CI"]}}}'; exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then
  echo '[[]]'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  count=0
  if [ -f "$WB_CI_WAIT_STATE" ]; then count=$(cat "$WB_CI_WAIT_STATE"); fi
  count=$((count + 1)); printf '%s' "$count" > "$WB_CI_WAIT_STATE"
  if [ "$count" -eq 1 ]; then
    echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success"}]}'
  elif [ "$count" -eq 2 ]; then
    echo '{"total_count":2,"check_runs":[{"name":"CI","status":"completed","conclusion":"success"},{"name":"Release","status":"queued"}]}'
  else
    echo '{"total_count":2,"check_runs":[{"name":"CI","status":"completed","conclusion":"success"},{"name":"Release","status":"completed","conclusion":"success"}]}'
  fi
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_CI_WAIT_STATE", state)
	var stdout, stderr bytes.Buffer
	// The fixture sequences its receipts by observation, not by elapsed time, so
	// the slice budget only has to outlast four observations on any runner.
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitRereadInterval.String(), "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitOK || output.Status != "passed" || output.StableObservations != 2 || len(output.Checks) != 2 {
		t.Fatalf("late-suite receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if observations := ciWaitObservations(t, state); observations != 4 {
		t.Fatalf("expected first green, late registration, then two stable terminal reads; observations=%d", observations)
	}
}

func TestCIWaitDirectTargetHonorsPinnedRequiredCheckIntegration(t *testing.T) {
	for _, test := range []struct {
		name       string
		appID      string
		wantStatus string
		wantCode   int
		slice      string
	}{
		{name: "matching app", appID: "42", wantStatus: "passed", wantCode: exitOK, slice: "15s"},
		{name: "same name wrong app", appID: "7", wantStatus: "pending", wantCode: exitFindings, slice: "5s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "bin")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  echo '{"protected":true,"protection":{}}'; exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then
  if [ "$2" != '--paginate' ] || [ "$3" != '--slurp' ]; then echo 'active rules must be fully paginated' >&2; exit 31; fi
  echo '[[],[{"type":"required_status_checks","ruleset_source_type":"Repository","ruleset_source":"acme/app","ruleset_id":7,"parameters":{"required_status_checks":[{"context":"CI","integration_id":42}]}}]]'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":` + test.appID + `}}]}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":1,"statuses":[{"context":"CI","state":"success"}]}'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`
			writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			var stdout, stderr bytes.Buffer
			code := run([]string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", test.slice, "--interval", "100ms", "--json"}, &stdout, &stderr)
			var output ciWaitOutput
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatal(err)
			}
			if code != test.wantCode || string(output.Status) != test.wantStatus || len(output.RequiredChecks) != 1 || output.RequiredChecks[0].IntegrationID != 42 {
				t.Fatalf("pinned integration receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
			}
			if test.wantStatus == "pending" && !strings.Contains(output.Reason, "GitHub App 42") {
				t.Fatalf("wrong producer was not explained: %+v", output)
			}
		})
	}
}

func TestCIWaitPullRequestHonorsPinnedRequiredCheckIntegration(t *testing.T) {
	for _, test := range []struct {
		name       string
		appID      string
		wantStatus string
		wantCode   int
		slice      string
	}{
		{name: "matching app", appID: "42", wantStatus: "passed", wantCode: exitOK, slice: "15s"},
		{name: "same name wrong app", appID: "7", wantStatus: "pending", wantCode: exitFindings, slice: "5s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "bin")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then
  echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then
  echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0
fi
if [ "$1" = pr ] && [ "$2" = checks ]; then
  echo '[{"name":"CI","bucket":"pass","link":"https://example.test/pr-check"}]'; exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  echo '{"protected":true,"protection":{}}'; exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then
  echo '[[{"type":"required_status_checks","ruleset_source_type":"Repository","ruleset_source":"acme/app","ruleset_id":7,"parameters":{"strict_required_status_checks_policy":true,"required_status_checks":[{"context":"CI","integration_id":42}]}}]]'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":` + test.appID + `}}]}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":1,"statuses":[{"context":"CI","state":"success"}]}'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`
			writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			var stdout, stderr bytes.Buffer
			code := run([]string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", test.slice, "--interval", "100ms", "--json"}, &stdout, &stderr)
			var output ciWaitOutput
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatal(err)
			}
			if code != test.wantCode || string(output.Status) != test.wantStatus || len(output.RequiredChecks) != 1 || output.RequiredChecks[0].IntegrationID != 42 {
				t.Fatalf("PR pinned integration receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
			}
			if test.wantStatus == "pending" && !strings.Contains(output.Reason, "GitHub App 42") {
				t.Fatalf("PR summary or wrong producer satisfied pinned requirement: %+v", output)
			}
			if test.wantStatus == "passed" {
				foundProducer := false
				for _, check := range output.Checks {
					if check.Name == "check-run:CI" && check.AppID == 42 {
						foundProducer = true
					}
				}
				if !foundProducer || output.StableObservations != 2 {
					t.Fatalf("PR pass lacks producer-aware stable exact-head receipt: %+v", output)
				}
			}
		})
	}
}

func TestCIWaitFreshPullRequestDoesNotWaitForRedTargetCI(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then
  echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0
fi
if [ "$1" = pr ] && [ "$2" = checks ]; then
  echo '[{"name":"CI","bucket":"pass"}]'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then
  echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/commits/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/check-runs'; then
  echo '{"total_count":1,"check_runs":[{"name":"Target CI","status":"completed","conclusion":"failure","app":{"id":42}}]}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'; exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  echo '{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"CI","app_id":42}]}}}'; exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then
  echo '{"strict":true,"contexts":[],"checks":[{"context":"CI","app_id":42}]}'; exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then
  echo '[[]]'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitOK || output.Status != "passed" || output.ObservedTargetHead != ciWaitTargetHead || !output.CandidateContainsTarget || !strings.Contains(output.TargetFreshnessAuthority, "strict") {
		t.Fatalf("fresh PR receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

func TestCIWaitRejectsStalePullRequestBeforeChecks(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then
  echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then
  echo '{"status":"behind","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"cccccccccccccccccccccccccccccccccccccccc"}}'; exit 0
fi
if [ "$1" = pr ] && [ "$2" = checks ]; then
  echo 'stale candidate reached checks' >&2; exit 31
fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || output.CandidateContainsTarget || !strings.Contains(output.Reason, "does not contain current target") {
		t.Fatalf("stale PR receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

func TestCIWaitRejectsPullRequestWithoutServerFreshnessFence(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then echo '[{"name":"CI","bucket":"pass"}]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["CI"]}}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then echo '{"strict":false,"contexts":["CI"],"checks":[]}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[[]]'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || output.TargetFreshnessAuthority != "" || !strings.Contains(output.Reason, "server-enforced strict up-to-date fence") {
		t.Fatalf("unfenced PR receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

func TestCIWaitRejectsClassicFreshnessPolicyWithoutStrictReceipt(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then echo '[{"name":"CI","bucket":"pass"}]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["CI"]}}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then echo '{"contexts":["CI"],"checks":[]}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[[]]'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "pending" || output.TargetFreshnessAuthority != "" || !strings.Contains(output.Reason, "omitted strict") {
		t.Fatalf("missing strict receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

func TestCIWaitRejectsEmptyStrictFreshnessPolicy(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then case " $* " in *" --required "*) echo '[]';; *) echo '[{"name":"Optional","bucket":"pass"}]';; esac; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"Optional","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{"required_status_checks":{}}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then echo '{"strict":true,"contexts":[],"checks":[]}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[[]]'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || output.TargetFreshnessAuthority != "" || !strings.Contains(output.Reason, "server-enforced strict up-to-date fence") {
		t.Fatalf("empty strict policy receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

func TestCIWaitPassesWithEmptyClassic404AndStrictRuleset(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then echo '[{"name":"CI","bucket":"pass"}]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{"required_status_checks":{}}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then echo 'gh: Not Found (HTTP 404)' >&2; exit 1; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[[{"type":"required_status_checks","ruleset_source_type":"Repository","ruleset_source":"acme/app","ruleset_id":7,"parameters":{"strict_required_status_checks_policy":true,"required_status_checks":[{"context":"CI","integration_id":42}]}}]]'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitOK || output.Status != "passed" || output.StableObservations != 2 || len(output.RequiredChecks) != 1 || output.RequiredChecks[0].IntegrationID != 42 || !strings.Contains(output.TargetFreshnessAuthority, "strict required-status-check ruleset 7") {
		t.Fatalf("ruleset-only success = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

func TestCIWaitRejectsEmptyClassic404WithoutRuleset(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then echo '[{"name":"CI","bucket":"pass"}]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{"required_status_checks":{}}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then echo 'gh: Not Found (HTTP 404)' >&2; exit 1; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[[]]'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || output.TargetFreshnessAuthority != "" || !strings.Contains(output.Reason, "server-enforced strict up-to-date fence") {
		t.Fatalf("ruleset-only without ruleset = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

func TestCIWaitDoesNotTreatMergeQueueRuleAsSourceHeadFreshness(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then echo '[{"name":"CI","bucket":"pass"}]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{}}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[[{"type":"merge_queue","ruleset_source_type":"Repository","ruleset_source":"acme/app","ruleset_id":9,"parameters":{}}]]'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "pending" || !strings.Contains(output.Reason, "merge-group check observation") {
		t.Fatalf("merge-queue source-head receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

func TestCIWaitRejectsTargetAdvanceAfterStablePullRequestChecks(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "target-reads")
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then echo '[{"name":"CI","bucket":"pass"}]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  count=0; if [ -f "$WB_TARGET_STATE" ]; then count=$(cat "$WB_TARGET_STATE"); fi
  count=$((count + 1)); printf '%s' "$count" > "$WB_TARGET_STATE"
  if [ "$count" -le 2 ]; then echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; else echo '{"object":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}'; fi
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["CI"]}}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then echo '{"strict":true,"contexts":["CI"],"checks":[]}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[[]]'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_TARGET_STATE", state)
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || output.StableObservations != 2 || !strings.Contains(output.Reason, "advanced after checks passed") {
		t.Fatalf("target-advance receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

func TestCIWaitCannotPassWithoutAuthoritativeBranchRules(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":false,"protection":{}}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo 'rules unavailable' >&2; exit 1; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success"}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "pending" || output.StableObservations != 0 || !strings.Contains(output.Reason, "authority is unavailable") {
		t.Fatalf("missing authority receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

func TestCIWaitCannotClaimAuthorityForRequiredWorkflowRule(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{}}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[[{"type":"workflows","ruleset_source_type":"Organization","ruleset_source":"acme","ruleset_id":9,"parameters":{}}]]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success"}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "pending" || !strings.Contains(output.Reason, "expected check names") {
		t.Fatalf("required-workflow authority receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

func TestCIWaitRejectsIncompleteDirectCheckPagination(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":101,"check_runs":[]}' ; exit 0; fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || !strings.Contains(output.Reason, "incomplete CI receipt") {
		t.Fatalf("incomplete page = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

func TestCIWaitRejectsIncompleteDirectStatusPagination(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":0,"check_runs":[]}' ; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":101,"statuses":[]}' ; exit 0; fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || !strings.Contains(output.Reason, "incomplete CI receipt") {
		t.Fatalf("incomplete status page = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

func TestCIWaitRejectsDirectTargetHeadDrift(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/'; then
  echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'
  exit 0
fi
echo "checks must not run after target drift" >&2
exit 31
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--target", "task/integration", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || !strings.Contains(output.Reason, "target task/integration advanced") {
		t.Fatalf("direct target drift = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

func TestCIWaitRejectsInvalidTargetBranch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--target", "not a branch", "--head", ciWaitHead, "--json"}, &stdout, &stderr)
	if code != exitUsage || !strings.Contains(stderr.String(), "valid Git branch") {
		t.Fatalf("invalid target = code %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestPrintCIWaitShellQuotesResumeArguments(t *testing.T) {
	command := newCIWaitCmd()
	var output bytes.Buffer
	command.SetOut(&output)
	if err := printCIWait(command, ciWaitOutput{
		PullRequestWaitResult: orchestrate.PullRequestWaitResult{
			Status:     orchestrate.PullRequestWaitPending,
			Repository: "acme/app",
			Target:     "feature/$(touch-pwned)",
			Head:       ciWaitHead,
			Reason:     "resume",
		},
		ResumeArgs: []string{"wb", "ci", "wait", "--target", "feature/$(touch-pwned)", "--pr", "https://example.test/pr/1?x='y'"},
	}); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, quoted := range []string{`'feature/$(touch-pwned)'`, `'https://example.test/pr/1?x='"'"'y'"'"''`} {
		if !strings.Contains(got, quoted) {
			t.Fatalf("human resume command is not shell-safe; missing %q in %q", quoted, got)
		}
	}
}

func writeCIWaitExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}
