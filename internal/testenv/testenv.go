// Package testenv isolates a test process from the ambient machine state
// documented in internal/envguard: WB_AGENT_* variables inherited from
// whichever agent is operating the shell that launched `go test`, and an
// ambient GOWORK the test process would otherwise carry into every `go`
// invocation it makes.
//
// WB's own test suite runs as a subprocess of that operating agent. A test
// that asserts "no active owner" or "no live registered session" observes
// the outer agent's identity instead of a clean slate unless it calls
// Isolate first: WB_AGENT_PID/WB_AGENT_RUNTIME/WB_AGENT_MODEL/WB_AGENT_ID are
// inherited by the `go test` binary itself, not only by subprocesses wb
// spawns, so internal/envguard's subprocess-environment sanitizing cannot
// reach them.
package testenv

import (
	"os"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/envguard"
)

// Isolate unsets every WB_AGENT_* variable and sets GOWORK=off for the
// current test's environment, via t.Setenv so both are restored
// automatically when t (or the subtest it was called from) completes.
//
// A test that intentionally exercises agent-mode behavior calls Isolate
// first and then sets its own WB_AGENT_* value with t.Setenv, so the
// intentional value applies last and is the one the code under test
// observes.
func Isolate(t testing.TB) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found && envguard.IsAgentVar(name) {
			t.Setenv(name, "")
		}
	}
	t.Setenv("GOWORK", "off")
}

// IsolateProcess performs the same isolation as Isolate for a caller with no
// *testing.T -- typically a package's TestMain, which isolates its whole
// test binary process before any test runs. Unlike Isolate this is not
// restored: TestMain's process exits once m.Run() returns, so there is
// nothing to restore it for.
func IsolateProcess() {
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found && envguard.IsAgentVar(name) {
			_ = os.Unsetenv(name)
		}
	}
	_ = os.Setenv("GOWORK", "off")
}
