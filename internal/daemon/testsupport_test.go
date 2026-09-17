package daemon

import (
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// newTestService constructs the service under test with the projects root
// pinned to the fixture's own root.
//
// The service derives its operation store from WB's one home resolver, whose
// default root is the developer's real projects directory; a test that does not
// pin it therefore reads and writes durable operation records in the real
// ~/projects/.wb, where it can recover, fence, or launch work belonging to a
// daemon that is really running on this machine. Pinning keeps every durable
// record inside the temporary root the test owns and removes.
func newTestService(t *testing.T, projectsRoot, build, generation string, authorizeRaw func() error) (*Service, error) {
	t.Helper()
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	t.Setenv(wbhome.EnvMigrationCompat, "")
	operationsDirectory, err := OperationsDir(projectsRoot)
	if err != nil {
		return nil, err
	}
	return NewService(projectsRoot, operationsDirectory, build, generation, authorizeRaw)
}
