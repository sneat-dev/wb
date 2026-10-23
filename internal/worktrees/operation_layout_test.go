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

func TestOperationLifecycleBacklogBindsRepositoryRunAndRoot(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	root := t.TempDir()
	for _, relative := range []string{"acme/widget", "github.com/acme/widget"} {
		result := ListResult{Task: "campaign-test.acme-widget", Repository: "acme/widget", CanonicalDir: filepath.Join(projectsRoot, "acme", "widget"), WorktreesRoot: root, WorktreeDir: filepath.Join(root, "campaign-test", filepath.FromSlash(relative)), Branch: "wb/campaign-test", Base: "main", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
		record := newLifecycleBacklogRecord(projectsRoot, result, "removed")
		if err := validateLifecycleBacklog(record); err != nil {
			t.Fatalf("operation backlog rejected: %v", err)
		}
		for _, mode := range []string{"foreign run", "foreign repository", "foreign root", "unbound effort", "relative path"} {
			bad := result
			switch mode {
			case "foreign run":
				bad.WorktreeDir = filepath.Join(root, "other", filepath.FromSlash(relative))
			case "foreign repository":
				bad.Repository = "acme/other"
				bad.CanonicalDir = filepath.Join(projectsRoot, "acme", "other")
			case "foreign root":
				bad.WorktreesRoot = filepath.Join(root, "other")
			case "unbound effort":
				bad.Task = "campaign-test.unrelated"
			case "relative path":
				bad.WorktreeDir = filepath.FromSlash("campaign-test/" + relative)
			}
			if err := validateLifecycleBacklog(newLifecycleBacklogRecord(projectsRoot, bad, "removed")); err == nil {
				t.Fatalf("accepted %s operation backlog", mode)
			}
		}
	}
}
