package daemon

import (
	"path/filepath"
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

// LegacyStatePath is the lifecycle record a daemon wrote before the runtime
// directory followed WB's home. It exists for detection only; nothing here
// reads it as this build's own state.
func LegacyStatePath(projectsRoot string) string {
	legacy := LegacyRuntimeDir(projectsRoot)
	if legacy == "" {
		return ""
	}
	return filepath.Join(legacy, StateFileName)
}
