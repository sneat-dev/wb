//go:build !windows

package main

import (
	"testing"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/operationreceipt"
)

type daemonOperationResult = operationreceipt.Receipt

// daemonTestService builds the daemon queue the way `wb daemon serve` does:
// the store location is resolved from the home this test pinned rather than by
// the constructor, which no longer reads the environment at all.
func daemonTestService(t *testing.T, root, build, generation string, authorizeRaw func() error) (*daemon.Service, error) {
	t.Helper()
	operationsDirectory, err := daemon.OperationsDir(root)
	if err != nil {
		return nil, err
	}
	return daemon.NewService(root, operationsDirectory, build, generation, authorizeRaw)
}
