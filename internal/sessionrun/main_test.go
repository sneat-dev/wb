package sessionrun

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/testenv"
	"os"
	"testing"
)

// Operation tests retain the isolated user and process state formerly
// provided by cmd/wb's TestMain, including configured shared-root discovery.
func TestMain(m *testing.M) {
	testenv.IsolateProcess()
	testenv.IsolateHarnessProcess()
	testenv.GitAutoMaintenanceOffProcess()
	remove, err := testenv.IsolateUserState()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	remove()
	os.Exit(code)
}
