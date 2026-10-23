//go:build e2e

package orchestrate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver/testfixture"

	"github.com/sneat-dev/wb/internal/testenv"
)

func installWorktreeMergeMergedPRGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := `#!/bin/sh
set -eu
case "$*" in
  'pr view https://example.test/acme/app/pull/17 --repo acme/app --json state,mergedAt,mergeCommit,headRefOid,baseRefName')
    printf '{"state":"MERGED","mergedAt":"2026-08-27T00:00:00Z","headRefOid":"%s","baseRefName":"main","mergeCommit":{"oid":"%s"}}\n' "$WB_TEST_CANDIDATE_SHA" "$WB_TEST_TARGET_SHA" ;;
  'api repos/acme/app/branches/main --include'|'api repos/acme/app/branches/main') printf '%s\n' '{"protected":false,"protection":{}}' ;;
  'api repos/acme/app/rules/branches/main?per_page=100 --include'|'api repos/acme/app/rules/branches/main?per_page=100') printf '%s\n' '[]' ;;
  'api repos/acme/app/git/ref/heads/main --include'|'api repos/acme/app/git/ref/heads/main') printf '{"object":{"sha":"%s"}}\n' "$WB_TEST_TARGET_SHA" ;;
  'api repos/acme/app/pulls/'*' --include'|'api repos/acme/app/pulls/'*)
    printf '{"number":41,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"%s","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}\n' "$WB_TEST_CANDIDATE_SHA" ;;
  *'/check-runs?per_page=100 --include'|*'/check-runs?per_page=100') printf '%s\n' '{"total_count":0,"check_runs":[]}' ;;
  *'/status?per_page=100 --include'|*'/status?per_page=100') printf '%s\n' '{"total_count":0,"statuses":[]}' ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := testenv.WriteExecutableFile(script, []byte(testfixture.WithEmptyActionsRuns(body)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installWorktreeMergePublishedRepairGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := `#!/bin/sh
set -eu
case "$*" in
  'pr view https://example.test/acme/app/pull/29 --repo acme/app --json state,mergedAt,mergeCommit,headRefOid,baseRefName')
    printf '{"state":"OPEN","mergedAt":"","headRefOid":"%s","baseRefName":"main","mergeCommit":{"oid":""}}\n' "$WB_TEST_PUBLISHED_SHA" ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := testenv.WriteExecutableFile(script, []byte(testfixture.WithEmptyActionsRuns(body)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
