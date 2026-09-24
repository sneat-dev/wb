package streams

import (
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// TestMain disables git's detached gc/maintenance dispatch for every git this
// test binary starts -- the fixtures' own commands and the production git
// calls under test alike -- so no background writer can race t.TempDir()'s
// cleanup (task-21). Bare remotes pushed to over a local transport are also
// configured directly with testenv.ConfigureGitAutoMaintenanceOff, because
// git strips GIT_CONFIG_* before spawning the server-side receive-pack.
func TestMain(m *testing.M) {
	testenv.GitAutoMaintenanceOffProcess()
	os.Exit(m.Run())
}
