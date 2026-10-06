//go:build e2e

package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// landFinalInstallNativeDirectCIContractGH preserves the existing native
// provider's actual Git ref/ancestry observations. Only external PR/workflow
// metadata is scripted, with its head read from the owned bare repository.
func landFinalInstallNativeDirectCIContractGH(t *testing.T, remote string) {
	t.Helper()
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_REMOTE", remote)
	t.Setenv("WB_TEST_DIRECT_CI_DRIFT", "0")
	script := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	metadata := `case "$*" in
 'api repos/acme/app/pulls/17 --include'|'api repos/acme/app/pulls/17')
  head="$(git --git-dir="$WB_TEST_REMOTE" rev-parse refs/heads/main)"
  state=open
  if [ "$WB_TEST_DIRECT_CI_DRIFT" = 1 ]; then state=closed; fi
  printf '{"number":17,"state":"%s","merged":false,"head":{"ref":"main","sha":"%s","repo":{"full_name":"acme/app"}},"base":{"ref":"release","repo":{"full_name":"acme/app"}}}\n' "$state" "$head"; exit 0 ;;
 'api repos/acme/app/branches/release --include'|'api repos/acme/app/branches/release')
  printf '%s\n' '{"protected":true,"protection":{"required_status_checks":{"contexts":["Required checks passed"]}}}'; exit 0 ;;
 'api repos/acme/app/branches/release/protection/required_status_checks --include'|'api repos/acme/app/branches/release/protection/required_status_checks')
  printf '%s\n' '{"strict":true,"contexts":["Required checks passed"],"checks":[]}'; exit 0 ;;
 'api repos/acme/app/rules/branches/release?per_page=100 --include'|'api repos/acme/app/rules/branches/release?per_page=100')
  printf '%s\n' '[]'; exit 0 ;;
 'api repos/acme/app/actions/workflows/go-ci.yml --include'|'api repos/acme/app/actions/workflows/go-ci.yml')
  printf '%s\n' '{"id":300,"name":"Go CI","path":".github/workflows/go-ci.yml","state":"active"}'; exit 0 ;;
 'api repos/acme/app/actions/runs?head_sha='*'&per_page=100 --include'|'api repos/acme/app/actions/runs?head_sha='*'&per_page=100')
  head="$(git --git-dir="$WB_TEST_REMOTE" rev-parse refs/heads/main)"
  printf '{"total_count":1,"workflow_runs":[{"id":15,"workflow_id":300,"head_sha":"%s","head_branch":"main","event":"pull_request","status":"completed","conclusion":"success","created_at":"2026-09-27T09:00:00Z","check_suite_id":305,"pull_requests":[{"number":17,"base":{"ref":"release"}}]}]}\n' "$head"; exit 0 ;;
esac
`
	updated := strings.Replace(string(body), "#!/bin/sh\n", "#!/bin/sh\nset -eu\n"+metadata, 1)
	if updated == string(body) {
		t.Fatal("private provider insertion point missing")
	}
	if err := testenv.WriteExecutableFile(script, []byte(updated), 0755); err != nil {
		t.Fatal(err)
	}
}
