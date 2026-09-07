package quality

import (
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// TestMain isolates the whole test binary from ambient machine state before
// any test runs: an inherited WB_AGENT_* export from the operating agent, and
// a go.work above TMPDIR that would otherwise flip a sharded/coverage test's
// temp Go module fixtures into workspace mode. See internal/testenv and
// internal/envguard -- the same isolation this package's own subprocess
// environment (commandEnv in verify.go) now applies to every check it runs.
func TestMain(m *testing.M) {
	testenv.IsolateProcess()
	os.Exit(m.Run())
}
