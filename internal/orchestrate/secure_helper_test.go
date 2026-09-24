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
		switch os.Args[1] {
		case worktrees.SecureCleanupGitHelperArgument:
			os.Exit(worktrees.RunSecureCleanupGitHelper(os.Args[2:]))
		case worktrees.SecureStageGitHelperArgument:
			os.Exit(worktrees.RunSecureStageGitHelper(os.Args[2:]))
		case worktrees.SecureCanonicalGitHelperArgument:
			os.Exit(worktrees.RunSecureCanonicalGitHelper(os.Args[2:]))
		case worktrees.SecureStageCanonicalGitHelperArgument:
			os.Exit(worktrees.RunSecureStageCanonicalGitHelper(os.Args[2:]))
		case worktrees.SecureRenameGitHelperArgument:
			os.Exit(worktrees.RunSecureRenameGitHelper(os.Args[2:]))
		}
	}
	// See internal/testenv: strip inherited WB_AGENT_* and pin GOWORK=off
	// before any orchestrate test (including merge recovery) runs.
	testenv.IsolateProcess()
	// Disable git's detached gc/maintenance for every git this binary
	// starts, including the engine's own fixture clones and pushes, so no
	// background writer can race t.TempDir() cleanup (task-21). Bare
	// remotes pushed to over a local transport are also configured
	// directly with testenv.ConfigureGitAutoMaintenanceOff.
	testenv.GitAutoMaintenanceOffProcess()
	os.Exit(m.Run())
}
