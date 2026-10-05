//go:build e2e

package orchestrate_test

import (
	"bytes"
	"encoding/json"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitFailedTerminalCheckUsesAnnotationsBeforeUnavailableLogs(t *testing.T) {
	t.Setenv("WB_PROJECTS_ROOT", t.TempDir())
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -Fq '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -Fq '/check-runs/101461768420/annotations?per_page=100'; then
  echo '[{"path":"cmd/wb/ci.go","start_line":17,"end_line":17,"message":"unchecked error"},{"path":"cmd/wb/ci.go","start_line":17,"end_line":17,"message":"unchecked error"},{"path":"internal/orchestrate/ciwait.go","start_line":1001,"end_line":1001,"message":"unchecked error"},{"path":"cmd/wb/ci_wait_progress.go","start_line":84,"end_line":84,"message":"unchecked error"}]'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -Fq '/check-runs?per_page=100'; then
  echo '{"total_count":2,"check_runs":[{"id":101461768420,"name":"errcheck","status":"completed","conclusion":"failure","html_url":"https://github.com/acme/app/actions/runs/123/job/456","app":{"id":42}},{"id":101461768421,"name":"tests","status":"in_progress","conclusion":"","html_url":"https://github.com/acme/app/actions/runs/123/job/457","app":{"id":42}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -Fq '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
if [ "$1" = run ] && [ "$2" = view ]; then
  echo 'logs are unavailable while the workflow is running' >&2
  exit 31
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--format=json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("machine JSON was not preserved: %v; stdout=%s", err, stdout.String())
	}
	if code != exitFindings || output.Status != "failed" || len(output.FailureDetails) != 1 || len(output.FailureDetails[0].Annotations) != 3 || output.FailureDetails[0].Excerpt != "" {
		t.Fatalf("annotation failure receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if output.FailureDetails[0].Annotations[0].Path != "cmd/wb/ci.go" || output.FailureDetails[0].Annotations[0].StartLine != 17 || output.FailureDetails[0].Annotations[0].Message != "unchecked error" {
		t.Fatalf("first annotation = %#v", output.FailureDetails[0].Annotations[0])
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitDirectTargetStatusReceiptsBlockFailureAndAcceptStatusOnlyPass(t *testing.T) {
	for _, test := range []struct {
		name       string
		state      string
		wantStatus string
		wantCode   int
	}{
		{name: "status only pass", state: "success", wantStatus: "passed", wantCode: exitOK},
		{name: "status failure", state: "failure", wantStatus: "failed", wantCode: exitFindings},
	} {
		//nolint:paralleltest // Each real observer subtest replaces process PATH/cache/state environment.
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
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
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
			code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", "10s", "--interval", "100ms", "--json"}, &stdout, &stderr)
			var output orchestrate.PullRequestWaitResult
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

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitDirectTargetPassesWithAuthoritativeNoApplicableChecks(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "no-applicable-checks")
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/docs/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'; exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/docs/rules/branches/main?per_page=100'; then
  echo '[]'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  count=0; if [ -f "$WB_CI_WAIT_STATE" ]; then count=$(cat "$WB_CI_WAIT_STATE"); fi
  count=$((count + 1)); printf '%s' "$count" > "$WB_CI_WAIT_STATE"
  echo '{"total_count":0,"check_runs":[]}'; exit 0
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
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/docs", "--target", "main", "--head", ciWaitHead, "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitRereadInterval.String(), "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitOK || output.Status != "passed" || output.StableObservations != 2 || len(output.Checks) != 0 || len(output.RequiredChecks) != 0 || !strings.Contains(output.Reason, "no checks") {
		t.Fatalf("no-applicable-check receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if observations := ciWaitObservations(t, state); observations != 2 {
		t.Fatalf("no-applicable-check receipt observed %d times, want an initial receipt and unchanged reread", observations)
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitDirectTargetWaitsWhenCheckRegistersAfterEmptyReceipt(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "late-after-empty")
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/docs/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'; exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/docs/rules/branches/main?per_page=100'; then
  echo '[]'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  count=0; if [ -f "$WB_CI_WAIT_STATE" ]; then count=$(cat "$WB_CI_WAIT_STATE"); fi
  count=$((count + 1)); printf '%s' "$count" > "$WB_CI_WAIT_STATE"
  if [ "$count" -eq 1 ]; then
    echo '{"total_count":0,"check_runs":[]}'
  elif [ "$count" -eq 2 ]; then
    echo '{"total_count":1,"check_runs":[{"name":"website","status":"queued"}]}'
  else
    echo '{"total_count":1,"check_runs":[{"name":"website","status":"completed","conclusion":"success"}]}'
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
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/docs", "--target", "main", "--head", ciWaitHead, "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitRereadInterval.String(), "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitOK || output.Status != "passed" || output.StableObservations != 2 || len(output.Checks) != 1 || output.Checks[0].Name != "check-run:website" {
		t.Fatalf("late check after empty receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if observations := ciWaitObservations(t, state); observations != 4 {
		t.Fatalf("expected empty receipt, late pending check, then two stable terminal checks; observations=%d", observations)
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitWaitsForStableRereadAfterLateSuiteRegistration(t *testing.T) {
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
  echo '[]'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
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
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitRereadInterval.String(), "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
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

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitConfirmsTerminalChecksWithoutWaitingAFullPollInterval(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "already-green")
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["CI"]}}}'; exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then
  echo '[]'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  count=0
  if [ -f "$WB_CI_WAIT_STATE" ]; then count=$(cat "$WB_CI_WAIT_STATE"); fi
  count=$((count + 1)); printf '%s' "$count" > "$WB_CI_WAIT_STATE"
  echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success"}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_CI_WAIT_STATE", state)
	previousDelay := orchestrate.DefaultStableRereadDelay
	orchestrate.DefaultStableRereadDelay = 300 * time.Millisecond
	t.Cleanup(func() { orchestrate.DefaultStableRereadDelay = previousDelay })
	var stdout, stderr bytes.Buffer
	// The interval leaves no room for a second quota-cadence poll inside the
	// slice, exactly like a default 30s cadence against real CI. A check set
	// that is already terminal on the first observation must still confirm
	// its stable reread within this same slice on the shorter confirmation
	// delay instead of returning a pending receipt the caller has to resume.
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitSingleObservationInterval.String(), "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitOK || output.Status != "passed" || output.StableObservations != 2 {
		t.Fatalf("terminal confirmation = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if observations := ciWaitObservations(t, state); observations != 2 {
		t.Fatalf("expected one terminal observation plus one confirming reread in a single slice; observations=%d", observations)
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitDirectTargetHonorsPinnedRequiredCheckIntegration(t *testing.T) {
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
		//nolint:paralleltest // Each real observer subtest replaces process PATH/cache/state environment.
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
  if echo "$*" | grep -Fq -- '--slurp'; then echo 'active rules must not use --slurp: gh 2.45 has no such flag' >&2; exit 31; fi
  echo '[{"type":"required_status_checks","ruleset_source_type":"Repository","ruleset_source":"acme/app","ruleset_id":7,"parameters":{"required_status_checks":[{"context":"CI","integration_id":42}]}}]'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
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
			code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", test.slice, "--interval", "100ms", "--json"}, &stdout, &stderr)
			var output orchestrate.PullRequestWaitResult
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

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitPullRequestHonorsPinnedRequiredCheckIntegration(t *testing.T) {
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
		//nolint:paralleltest // Each real observer subtest replaces process PATH/cache/state environment.
		t.Run(test.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "bin")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then
  echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":17,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
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
  echo '[{"type":"required_status_checks","ruleset_source_type":"Repository","ruleset_source":"acme/app","ruleset_id":7,"parameters":{"strict_required_status_checks_policy":true,"required_status_checks":[{"context":"CI","integration_id":42}]}}]'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
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
			code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", test.slice, "--interval", "100ms", "--json"}, &stdout, &stderr)
			var output orchestrate.PullRequestWaitResult
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

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitFreshPullRequestDoesNotWaitForRedTargetCI(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then
  echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":17,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
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
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
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
  echo '[]'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", ciWaitSliceBudget.String(), "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitOK || output.Status != "passed" || output.ObservedTargetHead != ciWaitTargetHead || !output.CandidateContainsTarget || !strings.Contains(output.TargetFreshnessAuthority, "strict") {
		t.Fatalf("fresh PR receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitRejectsStalePullRequestBeforeChecks(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then
  echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":17,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
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
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", ciWaitSliceBudget.String(), "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || output.CandidateContainsTarget || !strings.Contains(output.Reason, "does not contain current target") {
		t.Fatalf("stale PR receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitRejectsPullRequestWithoutServerFreshnessFence(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":17,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then echo '[{"name":"CI","bucket":"pass"}]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["CI"]}}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then echo '{"strict":false,"contexts":["CI"],"checks":[]}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[]'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", ciWaitSliceBudget.String(), "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || output.TargetFreshnessAuthority != "" || !strings.Contains(output.Reason, "server-enforced strict up-to-date fence") {
		t.Fatalf("unfenced PR receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitRejectsClassicFreshnessPolicyWithoutStrictReceipt(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":17,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then echo '[{"name":"CI","bucket":"pass"}]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["CI"]}}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then echo '{"contexts":["CI"],"checks":[]}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[]'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", ciWaitSliceBudget.String(), "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "pending" || output.TargetFreshnessAuthority != "" || !strings.Contains(output.Reason, "omitted strict") {
		t.Fatalf("missing strict receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitRejectsEmptyStrictFreshnessPolicy(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":17,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then case " $* " in *" --required "*) echo '[]';; *) echo '[{"name":"Optional","bucket":"pass"}]';; esac; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"Optional","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{"required_status_checks":{}}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then echo '{"strict":true,"contexts":[],"checks":[]}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[]'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", ciWaitSliceBudget.String(), "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || output.TargetFreshnessAuthority != "" || !strings.Contains(output.Reason, "server-enforced strict up-to-date fence") {
		t.Fatalf("empty strict policy receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitPassesWithEmptyClassic404AndStrictRuleset(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":17,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then echo '[{"name":"CI","bucket":"pass"}]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{"required_status_checks":{}}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then echo 'gh: Not Found (HTTP 404)' >&2; exit 1; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[{"type":"required_status_checks","ruleset_source_type":"Repository","ruleset_source":"acme/app","ruleset_id":7,"parameters":{"strict_required_status_checks_policy":true,"required_status_checks":[{"context":"CI","integration_id":42}]}}]'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", ciWaitSliceBudget.String(), "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitOK || output.Status != "passed" || output.StableObservations != 2 || len(output.RequiredChecks) != 1 || output.RequiredChecks[0].IntegrationID != 42 || !strings.Contains(output.TargetFreshnessAuthority, "strict required-status-check ruleset 7") {
		t.Fatalf("ruleset-only success = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitRejectsEmptyClassic404WithoutRuleset(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":17,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then echo '[{"name":"CI","bucket":"pass"}]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{"required_status_checks":{}}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then echo 'gh: Not Found (HTTP 404)' >&2; exit 1; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[]'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", ciWaitSliceBudget.String(), "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || output.TargetFreshnessAuthority != "" || !strings.Contains(output.Reason, "server-enforced strict up-to-date fence") {
		t.Fatalf("ruleset-only without ruleset = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitDoesNotTreatMergeQueueRuleAsSourceHeadFreshness(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":17,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then echo '[{"name":"CI","bucket":"pass"}]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{}}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[{"type":"merge_queue","ruleset_source_type":"Repository","ruleset_source":"acme/app","ruleset_id":9,"parameters":{}}]'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "pending" || !strings.Contains(output.Reason, "merge-group check observation") {
		t.Fatalf("merge-queue source-head receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitRejectsTargetAdvanceAfterStablePullRequestChecks(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "target-reads")
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":17,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then echo '[{"name":"CI","bucket":"pass"}]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  count=0; if [ -f "$WB_TARGET_STATE" ]; then count=$(cat "$WB_TARGET_STATE"); fi
  count=$((count + 1)); printf '%s' "$count" > "$WB_TARGET_STATE"
  if [ "$count" -le 2 ]; then echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; else echo '{"object":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}'; fi
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["CI"]}}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then echo '{"strict":true,"contexts":["CI"],"checks":[]}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[]'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_TARGET_STATE", state)
	var stdout, stderr bytes.Buffer
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", "20s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || output.StableObservations != 2 || !strings.Contains(output.Reason, "advanced after checks passed") {
		t.Fatalf("target-advance receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitCannotPassWithoutAuthoritativeBranchRules(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":false,"protection":{}}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo 'rules unavailable' >&2; exit 1; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success"}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "pending" || output.StableObservations != 0 || !strings.Contains(output.Reason, "authority is unavailable") {
		t.Fatalf("missing authority receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitCannotClaimAuthorityForRequiredWorkflowRule(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{}}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[{"type":"workflows","ruleset_source_type":"Organization","ruleset_source":"acme","ruleset_id":9,"parameters":{}}]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success"}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
echo "unexpected gh args: $*" >&2; exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "pending" || !strings.Contains(output.Reason, "expected check names") {
		t.Fatalf("required-workflow authority receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitRejectsIncompleteDirectCheckPagination(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":101,"check_runs":[]}' ; exit 0; fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || !strings.Contains(output.Reason, "incomplete CI receipt") {
		t.Fatalf("incomplete page = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitRejectsIncompleteDirectStatusPagination(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"0123456789012345678901234567890123456789","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":0,"check_runs":[]}' ; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":101,"statuses":[]}' ; exit 0; fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || !strings.Contains(output.Reason, "incomplete CI receipt") {
		t.Fatalf("incomplete status page = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}

//nolint:paralleltest // Real observer fixtures replace process PATH and private-state environment.
func TestE2ECIWaitRejectsDirectTargetHeadDrift(t *testing.T) {
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
	code := observeForTest(t, []string{"ci", "wait", "--repo", "acme/app", "--target", "task/integration", "--head", ciWaitHead, "--slice", "5s", "--interval", "100ms", "--json"}, &stdout, &stderr)
	var output orchestrate.PullRequestWaitResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || output.Status != "failed" || !strings.Contains(output.Reason, "target task/integration advanced") {
		t.Fatalf("direct target drift = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
}
