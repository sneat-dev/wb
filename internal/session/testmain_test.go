package session

import (
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// TestMain isolates the whole test binary from an inherited WB_AGENT_* export
// before any test runs: this package's autoregister tests assert who owns a
// session/Work Log claim, and an ambient WB_AGENT_ID/PID/RUNTIME/MODEL from
// the operating agent otherwise overwrites what the test set up. See
// internal/testenv and internal/envguard.
func TestMain(m *testing.M) {
	testenv.IsolateProcess()
	os.Exit(m.Run())
}
