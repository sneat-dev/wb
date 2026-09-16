package daemon

import (
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// newTestService constructs the service under test with WB_HOME pinned inside
// the fixture's own projects root.
//
// The service derives its operation store from WB's one home resolver, whose
// default is the developer's real WB home. A test that does not pin WB_HOME
// therefore reads and writes durable operation records in ~/.wb, where it can
// recover, fence, or launch work belonging to a daemon that is really running
// on this machine. Pinning keeps every durable record inside the temporary
// root the test owns and removes.
func newTestService(t *testing.T, projectsRoot, build, generation string, authorizeRaw func() error) (*Service, error) {
	t.Helper()
	t.Setenv(wbhome.EnvOverride, filepath.Join(projectsRoot, ".wb"))
	t.Setenv(wbhome.EnvMigrationCompat, "")
	operationsDirectory, err := OperationsDir(projectsRoot)
	if err != nil {
		return nil, err
	}
	return NewService(projectsRoot, operationsDirectory, build, generation, authorizeRaw)
}
