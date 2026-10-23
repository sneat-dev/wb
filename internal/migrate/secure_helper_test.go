package migrate

import (
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		if helper, known := worktrees.SecureGitHelperForArgument(os.Args[1]); known {
			os.Exit(helper(os.Args[2:]))
		}
	}
	// See internal/testenv: strip inherited WB_AGENT_* and pin GOWORK=off
	// before any migrate/campaign test runs.
	testenv.IsolateProcess()
	// Disable git's detached gc/maintenance for every git this binary
	// starts, including this package's own fixture clones and pushes, so
	// no background writer can race t.TempDir() cleanup (task-21). Bare
	// remotes pushed to over a local transport are also configured
	// directly with testenv.ConfigureGitAutoMaintenanceOff.
	testenv.GitAutoMaintenanceOffProcess()
	os.Exit(m.Run())
}
