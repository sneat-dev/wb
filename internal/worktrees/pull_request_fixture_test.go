package worktrees

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// installPullRequestResponses answers read-only paginated gh API requests.
// When matchHead is set, other commit lookups return an empty PR list.
func installPullRequestResponses(t *testing.T, payload, matchHead string) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	content := `#!/bin/sh
set -eu
if [ "$1 $2" != "api --paginate" ]; then
    echo "unexpected gh command: $*" >&2
    exit 2
fi
if [ -n "$WB_TEST_PRS_MATCH_HEAD" ]; then
    case "$3" in
        *"$WB_TEST_PRS_MATCH_HEAD"*) ;;
        *) printf '[]\n'; exit 0;;
    esac
fi
printf '%s\n' "$WB_TEST_PRS_PAYLOAD"
`
	if err := testenv.WriteExecutableFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_PRS_PAYLOAD", payload)
	t.Setenv("WB_TEST_PRS_MATCH_HEAD", matchHead)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
