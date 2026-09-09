package deps

import (
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// TestMain isolates the whole test binary from ambient machine state before
// any test runs: an inherited WB_AGENT_* export from the operating agent, and
// a go.work above TMPDIR that would otherwise flip this package's temp Go
// module fixtures (go-directive, bump, campaign) into workspace mode. See
// internal/testenv and internal/envguard.
func TestMain(m *testing.M) {
	testenv.IsolateProcess()
	os.Exit(m.Run())
}
