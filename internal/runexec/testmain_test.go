package runexec

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/hostload"
	"github.com/sneat-dev/wb/internal/testenv"
	"os"
	"testing"
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
	if err := os.Setenv(hostload.EnvLoadFloor, "0"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		remove()
		os.Exit(1)
	}
	code := m.Run()
	remove()
	os.Exit(code)
}
