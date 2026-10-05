package orchestrate

import (
	"github.com/sneat-dev/wb/internal/worktrees"
	"strings"
	"testing"
)

func sealOwnerAdvanceSource(t *testing.T, f engineFixture, r WorktreeMergeReceipt, source worktrees.CreateResult) string {
	t.Helper()
	runEngineGit(t, f.canonical, "merge", "--squash", r.Sources[0].SHA)
	runEngineGit(t, f.canonical, "commit", "-m", "test: target has source tree without source ancestry")
	runEngineGit(t, f.canonical, "push", "origin", "main")
	runEngineGit(t, source.WorktreeDir, "commit", "--allow-empty", "-m", "test: preserved source descendant")
	head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	targetTree := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD^{tree}"))
	if got := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD^{tree}")); got != targetTree {
		t.Fatalf("native source/target trees differ %s %s", got, targetTree)
	}
	if contains, err := isMergeAncestor(t.Context(), source.WorktreeDir, r.Sources[0].SHA, head); err != nil || !contains {
		t.Fatalf("native source descendant %t %v", contains, err)
	}
	return head
}

func fmtSealOrdinal(n int) string {
	switch n {
	case 1:
		return "1"
	case 2:
		return "2"
	default:
		return "other"
	}
}
