package prsnapshot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// writeFakeGH puts a fake `gh` on PATH ahead of any real one, so this
// package's tests never reach the real GitHub API — matching the seam
// orchestrate.ReadPullRequest / PullRequestHeadChecks already go through.
func writeFakeGH(t *testing.T, script string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "/actions/runs?head_sha=") {
		const response = `if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":0,"workflow_runs":[]}'
  exit 0
fi
`
		script = strings.Replace(script, "#!/bin/sh\n", "#!/bin/sh\n"+response, 1)
	}
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const fakeHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// TestObserveOpenPullRequestWithPassingChecks proves a fully green, open pull
// request reports Green true with no failure or blocked evidence.
func TestObserveOpenPullRequestWithPassingChecks(t *testing.T) {
	writeFakeGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/5$'; then
  echo '{"number":5,"state":"open","draft":false,"merged":false,"mergeable_state":"clean","html_url":"https://example.invalid/5","head":{"ref":"feature","sha":"`+fakeHead+`"},"base":{"ref":"main","sha":"b"}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'check-runs'; then
  echo '{"total_count":1,"check_runs":[{"name":"build","status":"completed","conclusion":"success","app":{"id":1,"slug":"gh"}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status'; then
  echo '{"state":"success","statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'rules/branches/main'; then echo '[]'; exit 0; fi
if [ "$1" = api ]; then echo '{}'; exit 0; fi
echo "unexpected gh args: $*" >&2
exit 30
`)
	snapshot := Observe(context.Background(), "acme/app", "5")
	if snapshot.Err != nil {
		t.Fatalf("Err = %v", snapshot.Err)
	}
	if !snapshot.Green || snapshot.Merged || snapshot.State != "open" {
		t.Fatalf("snapshot = %+v, want green open unmerged", snapshot)
	}
	if len(snapshot.Failed) != 0 || len(snapshot.Blocked) != 0 {
		t.Fatalf("snapshot = %+v, want no failed or blocked checks", snapshot)
	}
	if snapshot.Head != fakeHead || snapshot.Base != "main" {
		t.Fatalf("snapshot head/base = %q/%q", snapshot.Head, snapshot.Base)
	}
}

// TestObserveOpenPullRequestWithFailedCheck proves a failed check surfaces
// in Failed and Failures without Green ever reading true.
func TestObserveOpenPullRequestWithFailedCheck(t *testing.T) {
	writeFakeGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/6$'; then
  echo '{"number":6,"state":"open","draft":false,"merged":false,"mergeable_state":"dirty","html_url":"https://example.invalid/6","head":{"ref":"feature","sha":"`+fakeHead+`"},"base":{"ref":"main","sha":"b"}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'check-runs'; then
  echo '{"total_count":1,"check_runs":[{"name":"build","status":"completed","conclusion":"failure","html_url":"https://example.invalid/run/1","app":{"id":1,"slug":"gh"}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status'; then
  echo '{"state":"success","statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'rules/branches/main'; then echo '[]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs/'; then echo '{}'; exit 0; fi
if [ "$1" = api ]; then echo '{}'; exit 0; fi
echo "unexpected gh args: $*" >&2
exit 30
`)
	snapshot := Observe(context.Background(), "acme/app", "6")
	if snapshot.Err != nil {
		t.Fatalf("Err = %v", snapshot.Err)
	}
	if snapshot.Green {
		t.Fatalf("snapshot = %+v, want Green false", snapshot)
	}
	if len(snapshot.Failed) != 1 || snapshot.Failed[0] != "check-run:build" {
		t.Fatalf("snapshot.Failed = %v, want exactly the failed check", snapshot.Failed)
	}
}

// TestObserveMergedPullRequestReportsMergedAndRawState proves Observe never
// rewrites State to "merged" itself — that overlay is cmd/wb's own, kept for
// `wb wait pr` backward compatibility — and instead reports the fact
// separately via Merged, so a caller like the daemon watcher can classify a
// merged outcome without parsing State text.
func TestObserveMergedPullRequestReportsMergedAndRawState(t *testing.T) {
	writeFakeGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/12$'; then
  echo '{"number":12,"state":"closed","draft":false,"merged":true,"mergeable_state":"clean","html_url":"https://example.invalid/12","head":{"ref":"feature","sha":"`+fakeHead+`"},"base":{"ref":"main","sha":"b"}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'check-runs'; then
  echo '{"total_count":0,"check_runs":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status'; then
  echo '{"state":"success","statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/branches/main/protection'; then
  echo '{"required_status_checks":{"strict":true,"checks":[]}}'
  exit 0
fi
if [ "$1" = api ]; then echo '{}'; exit 0; fi
echo "unexpected gh args: $*" >&2
exit 30
`)
	snapshot := Observe(context.Background(), "acme/app", "12")
	if snapshot.Err != nil {
		t.Fatalf("Err = %v", snapshot.Err)
	}
	if !snapshot.Merged || snapshot.State != "closed" {
		t.Fatalf("snapshot = %+v, want Merged=true State=closed (raw, unrewritten)", snapshot)
	}
}

// TestObserveClosedPullRequestSkipsChecksOnReadError proves a closed pull
// request whose head checks can no longer be read (GitHub may retire a
// closed head's check data) reports the closure rather than an error.
func TestObserveClosedPullRequestSkipsChecksOnReadError(t *testing.T) {
	writeFakeGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/7$'; then
  echo '{"number":7,"state":"closed","draft":false,"merged":false,"mergeable_state":"unknown","html_url":"https://example.invalid/7","head":{"ref":"feature","sha":"`+fakeHead+`"},"base":{"ref":"main","sha":"b"}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'check-runs'; then
  echo 'gh: not found (HTTP 404)' >&2
  exit 1
fi
echo "unexpected gh args: $*" >&2
exit 30
`)
	snapshot := Observe(context.Background(), "acme/app", "7")
	if snapshot.Err != nil {
		t.Fatalf("Err = %v, want the closed short-circuit instead of an error", snapshot.Err)
	}
	if snapshot.State != "closed" || snapshot.Merged {
		t.Fatalf("snapshot = %+v, want closed and unmerged", snapshot)
	}
	if snapshot.Checks == nil || len(snapshot.Checks) != 0 {
		t.Fatalf("snapshot.Checks = %#v, want an empty, non-nil map", snapshot.Checks)
	}
}

// TestObserveReturnsErrorWhenThePullRequestCannotBeRead proves a hard read
// failure on an unresolved pull request surfaces as Err, not a zero Snapshot
// silently reported as if it were a real observation.
func TestObserveReturnsErrorWhenThePullRequestCannotBeRead(t *testing.T) {
	writeFakeGH(t, `#!/bin/sh
echo "unexpected gh args: $*" >&2
exit 30
`)
	snapshot := Observe(context.Background(), "acme/app", "999")
	if snapshot.Err == nil {
		t.Fatal("Err = nil, want the unreadable pull request to surface")
	}
}

// TestObserveNamesAnUnsatisfiedRequiredCheck proves the renamed-workflow
// trap still surfaces through Blocked: a required check absent from the
// observed set is named, not silently treated as green.
func TestObserveNamesAnUnsatisfiedRequiredCheck(t *testing.T) {
	writeFakeGH(t, `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/8$'; then
  echo '{"number":8,"state":"open","draft":false,"merged":false,"mergeable_state":"unknown","html_url":"https://example.invalid/8","head":{"ref":"feature","sha":"`+fakeHead+`"},"base":{"ref":"main","sha":"b"}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'check-runs'; then
  echo '{"total_count":0,"check_runs":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status'; then
  echo '{"state":"success","statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["build"],"checks":[]}}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'rules/branches/main'; then echo '[]'; exit 0; fi
if [ "$1" = api ]; then echo '{}'; exit 0; fi
echo "unexpected gh args: $*" >&2
exit 30
`)
	snapshot := Observe(context.Background(), "acme/app", "8")
	if snapshot.Err != nil {
		t.Fatalf("Err = %v", snapshot.Err)
	}
	if snapshot.Green {
		t.Fatal("Green = true, want false: the required check never registered")
	}
	if len(snapshot.Blocked) != 1 || snapshot.Blocked[0] != "build" {
		t.Fatalf("Blocked = %v, want [build]", snapshot.Blocked)
	}
}
