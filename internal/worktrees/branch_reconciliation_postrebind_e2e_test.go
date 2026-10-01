//go:build e2e

package worktrees

import (
	"context"
	"strings"
	"testing"
)

//nolint:paralleltest // the native Git fixture configures process-wide Git and WB environment.
func TestE2EBranchReconciliationRejectsLostPostRebindAuthority(t *testing.T) {
	fixture, created, liveBranch, head, _, _ := prepareBranchReconciliationFixture(t)
	options := reconcileOptions(fixture, created, liveBranch, head)
	options.Apply = true
	ctx := context.Background()
	stop := options
	stop.testStopAfterStage = "rebound"
	if _, err := LogRecover(ctx, stop); err == nil || !strings.Contains(err.Error(), "injected interruption") {
		t.Fatalf("save rebound stage = %v", err)
	}
	assertRefusal := func(name string) {
		t.Helper()
		before, err := readLocalEvents(created.WorktreeDir)
		if err != nil {
			t.Fatal(err)
		}
		result, err := LogRecover(ctx, options)
		if err == nil || result.ReadyForNormalCleanup || result.Applied {
			t.Fatalf("%s accepted lost branch authority: result=%+v err=%v", name, result, err)
		}
		after, readErr := readLocalEvents(created.WorktreeDir)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(after) != len(before) {
			t.Fatalf("%s appended an event: before=%d after=%d", name, len(before), len(after))
		}
	}
	// A saved rebound cannot be resumed by checking out the prior live name.
	gitTest(t, created.WorktreeDir, "branch", "-m", liveBranch)
	assertRefusal("checkout reverted to live branch")
	gitTest(t, created.WorktreeDir, "branch", "-m", created.Branch)

	// The claim checkout alone is insufficient if the old live ref returns.
	gitTest(t, fixture.canonical, "branch", liveBranch, head)
	assertRefusal("live ref recreated after rebound")
	gitTest(t, fixture.canonical, "branch", "-D", liveBranch)

	claimHead := gitTestOutput(t, fixture.canonical, "rev-parse", "refs/heads/"+created.Branch)
	gitTest(t, fixture.canonical, "update-ref", "refs/heads/"+created.Branch, "origin/main")
	assertRefusal("claim ref moved after rebound")
	gitTest(t, fixture.canonical, "update-ref", "refs/heads/"+created.Branch, claimHead)

	gitTest(t, created.WorktreeDir, "commit", "--allow-empty", "-m", "unexpected head")
	assertRefusal("checkout head changed after rebound")
	gitTest(t, created.WorktreeDir, "reset", "--hard", head)

	stop = options
	stop.testStopAfterStage = "event"
	if _, err := LogRecover(ctx, stop); err == nil || !strings.Contains(err.Error(), "injected interruption") {
		t.Fatalf("save event stage = %v", err)
	}
	gitTest(t, fixture.canonical, "branch", liveBranch, head)
	assertRefusal("live ref recreated after event append")
	gitTest(t, fixture.canonical, "branch", "-D", liveBranch)
	if result, err := LogRecover(ctx, options); err != nil || !result.ReadyForNormalCleanup {
		t.Fatalf("complete with intact authority = %+v, %v", result, err)
	}
	gitTest(t, fixture.canonical, "branch", liveBranch, head)
	assertRefusal("live ref recreated after complete")
}
