package fleetinspect

import (
	"fmt"
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/hostload"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		if helper, known := worktrees.SecureGitHelperForArgument(os.Args[1]); known {
			os.Exit(helper(os.Args[2:]))
		}
	}
	testenv.IsolateHarnessProcess()
	testenv.IsolateProcess()
	cleanup, err := testenv.IsolateUserState()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	testenv.GitAutoMaintenanceOffProcess()
	if err := os.Setenv(hostload.EnvLoadFloor, "0"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		cleanup()
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
