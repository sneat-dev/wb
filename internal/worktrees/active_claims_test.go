package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// TestListActiveClaimSummariesIncludesLegacyHomeClaims proves the read set
// reviewer BLOCKING #1 named: a claim recorded under the retired default
// state directory ($HOME/.wb) must still be visible when a caller resolves
// active claims for a projects root that isn't that legacy home's root, so a
// migration refusal check (or any other safety gate) does not go blind to a
// live task simply because it predates the projects-root layout.
func TestListActiveClaimSummariesIncludesLegacyHomeClaims(t *testing.T) {
	userHome, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", userHome)

	projectsRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, projectsRoot)

	// Establish the legacy home: $HOME/.wb, only recognised once it carries a
	// worktrees root (legacyUserLayout's gate).
	legacyHome := filepath.Join(userHome, ".wb")
	legacyWorktree, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, legacyWorktree, "init")
	if err := os.MkdirAll(filepath.Join(legacyHome, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := recordWorkLogWithHooks(legacyHome, "legacy-claim", CreateResult{
		Repository: "acme/legacy", WorktreeDir: legacyWorktree, Branch: "legacy-claim", Base: "main", BaseSHA: strings.Repeat("a", 40),
	}, WorkLogOptions{
		EffortID: "legacy-claim", RunID: "run", AgentID: "codex-1", Model: "unknown",
		TaskSummary: "Predates the projects-root layout", WBSessionID: "wbs-legacy",
	}, workLogPublicationHooks{}); err != nil {
		t.Fatal(err)
	}

	listed, err := ListActiveClaimSummaries(projectsRoot, "acme/legacy")
	if err != nil {
		t.Fatalf("ListActiveClaimSummaries: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected the legacy-home claim to be visible, got %#v", listed)
	}
	if got := listed[0]; got.Task != "legacy-claim" || got.Repository != "acme/legacy" {
		t.Fatalf("legacy claim summary = %#v", got)
	}

	// A claim recorded in the write home for the same projects root must also
	// still be visible alongside the legacy one.
	writeHome := filepath.Join(projectsRoot, ".wb")
	writeWorktree, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, writeWorktree, "init")
	if _, err := recordWorkLogWithHooks(writeHome, "current-claim", CreateResult{
		Repository: "acme/current", WorktreeDir: writeWorktree, Branch: "current-claim", Base: "main", BaseSHA: strings.Repeat("b", 40),
	}, WorkLogOptions{
		EffortID: "current-claim", RunID: "run", AgentID: "codex-1", Model: "unknown",
		TaskSummary: "Uses the projects-root layout", WBSessionID: "wbs-current",
	}, workLogPublicationHooks{}); err != nil {
		t.Fatal(err)
	}

	both, err := ListActiveClaimSummaries(projectsRoot, "")
	if err != nil {
		t.Fatalf("ListActiveClaimSummaries: %v", err)
	}
	if len(both) != 2 {
		t.Fatalf("expected claims from both homes, got %#v", both)
	}
}
