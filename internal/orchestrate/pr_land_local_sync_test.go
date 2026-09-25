package orchestrate

import "context"

// addLandWorktree (used only by the e2e-tier tests below) moved to
// pr_land_local_sync_e2e_test.go alongside its only callers -- left here it
// has no caller under the default build tags at all, which golangci-lint's
// unused check (rightly) flags.

// TestLandFastForwardsACleanWorktreeAfterUpdateBranch,
// TestLandLeavesADirtyWorktreeUntouched, TestLandLeavesADivergedWorktreeUntouched,
// TestLandDoesNotErrorWithNoWorktreeForTheBranch and
// TestFastForwardWorktreeToUpdatedHeadNotesAMismatchedFetch moved to
// pr_land_local_sync_e2e_test.go (spec/plans/coverage-to-100 task-17):
// fastForwardWorktreeToUpdatedHead now runs part of its decision through
// orchestrateGit (internal/runner), which task-24's runtime guard blocks
// outside the e2e tier.

// runGitAllowFail is a thin runEngineGit variant that returns an error
// instead of failing the test, for assertions that want to check
// success/failure themselves (e.g. that a ref resolves at all).
func runGitAllowFail(directory string, args ...string) (string, error) {
	out, _, err := runCommand(context.Background(), 0, 0, directory, "git", args...)
	return out, err
}
