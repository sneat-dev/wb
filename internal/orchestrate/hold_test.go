package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// holdGHScript answers everything a green exact-head PR-check wait needs, and
// treats any merge attempt as a test failure. That inversion is the point: the
// assertion is not "Merged is false" (a bug could set it false while still
// merging server-side) but "gh pr merge was never invoked at all".
const holdGHScript = `#!/bin/sh
# The pull-request head is whatever the engine actually pushed, read straight
# from the fixture's bare remote, so the stub cannot drift from the run.
HEADSHA=$(git --git-dir="$WB_HOLD_REMOTE" rev-parse refs/heads/wb/deps/test)
if [ "$1" = pr ] && [ "$2" = list ]; then exit 0; fi
if [ "$1" = pr ] && [ "$2" = create ]; then echo 'https://github.test/acme/app/pull/1'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"'"$HEADSHA"'","baseRefName":"main"}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then echo '[{"name":"CI","bucket":"pass"}]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"'"$HEADSHA"'"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/'; then echo '{"status":"identical","base_commit":{"sha":"'"$HEADSHA"'"},"merge_base_commit":{"sha":"'"$HEADSHA"'"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then echo '{"number":1,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"'"$HEADSHA"'","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"CI","app_id":42}]}}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then echo '{"strict":true,"contexts":[],"checks":[{"context":"CI","app_id":42}]}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[]'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = merge ]; then echo "HELD REPOSITORY WAS MERGED" > "$WB_HOLD_BREACH"; exit 0; fi
echo "unexpected gh args: $*" >&2
exit 2
`

// A held repository is one whose merge is a human decision — sneat-co/sneat-go
// and sneat-co/sneat-apps are the live examples. On 2026-09-02 there was no way
// to say that to `wb deps bump --merge` short of running the campaign without
// --merge and merging the rest by hand. Hold does all the mechanical work and
// stops at exactly the irreversible step.
func TestRunHoldOpensAndVerifiesThePullRequestButNeverMergesIt(t *testing.T) {
	fixture := newEngineFixture(t)
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	breach := filepath.Join(t.TempDir(), "breach")
	writeEngineFile(t, filepath.Join(bin, "gh"), holdGHScript)
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_HOLD_BREACH", breach)
	t.Setenv("WB_HOLD_REMOTE", fixture.repository.CloneURL)

	options := fixture.options()
	options.Merge = true
	options.Hold = []string{"acme/*"}
	options.Timeout = 30 * time.Second
	options.CheckPollInterval = time.Millisecond

	results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
	if err != nil {
		t.Fatalf("held run = %v (results %+v)", err, results)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	result := results[0]
	if _, statErr := os.Stat(breach); statErr == nil {
		t.Fatal("gh pr merge was invoked for a held repository")
	}
	if result.Merged {
		t.Fatalf("held result reports Merged: %+v", result)
	}
	if !result.Held {
		t.Fatalf("held result does not record the hold: %+v", result)
	}
	if result.PR == "" {
		t.Fatalf("held repository must still get a pull request: %+v", result)
	}
	if result.Status != "pr_open_held" || !strings.Contains(result.Reason, "--hold") {
		t.Fatalf("held result status/reason = %q / %q", result.Status, result.Reason)
	}
}

// The complement: without --hold, the same fixture merges. Without this, a
// hold test could pass because the merge path was broken for everyone.
func TestRunWithoutHoldStillMergesTheSameRepository(t *testing.T) {
	fixture := newEngineFixture(t)
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	breach := filepath.Join(t.TempDir(), "breach")
	writeEngineFile(t, filepath.Join(bin, "gh"), holdGHScript)
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_HOLD_BREACH", breach)
	t.Setenv("WB_HOLD_REMOTE", fixture.repository.CloneURL)

	options := fixture.options()
	options.Merge = true
	options.Hold = []string{"other-owner/*"}
	options.Timeout = 30 * time.Second
	options.CheckPollInterval = time.Millisecond

	results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
	if err != nil {
		t.Fatalf("unheld run = %v (results %+v)", err, results)
	}
	if results[0].Held {
		t.Fatalf("a repository no hold pattern matches must not be held: %+v", results[0])
	}
	if _, statErr := os.Stat(breach); statErr != nil {
		t.Fatalf("gh pr merge was never invoked for an unheld repository: %+v", results[0])
	}
}
