package remotepublish

import (
	"fmt"
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

func TestMain(m *testing.M) {
	testenv.IsolateHarnessProcess()
	testenv.IsolateProcess()
	remove, err := testenv.IsolateUserState()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	testenv.GitAutoMaintenanceOffProcess()
	code := m.Run()
	remove()
	os.Exit(code)
}
