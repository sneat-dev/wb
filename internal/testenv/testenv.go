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

// Isolate truly unsets every WB_AGENT_* variable -- the key itself is
// removed from the environment, not merely emptied -- and sets GOWORK=off
// for the current test's environment. It restores every value it changed,
// via t.Cleanup, when t (or the subtest it was called from) completes.
//
// t.Setenv("WB_AGENT_X", "") is not enough: it leaves the key present with
// an empty value, and envguard.Inspect (like most agent-var detection)
// keys off presence in os.Environ(), not value, so an emptied variable
// still reads as set. Isolate calls os.Unsetenv directly instead.
//
// A test that intentionally exercises agent-mode behavior calls Isolate
// first and then sets its own WB_AGENT_* value with t.Setenv, so the
// intentional value applies last and is the one the code under test
// observes.
//
// Like t.Setenv, Isolate makes the calling test (and its subtests) unsafe
// to run in parallel with sibling tests that also touch the process
// environment: it mutates shared process state and only restores it on
// this test's own Cleanup.
func Isolate(t testing.TB) {
	t.Helper()
	// Route through t.Setenv first so the standard library marks this test
	// (and any test that already called t.Parallel) as ineligible for
	// parallel execution, exactly as if Isolate had used t.Setenv
	// throughout -- before this function makes any direct os.Unsetenv call
	// of its own.
	t.Setenv("GOWORK", "off")
	for _, entry := range os.Environ() {
		name, value, found := strings.Cut(entry, "=")
		if !found || !envguard.IsAgentVar(name) {
			continue
		}
		original := value
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("testenv.Isolate: unsetenv %s: %v", name, err)
		}
		t.Cleanup(func() {
			if err := os.Setenv(name, original); err != nil {
				t.Errorf("testenv.Isolate: restore %s: %v", name, err)
			}
		})
	}
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
