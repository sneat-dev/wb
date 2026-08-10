package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRenameApplyMovesWorktreePreservesExplicitCacheAndSwitchesBranch proves
// an explicitly allowed cache (standing in for node_modules) may survive the
// move, while Git's own
// bookkeeping (`git worktree list`) must report the new path, and the
// worktree must land on a freshly created branch that both `wb worktree
// guard` and Git itself accept.
func TestRenameApplyMovesWorktreePreservesExplicitCacheAndSwitchesBranch(t *testing.T) {
	fixture := newGitFixture(t)
	// A real project ignores node_modules; without that, plain `git status`
	// would call the worktree dirty for the exact directory this verb exists
	// to preserve, which is what every "Clean" check across Create/List/
	// Cleanup already keys off.
	if err := os.WriteFile(filepath.Join(fixture.canonical, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", ".gitignore")
	gitTest(t, fixture.canonical, "commit", "-m", "ignore node_modules")
	gitTest(t, fixture.canonical, "push", "origin", "main")

	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "old-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	oldWorktree := created[0].WorktreeDir
	untracked := filepath.Join(oldWorktree, "node_modules", "left-behind.txt")
	if err := os.MkdirAll(filepath.Dir(untracked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(untracked, []byte("expensive to rebuild\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot:       fixture.projectsRoot,
		OldTask:            "old-task",
		NewTask:            "new-task",
		PreserveCachePaths: []string{"node_modules"},
		Apply:              true,
	})
	if err != nil {
		t.Fatalf("Rename apply: %v\nresults=%#v", err, outcome.Results)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("rename outcome = %#v", outcome.Results)
	}
	result := outcome.Results[0]
	wantNewWorktree := filepath.Join(fixture.home, "worktrees", "new-task", "acme", "app")
	if result.NewWorktreeDir != wantNewWorktree {
		t.Fatalf("new worktree dir = %s, want %s", result.NewWorktreeDir, wantNewWorktree)
	}
	if result.NewBranch != "codex/new-task" {
		t.Fatalf("default new branch = %q, want codex/new-task", result.NewBranch)
	}
	if result.Repaired {
		t.Fatalf("a healthy `git worktree move` must not need repair: %#v", result)
	}

	// The untracked file — node_modules stand-in — must have made the trip.
	moved := filepath.Join(wantNewWorktree, "node_modules", "left-behind.txt")
	content, err := os.ReadFile(moved)
	if err != nil {
		t.Fatalf("untracked file did not survive the rename: %v", err)
	}
	if string(content) != "expensive to rebuild\n" {
		t.Fatalf("untracked file content = %q", content)
	}

	// The old path must be gone, and Git's own registration must know it.
	if _, err := os.Stat(oldWorktree); !os.IsNotExist(err) {
		t.Fatalf("old worktree still present: %v", err)
	}
	listing := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	if !strings.Contains(listing, "worktree "+wantNewWorktree) {
		t.Fatalf("git worktree list does not report the new path:\n%s", listing)
	}
	if strings.Contains(listing, "worktree "+oldWorktree) {
		t.Fatalf("git worktree list still reports the old path:\n%s", listing)
	}

	// The worktree must be on the new branch, and satisfy Guard.
	branch := gitTestOutput(t, wantNewWorktree, "branch", "--show-current")
	if branch != "codex/new-task" {
		t.Fatalf("checked-out branch = %q, want codex/new-task", branch)
	}
	if _, err := Guard(context.Background(), wantNewWorktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil {
		t.Fatalf("renamed worktree failed guard: %v", err)
	}

	// The old task root is retained (matching Cleanup's convention), not
	// deleted outright, and its lock was retired rather than left held.
	oldTaskRoot := filepath.Join(fixture.home, "worktrees", "old-task")
	if info, statErr := os.Stat(oldTaskRoot); statErr != nil || !info.IsDir() {
		t.Fatalf("old task root should remain in place: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(oldTaskRoot, ".lock")); !os.IsNotExist(statErr) {
		t.Fatalf(".lock must not remain held after rename: %v", statErr)
	}
	entries, err := os.ReadDir(oldTaskRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".wb-retired-lock-") {
			return // Retired, not held or deleted — matches Cleanup's bookkeeping.
		}
	}
	t.Fatalf("old task root has no retired lock sentinel: %#v", entries)
}

// TestRenameFallsBackToMoveAndRepairWhenGitWorktreeMoveRefuses is the
// regression test for the founder's own production incident: a bare
// filesystem move leaves Git's gitdir pointer stale, and it must never be the
// first choice. This test forces `git worktree move` itself to refuse — by
// taking a real Git-level worktree lock, independent of WB's own task
// `.lock` — so the fallback path (plain move + `git worktree repair`) is the
// one actually exercised and verified, not merely present as dead code.
func TestRenameFallsBackToMoveAndRepairWhenGitWorktreeMoveRefuses(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "locked-move-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "lock", created[0].WorktreeDir, "--reason", "simulate git worktree move refusing")
	if output, moveErr := gitTestRun(fixture.canonical, "worktree", "move", created[0].WorktreeDir, filepath.Join(fixture.home, "elsewhere")); moveErr == nil {
		t.Fatalf("test setup assumption broken: a locked worktree must refuse `git worktree move`, output=%s", output)
	}

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "locked-move-task",
		NewTask:      "locked-move-task-renamed",
		Apply:        true,
	})
	if err != nil {
		t.Fatalf("Rename apply: %v\nresults=%#v", err, outcome.Results)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied || !outcome.Results[0].Repaired {
		t.Fatalf("fallback rename outcome = %#v", outcome.Results)
	}
	newWorktree := outcome.Results[0].NewWorktreeDir
	listing := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	if !strings.Contains(listing, "worktree "+newWorktree) {
		t.Fatalf("git worktree list does not report the repaired path:\n%s", listing)
	}
	if _, guardErr := Guard(context.Background(), newWorktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); guardErr != nil {
		t.Fatalf("repaired worktree failed guard: %v", guardErr)
	}
}

// TestRenamePlanDoesNotMutateAnything proves the dry-run default is truly
// read-only: no directory moves, no branch changes, no report file.
func TestRenamePlanDoesNotMutateAnything(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "plan-only",
	})
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "plan-only",
		NewTask:      "plan-only-renamed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Eligible || outcome.Results[0].Applied {
		t.Fatalf("plan outcome = %#v", outcome.Results)
	}
	if outcome.ReportPath != "" {
		t.Fatalf("a dry-run plan must not write a report: %q", outcome.ReportPath)
	}
	if _, err := os.Stat(created[0].WorktreeDir); err != nil {
		t.Fatalf("dry-run moved the worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.home, "worktrees", "plan-only-renamed")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created the destination task directory: %v", err)
	}
}

// TestRenameRefusesDirtyWorktree matches Cleanup's own safety posture: a
// worktree with local changes must never be renamed out from under whatever
// process is relying on its current path.
func TestRenameRefusesDirtyWorktree(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "dirty-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "dirty-task",
		NewTask:      "dirty-task-renamed",
		Apply:        true,
	})
	if err == nil {
		t.Fatalf("dirty worktree rename unexpectedly succeeded: %#v", outcome.Results)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Eligible || outcome.Results[0].Applied {
		t.Fatalf("dirty rename outcome = %#v", outcome.Results)
	}
	if !strings.Contains(outcome.Results[0].Reason, "local changes") {
		t.Fatalf("dirty rename reason = %q", outcome.Results[0].Reason)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); statErr != nil {
		t.Fatalf("dirty worktree was moved despite refusal: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.home, "worktrees", "dirty-task-renamed")); !os.IsNotExist(statErr) {
		t.Fatalf("destination task directory was created despite refusal: %v", statErr)
	}
}

// TestRenameRefusesLockedTask mirrors List/Cleanup's own `.lock`-presence
// check: a task with an active or interrupted operation must never be
// renamed out from under it.
func TestRenameRefusesLockedTask(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "locked-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(fixture.home, "worktrees", "locked-task", ".lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "locked-task",
		NewTask:      "locked-task-renamed",
		Apply:        true,
	})
	if err == nil {
		t.Fatalf("locked task rename unexpectedly succeeded: %#v", outcome.Results)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Eligible || !strings.Contains(outcome.Results[0].Reason, "locked") {
		t.Fatalf("locked rename outcome = %#v", outcome.Results)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); statErr != nil {
		t.Fatalf("locked worktree was moved despite refusal: %v", statErr)
	}
}

// TestRenameRefusesDestinationCollision proves a task directory that already
// exists at the destination name blocks the whole rename atomically, rather
// than partially adopting some repositories into someone else's task.
func TestRenameRefusesDestinationCollision(t *testing.T) {
	fixture := newGitFixture(t)
	if _, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "source-task",
	}); err != nil {
		t.Fatal(err)
	}
	// Occupy the destination name with an unrelated task.
	if _, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "taken-task",
		Resume:       false,
	}); err != nil {
		t.Fatal(err)
	}

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "source-task",
		NewTask:      "taken-task",
		Apply:        true,
	})
	if err == nil {
		t.Fatalf("collision rename unexpectedly succeeded: %#v", outcome.Results)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Eligible || !strings.Contains(outcome.Results[0].Reason, "already exists") {
		t.Fatalf("collision rename outcome = %#v", outcome.Results)
	}
	source := filepath.Join(fixture.home, "worktrees", "source-task", "acme", "app")
	if _, statErr := os.Stat(source); statErr != nil {
		t.Fatalf("source worktree was moved despite collision refusal: %v", statErr)
	}
}

// TestRenameDeleteOldBranchRequiresForceUnlessMerged is the regression test
// for the founder's explicit "unmerged work must never be silently lost"
// requirement: --delete-old-branch on an unmerged branch is a no-op unless
// --force is also given, and the branch survives either way until it does.
func TestRenameDeleteOldBranchRequiresForceUnlessMerged(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "unmerged-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, "feature.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, created[0].WorktreeDir, "add", "feature.txt")
	gitTest(t, created[0].WorktreeDir, "commit", "-m", "unmerged work")

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot:    fixture.projectsRoot,
		OldTask:         "unmerged-task",
		NewTask:         "unmerged-task-renamed",
		DeleteOldBranch: true,
		Apply:           true,
	})
	if err != nil {
		t.Fatalf("Rename apply: %v\nresults=%#v", err, outcome.Results)
	}
	if !outcome.Results[0].Applied || outcome.Results[0].OldBranchDeleted {
		t.Fatalf("unmerged branch was deleted without --force: %#v", outcome.Results[0])
	}
	if !strings.Contains(outcome.Results[0].Reason, "--force") {
		t.Fatalf("refusal reason does not mention --force: %q", outcome.Results[0].Reason)
	}
	if !strings.Contains(outcome.Results[0].Reason, "main") {
		t.Fatalf("refusal reason does not say which base it was checked against: %q", outcome.Results[0].Reason)
	}
	if !localBranchExistsIn(t, fixture.canonical, "codex/unmerged-task") {
		t.Fatal("unmerged old branch was deleted despite missing --force")
	}

	// A second rename, this time with --force, must delete it.
	outcome, err = Rename(context.Background(), RenameOptions{
		ProjectsRoot:    fixture.projectsRoot,
		OldTask:         "unmerged-task-renamed",
		NewTask:         "unmerged-task-final",
		Branch:          "codex/unmerged-task-final",
		DeleteOldBranch: true,
		Force:           true,
		Apply:           true,
	})
	if err != nil {
		t.Fatalf("forced Rename apply: %v\nresults=%#v", err, outcome.Results)
	}
	if !outcome.Results[0].Applied || !outcome.Results[0].OldBranchDeleted {
		t.Fatalf("forced delete did not remove the unmerged branch: %#v", outcome.Results[0])
	}
	// codex/unmerged-task-renamed is the branch the worktree was on entering
	// this second rename (created by the first rename above); --force must
	// have deleted that one specifically.
	if localBranchExistsIn(t, fixture.canonical, "codex/unmerged-task-renamed") {
		t.Fatal("unmerged old branch survived --force")
	}
}

// TestRenameDeleteOldBranchWithoutForceWhenMerged proves the safety check is
// specifically about merge state, not a blanket refusal: a branch that is
// already merged into base is deleted without needing --force.
func TestRenameDeleteOldBranchWithoutForceWhenMerged(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "merged-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, "feature.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, created[0].WorktreeDir, "add", "feature.txt")
	gitTest(t, created[0].WorktreeDir, "commit", "-m", "merged work")
	gitTest(t, created[0].WorktreeDir, "push", "-u", "origin", "codex/merged-task")
	gitTest(t, fixture.canonical, "merge", "--no-ff", "codex/merged-task", "-m", "merge feature")
	gitTest(t, fixture.canonical, "push", "origin", "main")

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot:    fixture.projectsRoot,
		OldTask:         "merged-task",
		NewTask:         "merged-task-renamed",
		DeleteOldBranch: true,
		Apply:           true,
	})
	if err != nil {
		t.Fatalf("Rename apply: %v\nresults=%#v", err, outcome.Results)
	}
	if !outcome.Results[0].Applied || !outcome.Results[0].OldBranchDeleted {
		t.Fatalf("merged branch was not deleted without --force: %#v", outcome.Results[0])
	}
	if localBranchExistsIn(t, fixture.canonical, "codex/merged-task") {
		t.Fatal("merged old branch survived deletion")
	}
}

func localBranchExistsIn(t *testing.T, canonicalDir, branch string) bool {
	t.Helper()
	exists, err := localBranchExists(context.Background(), canonicalDir, branch)
	if err != nil {
		t.Fatal(err)
	}
	return exists
}

// TestRenameEligibilityAndCoordination is a fast, non-Git unit test of the
// all-or-nothing coordination rule: one ineligible or malformed repository
// blocks every sibling in the same rename, exactly like Cleanup's
// blockDiagnosedTasks/blockUnsafeTasks — moving part of a task and leaving
// the rest behind would strand the very recycling this verb exists for.
func TestRenameEligibilityAndCoordination(t *testing.T) {
	clean := ListResult{Repository: "acme/app", Clean: true}
	if eligible, reason := renameEligibility(clean); !eligible || reason != "" {
		t.Fatalf("clean entry eligibility = %v, %q", eligible, reason)
	}
	dirty := ListResult{Repository: "acme/app", Clean: false}
	if eligible, reason := renameEligibility(dirty); eligible || !strings.Contains(reason, "local changes") {
		t.Fatalf("dirty entry eligibility = %v, %q", eligible, reason)
	}
	locked := ListResult{Repository: "acme/app", Clean: true, Locked: true}
	if eligible, reason := renameEligibility(locked); eligible || !strings.Contains(reason, "locked") {
		t.Fatalf("locked entry eligibility = %v, %q", eligible, reason)
	}

	plans := []renamePlan{
		{entry: clean, result: RenameResult{Repository: "acme/app", Eligible: true}},
		{entry: dirty, result: RenameResult{Repository: "acme/api", Eligible: false, Reason: "worktree has local changes"}},
	}
	blockRenameTask(plans, nil, "")
	for _, plan := range plans {
		if plan.result.Eligible {
			t.Fatalf("one ineligible repository must block every sibling: %#v", plans)
		}
	}
	if !strings.Contains(plans[0].result.Reason, "coordinated task blocked by") {
		t.Fatalf("blocked sibling reason = %q", plans[0].result.Reason)
	}
	if plans[1].result.Reason != "worktree has local changes" {
		t.Fatalf("original culprit reason must stay unprefixed: %q", plans[1].result.Reason)
	}

	// A destination collision is an absolute, unconditional blocker.
	plans = []renamePlan{{entry: clean, result: RenameResult{Repository: "acme/app", Eligible: true}}}
	blockRenameTask(plans, nil, "destination task already exists: /wb/worktrees/taken")
	if plans[0].result.Eligible || plans[0].result.Reason != "destination task already exists: /wb/worktrees/taken" {
		t.Fatalf("destination collision result = %#v", plans[0].result)
	}
}

func TestDefaultRenameReportDir(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	got := DefaultRenameReportDir("/home/.wb", now)
	want := filepath.Join("/home/.wb", "reports", "worktree-rename", "20260810T120000.000000000Z")
	if got != want {
		t.Fatalf("DefaultRenameReportDir = %s, want %s", got, want)
	}
}
