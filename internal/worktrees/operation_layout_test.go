package worktrees

import (
	"path/filepath"
	"testing"
)

func TestOperationClaimLayoutBindsRunRepositoryAndExactCheckout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, relative := range []string{"acme/widget", "github.com/acme/widget"} {
		path := filepath.Join(root, "campaign-test", filepath.FromSlash(relative))
		claim := workLogClaim{Task: "campaign-test.acme-widget", EffortID: "campaign-test.acme-widget", RunID: "campaign-test", Repository: "acme/widget", Worktree: path}
		layout, err := claimedSharedWorktreeLayout(path, claim)
		if err != nil || layout.WorktreesRoot != root {
			t.Fatalf("operation layout not recognized: %+v %v", layout, err)
		}
		for _, mode := range []string{"foreign run", "foreign effort", "foreign repository", "foreign path", "arbitrary suffix", "unsafe run"} {
			bad := claim
			switch mode {
			case "foreign run":
				bad.RunID = "other"
			case "foreign effort":
				bad.EffortID = "other.acme-widget"
			case "foreign repository":
				bad.Repository = "acme/other"
			case "foreign path":
				bad.Worktree = filepath.Join(root, "elsewhere")
			case "arbitrary suffix":
				bad.Task = "campaign-test.unrelated"
				bad.EffortID = bad.Task
			case "unsafe run":
				bad.RunID = "../campaign-test"
			}
			if _, err := claimedSharedWorktreeLayout(path, bad); err == nil {
				t.Fatalf("accepted %s claim for %s", mode, relative)
			}
		}
	}
}
