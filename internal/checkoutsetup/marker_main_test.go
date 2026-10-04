package checkoutsetup

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/testenv"
	"os"
	"testing"
)

// Native tests inherit the isolated user, harness identity, process state and
// Git maintenance policy formerly established by the executable test binary.
func TestMain(m *testing.M) {
	testenv.IsolateHarnessProcess()
	testenv.IsolateProcess()
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
