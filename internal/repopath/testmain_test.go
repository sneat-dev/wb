package repopath

import (
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// TestMain disables git's detached gc/maintenance dispatch for every git this
// test binary starts -- the fixtures' own commands and the production git
// calls under test alike -- so no background writer can race t.TempDir()'s
// cleanup (task-21). This package's fixtures never push to a bare remote, so
// no ConfigureGitAutoMaintenanceOff call is needed alongside this one.
func TestMain(m *testing.M) {
	testenv.GitAutoMaintenanceOffProcess()
	os.Exit(m.Run())
}
