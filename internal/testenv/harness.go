package testenv

import (
	"os"
	"strings"

	"github.com/sneat-dev/wb/internal/provenance"
)

// harnessVariables are the exact names an agent harness exports into every
// process it launches, beyond the CLAUDE_CODE_ family. provenance reads the
// session and harness names, and wb's work-log claim code treats either as a
// signal to register the live harness process above the caller as the
// session that owns the claim.
var harnessVariables = []string{
	provenance.EnvHarness,
	provenance.EnvEffortLevel,
	"CLAUDECODE",
	"CLAUDE_PID",
}

// harnessVariablePrefix covers the rest of Claude Code's own exports,
// including provenance.EnvHarnessSessionID (CLAUDE_CODE_SESSION_ID).
const harnessVariablePrefix = "CLAUDE_CODE_"

// IsolateHarnessProcess removes every agent-harness variable from the test
// process, for a package's TestMain, so a test binary run from inside an agent
// session sees the same environment it sees on CI.
//
// Isolate and IsolateProcess only remove WB_AGENT_*. A harness also exports
// its own session and process variables, and those are enough for a
// work-log claim made by `worktree create` to register the real harness
// process found by walking the test binary's parents, so that session owns
// the claim instead of the session the test declared. That made
// TestWorktreeRelocateCLIJSONEnvelopeAndShortcut report "worktree has an
// active owner" and TestSessionResumeLocalActualCustodyRefusalDoesNotClaimRoute
// report that its source session does not own the active Work Log, on every
// run from inside an agent session and never on CI.
//
// It is not restored: TestMain's process exits once m.Run returns. A test
// that exercises harness behaviour sets its own variables with t.Setenv.
func IsolateHarnessProcess() {
	for _, name := range harnessVariables {
		_ = os.Unsetenv(name)
	}
	for _, entry := range os.Environ() {
		if name, _, found := strings.Cut(entry, "="); found && strings.HasPrefix(name, harnessVariablePrefix) {
			_ = os.Unsetenv(name)
		}
	}
}
