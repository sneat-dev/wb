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

	"github.com/sneat-dev/wb/internal/cli/cmdci"
	"github.com/sneat-dev/wb/internal/testenv"
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
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":17,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"feature/integration","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/feature/integration'; then
  echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then
  echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  count=0
  if [ -f "$WB_CI_WAIT_STATE" ]; then count=$(cat "$WB_CI_WAIT_STATE"); fi
  count=$((count + 1))
  printf '%s' "$count" > "$WB_CI_WAIT_STATE"
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
  echo '[]'
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
		var output cmdci.WaitOutput
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

// A failing check is a failed receipt whatever the transport did. This used to
// be expressed against `gh pr checks`, which exits non-zero while still
// printing its JSON; the waiter no longer speaks that dialect — the installed
// gh 2.45 does not support it — so the property is now expressed against the
// head commit's own check runs, which is where the fact actually lives.
func TestCIWaitFailedCheckRunProducesAFailedReceipt(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":17,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then
  echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"failure","html_url":"https://github.com/acme/app/actions/runs/123/job/456","app":{"id":42}}]}'
  exit 0
fi
if [ "$1" = run ] && [ "$2" = view ]; then
  echo 'compile error: unexpected type'
  echo 'token ghp_notForOutput'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '^repos/acme/app/branches/main$'; then
  echo '{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"CI","app_id":42}]}}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '^repos/acme/app/branches/main/protection/required_status_checks$'; then
  echo '{"strict":true,"contexts":[],"checks":[{"context":"CI","app_id":42}]}'; exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then
  echo '[]'; exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", "20s", "--interval", "100ms", "--format=json"}, &stdout, &stderr)
	var output cmdci.WaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || len(output.Checks) != 1 ||
		output.Checks[0].Bucket != "fail" || output.Checks[0].Name != "check-run:CI" {
		t.Fatalf("failed-check receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if len(output.FailureDetails) != 1 || output.FailureDetails[0].RunURL != "https://github.com/acme/app/actions/runs/123" ||
		output.FailureDetails[0].JobURL != "https://github.com/acme/app/actions/runs/123/job/456" ||
		!strings.Contains(output.FailureDetails[0].Excerpt, "compile error") || strings.Contains(output.FailureDetails[0].Excerpt, "ghp_notForOutput") {
		t.Fatalf("failed job diagnostic = %+v", output.FailureDetails)
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
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
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
		var output cmdci.WaitOutput
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

func TestCIWaitRejectsInvalidTargetBranch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--target", "not a branch", "--head", ciWaitHead, "--json"}, &stdout, &stderr)
	if code != exitUsage || !strings.Contains(stderr.String(), "valid Git branch") {
		t.Fatalf("invalid target = code %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func writeCIWaitExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if !strings.Contains(contents, "/actions/runs?head_sha=") {
		const response = `if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":0,"workflow_runs":[]}'
  exit 0
fi
`
		contents = strings.Replace(contents, "#!/bin/sh\n", "#!/bin/sh\n"+response, 1)
	}
	// Every fake GitHub process must observe only the responses prepared by
	// this test. Reusing the real per-user observer cache lets another test or
	// WB process supply a fresh cached response for the same acme/app fixture.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Sharded package tests are separate processes. Isolate WB's private state
	// too, so a concurrent shard cannot supply or replace observer evidence.
	t.Setenv("WB_PROJECTS_ROOT", t.TempDir())
	if err := testenv.WriteExecutableFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}
