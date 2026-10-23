package orchestrate

import (
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Orchestrator integration tests create real managed worktrees, so their test
// binary must expose the same descriptor-anchored child modes as cmd/wb.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		if helper, known := worktrees.SecureGitHelperForArgument(os.Args[1]); known {
			os.Exit(helper(os.Args[2:]))
		}
	}
	// See internal/testenv: strip inherited WB_AGENT_* and pin GOWORK=off
	// before any orchestrate test (including merge recovery) runs.
	testenv.IsolateHarnessProcess()
	testenv.IsolateProcess()
	// Moved observer journeys must not resolve the operator's config or claim store.
	removeUserState, userStateErr := testenv.IsolateUserState()
	if userStateErr != nil {
		_, _ = os.Stderr.WriteString("isolate orchestrate test user: " + userStateErr.Error() + "\n")
		os.Exit(1)
	}
	// Disable git's detached gc/maintenance for every git this binary
	// starts, including the engine's own fixture clones and pushes, so no
	// background writer can race t.TempDir() cleanup (task-21). Bare
	// remotes pushed to over a local transport are also configured
	// directly with testenv.ConfigureGitAutoMaintenanceOff.
	testenv.GitAutoMaintenanceOffProcess()
	seedRoot, err := os.MkdirTemp("", "wb-orchestrate-git-seeds-")
	if err != nil {
		_, _ = os.Stderr.WriteString("create engine Git seed root: " + err.Error() + "\n")
		os.Exit(1)
	}
	engineGitSeeds.root = seedRoot
	code := m.Run()
	removeUserState()
	if err := os.RemoveAll(seedRoot); err != nil {
		_, _ = os.Stderr.WriteString("remove engine Git seed root: " + err.Error() + "\n")
		code = 1
	}
	os.Exit(code)
}
