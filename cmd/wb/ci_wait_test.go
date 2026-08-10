package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ciWaitHead = "0123456789012345678901234567890123456789"

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
if [ "$1" = pr ] && [ "$2" = checks ]; then
  case " $* " in *" --required "*) echo "ci wait must observe all checks, not only required" >&2; exit 29;; esac
  count=0
  if [ -f "$WB_CI_WAIT_STATE" ]; then count=$(cat "$WB_CI_WAIT_STATE"); fi
  count=$((count + 1))
  printf '%s' "$count" > "$WB_CI_WAIT_STATE"
  if [ "$count" -lt 3 ]; then
    echo '[{"name":"CI","bucket":"pending"}]'
	# gh pr checks exits 8 while a JSON receipt contains pending checks.
	# WB must parse this receipt and return a resumable pending result.
    exit 8
  else
    echo '[{"name":"CI","bucket":"pass"}]'
  fi
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_CI_WAIT_STATE", state)
	// Keep one observation per invocation (`interval > slice`), but leave room
	// for process scheduling while the complete coverage suite runs in parallel.
	arguments := []string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "feature/integration", "--head", ciWaitHead, "--slice", "8s", "--interval", "9s", "--json"}
	for invocation := 1; invocation <= 3; invocation++ {
		var stdout, stderr bytes.Buffer
		code := run(arguments, &stdout, &stderr)
		var output ciWaitOutput
		if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
			t.Fatalf("slice %d output=%q: %v", invocation, stdout.String(), err)
		}
		if invocation < 3 {
			if code != exitFindings || output.Status != "pending" || len(output.ResumeArgs) == 0 {
				t.Fatalf("slice %d = code %d output=%+v stderr=%s", invocation, code, output, stderr.String())
			}
			joined := strings.Join(output.ResumeArgs, " ")
			for _, required := range []string{"--repo acme/app", "--pr 17", "--target feature/integration", "--head " + ciWaitHead, "--json"} {
				if !strings.Contains(joined, required) {
					t.Fatalf("slice %d resume=%q missing %q", invocation, joined, required)
				}
			}
			continue
		}
		if code != exitOK || output.Status != "passed" || output.ObservedHead != ciWaitHead {
			t.Fatalf("terminal slice = code %d output=%+v stderr=%s", code, output, stderr.String())
		}
	}
	if contents, err := os.ReadFile(state); err != nil || string(contents) != "3" {
		t.Fatalf("expected exactly three foreground slices, state=%q err=%v", contents, err)
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
if [ "$1" = pr ] && [ "$2" = checks ]; then
  echo '[{"name":"CI","bucket":"fail"}]'
  exit 1
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--pr", "17", "--target", "main", "--head", ciWaitHead, "--slice", "8s", "--interval", "9s", "--json"}, &stdout, &stderr)
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
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  count=0
  if [ -f "$WB_CI_WAIT_STATE" ]; then count=$(cat "$WB_CI_WAIT_STATE"); fi
  count=$((count + 1))
  printf '%s' "$count" > "$WB_CI_WAIT_STATE"
  if [ "$count" -lt 3 ]; then
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
	arguments := []string{"ci", "wait", "--repo", "acme/app", "--target", "feature/integration", "--head", ciWaitHead, "--slice", "8s", "--interval", "9s", "--json"}
	for invocation := 1; invocation <= 3; invocation++ {
		var stdout, stderr bytes.Buffer
		code := run(arguments, &stdout, &stderr)
		var output ciWaitOutput
		if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
			t.Fatalf("slice %d output=%q: %v", invocation, stdout.String(), err)
		}
		if invocation < 3 {
			if code != exitFindings || output.Status != "pending" || len(output.ResumeArgs) == 0 {
				t.Fatalf("slice %d = code %d output=%+v stderr=%s", invocation, code, output, stderr.String())
			}
			continue
		}
		if code != exitOK || output.Status != "passed" || output.ObservedHead != ciWaitHead {
			t.Fatalf("terminal direct slice = code %d output=%+v stderr=%s", code, output, stderr.String())
		}
		if len(output.Checks) != 2 || output.Checks[0].Name != "check-run:lint" || output.Checks[1].Name != "check-run:test" {
			t.Fatalf("direct checks were not deterministically sorted: %#v", output.Checks)
		}
	}
	if contents, err := os.ReadFile(state); err != nil || string(contents) != "3" {
		t.Fatalf("expected exactly three direct foreground slices, state=%q err=%v", contents, err)
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
			code := run([]string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", "8s", "--interval", "9s", "--json"}, &stdout, &stderr)
			var output ciWaitOutput
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatal(err)
			}
			if code != test.wantCode || string(output.Status) != test.wantStatus || len(output.Checks) != 1 || output.Checks[0].Name != "status:legacy" {
				t.Fatalf("status receipt = code %d output=%+v stderr=%s", code, output, stderr.String())
			}
		})
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
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", "8s", "--interval", "9s", "--json"}, &stdout, &stderr)
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
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead, "--slice", "3s", "--interval", "4s", "--json"}, &stdout, &stderr)
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
	code := run([]string{"ci", "wait", "--repo", "acme/app", "--target", "task/integration", "--head", ciWaitHead, "--slice", "8s", "--interval", "9s", "--json"}, &stdout, &stderr)
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

func writeCIWaitExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}
