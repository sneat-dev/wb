package remoterun

import (
	"fmt"
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

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
