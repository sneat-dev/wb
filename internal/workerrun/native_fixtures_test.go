package workerrun

import (
	"testing"

	"github.com/sneat-dev/wb/internal/daemon"
)

// Native queue fixtures use the production constructor and resolved private root.
func workerTestService(t *testing.T, root, build, generation string, raw func() error) (*daemon.Service, error) {
	t.Helper()
	directory, err := daemon.OperationsDir(root)
	if err != nil {
		return nil, err
	}
	return daemon.NewService(root, directory, build, generation, raw)
}
