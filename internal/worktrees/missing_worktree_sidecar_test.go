package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

// A claim landed through a pull request keeps a WB-written sidecar. Looking up
// the owner of a missing registered worktree passes over a validated sidecar
// of another claim, but still refuses orphaned, mismatched or linked ones.
func TestMissingWorktreeLookupValidatesPullRequestSidecars(t *testing.T) {
	t.Parallel()
	other, mine := strings.Repeat("a", 64), strings.Repeat("b", 64)
	sidecar := other + worktreeclaims.PullRequestBindingSuffix
	setup := func(t *testing.T) (home, worktree, claims string) {
		home = t.TempDir()
		worktree = filepath.Join(t.TempDir(), "missing-checkout")
		claims = filepath.Join(home, "worklogs", "task-one", "runs", "run-one", "claims")
		wtLifeCovWriteJSON(t, filepath.Join(claims, other+".json"), workLogClaim{EffortID: "task-one", RunID: "run-one", ClaimID: other,
			Task: "task-one", Repository: "acme/app", Worktree: filepath.Join(t.TempDir(), "other"), Lifecycle: "active"})
		wtLifeCovWriteJSON(t, filepath.Join(claims, mine+".json"), workLogClaim{EffortID: "task-one", RunID: "run-one", ClaimID: mine,
			Task: "task-one", Repository: "acme/provider", Worktree: worktree, Lifecycle: "active"})
		return home, worktree, claims
	}
	binding := func(repository string) worktreeclaims.ClaimPullRequestBinding {
		return worktreeclaims.ClaimPullRequestBinding{Repository: repository, PullRequest: 42,
			URL: "https://github.com/" + repository + "/pull/42", RecordedAt: time.Unix(1, 0).UTC()}
	}

	t.Run("registered sidecar of another claim", func(t *testing.T) {
		t.Parallel()
		home, worktree, claims := setup(t)
		wtLifeCovWriteJSON(t, filepath.Join(claims, sidecar), binding("acme/app"))
		claim, err := activeWorkLogClaimAtPath(home, worktree, nil)
		if err != nil || claim == nil || claim.ClaimID != mine {
			t.Fatalf("lookup did not pass over a registered sidecar: claim=%#v, err=%v", claim, err)
		}
	})
	t.Run("orphaned sidecar", func(t *testing.T) {
		t.Parallel()
		home, worktree, claims := setup(t)
		orphan := strings.Repeat("0", 64) + worktreeclaims.PullRequestBindingSuffix
		wtLifeCovWriteJSON(t, filepath.Join(claims, orphan), binding("acme/app"))
		if _, err := activeWorkLogClaimAtPath(home, worktree, nil); err == nil || !strings.Contains(err.Error(), "unsafe Work Log claim entry") {
			t.Fatalf("orphaned sidecar accepted: %v", err)
		}
	})
	t.Run("sidecar for another repository", func(t *testing.T) {
		t.Parallel()
		home, worktree, claims := setup(t)
		wtLifeCovWriteJSON(t, filepath.Join(claims, sidecar), binding("acme/elsewhere"))
		if _, err := activeWorkLogClaimAtPath(home, worktree, nil); err == nil || !strings.Contains(err.Error(), "invalid pull request binding") {
			t.Fatalf("mismatched sidecar accepted: %v", err)
		}
	})
	t.Run("linked sidecar", func(t *testing.T) {
		t.Parallel()
		home, worktree, claims := setup(t)
		target := wtLifeCovWriteJSON(t, filepath.Join(t.TempDir(), "binding.json"), binding("acme/app"))
		if err := os.Symlink(target, filepath.Join(claims, sidecar)); err != nil {
			t.Fatal(err)
		}
		if _, err := activeWorkLogClaimAtPath(home, worktree, nil); err == nil {
			t.Fatal("lookup followed a symlinked sidecar")
		}
	})
}
