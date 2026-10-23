//go:build e2e

package main

import (
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// The built CLI, rather than only a test binary's TestMain, must own every
// worktrees child protocol after the package import side effect is removed.
func TestE2EBuiltWBDispatchesEverySecureGitHelper(t *testing.T) {
	for _, tc := range []struct {
		name, argument, diagnostic string
	}{
		{"cleanup", worktrees.SecureCleanupGitHelperArgument, "wb secure cleanup helper: missing worktree path or Git command"},
		{"stage", worktrees.SecureStageGitHelperArgument, "wb secure stage helper: enter inherited stage directory"},
		{"canonical", worktrees.SecureCanonicalGitHelperArgument, "wb secure canonical helper: missing Git executable"},
		{"canonical policy", worktrees.SecureCanonicalPolicyGitHelperArgument, "wb secure canonical policy helper: invalid read-only query"},
		{"staged canonical", worktrees.SecureStageCanonicalGitHelperArgument, "wb secure staged canonical helper: invalid arguments"},
		{"rename", worktrees.SecureRenameGitHelperArgument, "wb secure rename helper: invalid arguments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := runWB(t, tc.argument)
			if result.exitCode != 1 || !strings.Contains(result.stderr, tc.diagnostic) || result.stdout != "" {
				t.Fatalf("wb %s: exit=%d stdout=%q stderr=%q; want helper refusal %q", tc.argument, result.exitCode, result.stdout, result.stderr, tc.diagnostic)
			}
		})
	}
}
