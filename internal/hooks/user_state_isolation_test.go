package hooks

import (
	"fmt"
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// runWithPrivateUser runs the package's tests as a private, empty user. This
// package reads wb.yaml from the user's configuration home by default, and a
// test must never read, or act on, the developer's own.
func runWithPrivateUser(m *testing.M) int {
	remove, err := testenv.IsolateUserState()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: could not isolate the test user's configuration and state: %v\n", err)
		return 1
	}
	defer remove()
	return m.Run()
}

func TestThisTestBinaryCannotReachARealUsersConfiguration(t *testing.T) {
	t.Parallel()
	if violations := testenv.UserStateViolations(wbconfig.DefaultPath()); len(violations) != 0 {
		t.Fatalf("this test binary can reach real user state: %v", violations)
	}
}
