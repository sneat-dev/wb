package deps

import (
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// TestMain isolates the whole test binary from ambient machine state before
// any test runs: an inherited WB_AGENT_* export from the operating agent, and
// a go.work above TMPDIR that would otherwise flip this package's temp Go
// module fixtures (go-directive, bump, campaign) into workspace mode. See
// internal/testenv and internal/envguard.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		if helper, known := worktrees.SecureGitHelperForArgument(os.Args[1]); known {
			os.Exit(helper(os.Args[2:]))
		}
	}
	testenv.IsolateProcess()
	testenv.GitAutoMaintenanceOffProcess()
	os.Exit(m.Run())
}
