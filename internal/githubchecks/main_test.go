package githubchecks

import (
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

func TestMain(m *testing.M) {
	testenv.IsolateHarnessProcess()
	testenv.IsolateProcess()
	removeUserState, err := testenv.IsolateUserState()
	if err != nil {
		_, _ = os.Stderr.WriteString("isolate githubchecks test user: " + err.Error() + "\n")
		os.Exit(1)
	}
	code := m.Run()
	removeUserState()
	os.Exit(code)
}
