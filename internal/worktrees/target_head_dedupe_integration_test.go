package worktrees

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// countingGitShim puts a `git` on PATH that records every exact-target fetch
// before delegating to the real binary, so the walk's network cost can be
// asserted rather than inferred from the cache's own unit tests.
func countingGitShim(t *testing.T) func() int {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on PATH")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "fetches.log")
	shim := filepath.Join(dir, "git")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"--no-tags\" ]; then printf '%s\\n' \"$*\" >> " + log + "; break; fi\n" +
		"done\n" +
		"exec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		data, readErr := os.ReadFile(log)
		if readErr != nil {
			return 0
		}
		return len(strings.Split(strings.TrimSpace(string(data)), "\n"))
	}
}

// Two worktrees in one repository must cost one exact-target fetch, not two.
// On the fleet this was measured against, 262 worktrees spanned 71
// repositories, so the un-deduplicated walk spent 73% of its fetches
// re-learning a SHA it already had.
func TestListFetchesExactTargetOncePerRepository(t *testing.T) {
	fixture := newGitFixture(t)
	_, firstHead, mergedAt := prepareMergedTaskInFixture(t, fixture, "dedupe-a")
	_, secondHead, _ := prepareMergedTaskInFixture(t, fixture, "dedupe-b")
	installMergedPullRequestFixtures(t, []string{firstHead, secondHead}, mergedAt)

	fetches := countingGitShim(t)
	outcome, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot,
		GitHub:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 2 {
		t.Fatalf("expected both worktrees in the inventory, got %#v", outcome.Results)
	}
	if got := fetches(); got != 1 {
		t.Fatalf("two worktrees in one repository issued %d exact-target fetches, want 1", got)
	}
	// Both must also be judged against the same target, which is the
	// correctness half: without the memo, a push landing mid-walk gives two
	// worktrees in one repository two different notions of the base.
	if a, b := outcome.Results[0].RemoteTargetSHA, outcome.Results[1].RemoteTargetSHA; a == "" || a != b {
		t.Fatalf("worktrees judged against different targets: %q vs %q", a, b)
	}
	_ = time.Now
}
