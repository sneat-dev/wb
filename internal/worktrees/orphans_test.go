package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// orphanFixture builds a projects root with one canonical clone and lets a test
// add linked worktrees wherever it likes, which is how the three real layout
// generations are reproduced.
type orphanFixture struct {
	t            *testing.T
	root         string
	projectsRoot string
	home         string
	canonical    string
}

func newOrphanFixture(t *testing.T) *orphanFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture := &orphanFixture{
		t:            t,
		root:         root,
		projectsRoot: filepath.Join(root, "projects"),
		home:         filepath.Join(root, "home", ".wb"),
		canonical:    filepath.Join(root, "projects", "acme", "app"),
	}
	if err := os.MkdirAll(fixture.canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "init", "--initial-branch=main")
	gitTest(t, fixture.canonical, "config", "user.name", "WB Test")
	gitTest(t, fixture.canonical, "config", "user.email", "wb@example.test")
	gitTest(t, fixture.canonical, "commit", "--allow-empty", "-m", "base")
	// A local origin/main ref stands in for the remote target, which is what
	// "already landed" is measured against.
	gitTest(t, fixture.canonical, "update-ref", "refs/remotes/origin/main", "HEAD")
	t.Setenv("WB_HOME", fixture.home)
	return fixture
}

// addWorktree creates a linked worktree at an arbitrary path so a test can
// place it in the current home, the legacy hierarchy, or outside both.
func (fixture *orphanFixture) addWorktree(path, branch string, commit bool) string {
	fixture.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fixture.t.Fatal(err)
	}
	gitTest(fixture.t, fixture.canonical, "worktree", "add", "-b", branch, path, "main")
	if commit {
		gitTest(fixture.t, path, "config", "user.name", "WB Test")
		gitTest(fixture.t, path, "config", "user.email", "wb@example.test")
		gitTest(fixture.t, path, "commit", "--allow-empty", "-m", "work on "+branch)
	}
	return path
}

func (fixture *orphanFixture) report(t *testing.T, now time.Time) OrphanReport {
	t.Helper()
	report, err := Orphans(t.Context(), OrphanOptions{
		ProjectsRoot: fixture.projectsRoot, Base: "main", Now: now,
		StaleAfter: 14 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func findOrphan(report OrphanReport, branch string) (OrphanWorktree, bool) {
	for _, family := range report.Families {
		for _, worktree := range family.Worktrees {
			if worktree.Branch == branch {
				return worktree, true
			}
		}
	}
	return OrphanWorktree{}, false
}

// Discovery must go through the clone's own registry, because that is the only
// source that sees every layout generation at once. A directory walk of WB's
// home would miss the 489 worktrees that live elsewhere.
func TestOrphansSeeEveryLayoutGeneration(t *testing.T) {
	fixture := newOrphanFixture(t)
	fixture.addWorktree(filepath.Join(fixture.home, "worktrees", "current-effort", "acme", "app"), "current-effort", true)
	fixture.addWorktree(filepath.Join(fixture.projectsRoot, ".wb", "worktrees", "legacy-effort", "acme", "app"), "legacy-effort", true)
	fixture.addWorktree(filepath.Join(fixture.root, "elsewhere", "app-external"), "external-effort", true)

	report := fixture.report(t, time.Now().UTC())
	if report.Totals.Worktrees != 3 {
		t.Fatalf("expected all three layouts discovered, got %d: %+v", report.Totals.Worktrees, report.Totals.ByLayout)
	}
	for branch, layout := range map[string]string{
		"current-effort":  LayoutCurrent,
		"legacy-effort":   LayoutLegacy,
		"external-effort": LayoutExternal,
	} {
		worktree, ok := findOrphan(report, branch)
		if !ok {
			t.Fatalf("%s was not discovered", branch)
		}
		if worktree.Layout != layout {
			t.Fatalf("%s classified as %q, want %q", branch, worktree.Layout, layout)
		}
	}
}

// A removal recommendation drives destructive action, so it must be reserved
// for branches whose commits already exist in the remote target.
func TestOrphansRecommendRemovalOnlyForLandedWork(t *testing.T) {
	fixture := newOrphanFixture(t)
	landed := fixture.addWorktree(filepath.Join(fixture.home, "worktrees", "landed", "acme", "app"), "landed", false)
	unmerged := fixture.addWorktree(filepath.Join(fixture.home, "worktrees", "unmerged", "acme", "app"), "unmerged", true)
	_ = landed

	// Idle for a month, so recency cannot be what protects the unmerged one.
	report := fixture.report(t, time.Now().UTC().Add(30*24*time.Hour))

	landedEntry, _ := findOrphan(report, "landed")
	if landedEntry.Disposition != DispositionRemove || !landedEntry.Merged {
		t.Fatalf("a branch contained in the target is removable: %+v", landedEntry)
	}
	unmergedEntry, _ := findOrphan(report, "unmerged")
	if unmergedEntry.Disposition != DispositionDecide {
		t.Fatalf("unmerged idle work needs a decision, not removal: %+v", unmergedEntry)
	}
	if !strings.Contains(strings.Join(unmergedEntry.Evidence, " "), "WB will not make it") {
		t.Fatalf("the refusal to decide must be explicit: %+v", unmergedEntry.Evidence)
	}
	_ = unmerged
}

// Uncommitted work exists nowhere else, so it must never be swept regardless of
// how the branch relates to the target.
func TestOrphansNeverRecommendRemovingUncommittedWork(t *testing.T) {
	fixture := newOrphanFixture(t)
	path := fixture.addWorktree(filepath.Join(fixture.home, "worktrees", "dirty", "acme", "app"), "dirty-effort", false)
	if err := os.WriteFile(filepath.Join(path, "unsaved.txt"), []byte("work in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := fixture.report(t, time.Now().UTC().Add(90*24*time.Hour))

	entry, ok := findOrphan(report, "dirty-effort")
	if !ok {
		t.Fatal("dirty worktree not discovered")
	}
	if !entry.Dirty || entry.Disposition != DispositionReview {
		t.Fatalf("a dirty worktree needs review even when its branch has landed: %+v", entry)
	}
	if entry.Merged && entry.Disposition == DispositionRemove {
		t.Fatal("merged status must not override uncommitted work")
	}
}

// A family is one subject. A parent must not be swept while a child is live —
// that is the case filesystem nesting would have made destructive.
func TestOrphanFamiliesGroupLexicallyAndStayConservative(t *testing.T) {
	fixture := newOrphanFixture(t)
	fixture.addWorktree(filepath.Join(fixture.home, "worktrees", "feature", "acme", "app"), "feature", false)
	fixture.addWorktree(filepath.Join(fixture.home, "worktrees", "feature.task-one", "acme", "app"), "feature.task-one", false)
	fixture.addWorktree(filepath.Join(fixture.home, "worktrees", "feature.task-two", "acme", "app"), "feature.task-two", true)

	report := fixture.report(t, time.Now().UTC())
	var family *OrphanFamily
	for index := range report.Families {
		if report.Families[index].RootEffort == "feature" {
			family = &report.Families[index]
		}
	}
	if family == nil {
		t.Fatalf("the effort family was not grouped: %+v", report.Families)
	}
	if len(family.Worktrees) != 3 {
		t.Fatalf("all three worktrees belong to one family, got %d", len(family.Worktrees))
	}
	// task-two committed just now, so the whole family is still in use.
	if family.Disposition != DispositionActive {
		t.Fatalf("a family with a live child must not be swept: %+v", family)
	}
	for _, worktree := range family.Worktrees {
		if worktree.EffortID == "feature.task-one" && worktree.ParentEffort != "feature" {
			t.Fatalf("parentage must resolve lexically: %+v", worktree)
		}
	}
}

// Identity must prefer the worktree's own manifest, and must say so when it had
// to guess instead.
func TestOrphansPreferManifestIdentityAndLabelReconstruction(t *testing.T) {
	fixture := newOrphanFixture(t)
	withManifest := fixture.addWorktree(filepath.Join(fixture.home, "worktrees", "declared", "acme", "app"), "declared", false)
	fixture.addWorktree(filepath.Join(fixture.home, "worktrees", "guessed", "acme", "app"), "guessed", false)

	manifest := newCreatedManifest("declared")
	manifest.Repository = "acme/app"
	manifest.Branch = "declared"
	if err := WriteManifest(withManifest, manifest); err != nil {
		t.Fatal(err)
	}
	report := fixture.report(t, time.Now().UTC())

	declared, _ := findOrphan(report, "declared")
	if !declared.HasManifest || declared.Provenance != ProvenanceCreated {
		t.Fatalf("a worktree with a manifest must be identified by it: %+v", declared)
	}
	if !strings.Contains(strings.Join(declared.Evidence, " "), "its own manifest") {
		t.Fatalf("evidence must name the manifest: %+v", declared.Evidence)
	}
	guessed, _ := findOrphan(report, "guessed")
	if guessed.HasManifest {
		t.Fatalf("a worktree with no manifest must not claim one: %+v", guessed)
	}
	if !strings.Contains(strings.Join(guessed.Evidence, " "), "reconstructed") {
		t.Fatalf("a guess must be labelled as one: %+v", guessed.Evidence)
	}
}

// Triage must be safe to run against a fleet with live agents at any moment.
func TestOrphansMutateNothing(t *testing.T) {
	fixture := newOrphanFixture(t)
	path := fixture.addWorktree(filepath.Join(fixture.home, "worktrees", "untouched", "acme", "app"), "untouched", true)
	if err := os.WriteFile(filepath.Join(path, "keep.txt"), []byte("do not touch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := mustGitOutput(t, path, "status", "--porcelain")
	headBefore := mustGitOutput(t, path, "rev-parse", "HEAD")

	fixture.report(t, time.Now().UTC())

	if after := mustGitOutput(t, path, "status", "--porcelain"); after != before {
		t.Fatalf("working tree changed: %q -> %q", before, after)
	}
	if after := mustGitOutput(t, path, "rev-parse", "HEAD"); after != headBefore {
		t.Fatalf("HEAD moved: %q -> %q", headBefore, after)
	}
	if _, err := os.Stat(filepath.Join(path, journalRootDirectory, journalLocalDirectory)); !os.IsNotExist(err) {
		t.Fatal("enumeration must not create a journal in a worktree it only inspected")
	}
}

// One unreadable directory must never hide the rest of the fleet.
func TestOrphansReportUnscannedRatherThanFailing(t *testing.T) {
	fixture := newOrphanFixture(t)
	fixture.addWorktree(filepath.Join(fixture.home, "worktrees", "visible", "acme", "app"), "visible", true)

	blocked := filepath.Join(fixture.projectsRoot, "blocked")
	if err := os.MkdirAll(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	report := fixture.report(t, time.Now().UTC())
	if _, ok := findOrphan(report, "visible"); !ok {
		t.Fatal("an unreadable sibling must not hide a readable worktree")
	}
}

func mustGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitTestRun(dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return out
}

// The parent's branch must not be retired out from under work based on it.
// Children are not nested inside the parent's directory precisely so removal
// cannot delete their working trees; that only pays off if cleanup also
// declines the parent while they are live.
func TestCleanupRefusesAParentWithLiveSubEfforts(t *testing.T) {
	root := t.TempDir()
	worktreesRoot := filepath.Join(root, "worktrees")
	for _, effort := range []string{"feature", "feature.task-one", "unrelated"} {
		if err := os.MkdirAll(filepath.Join(worktreesRoot, effort), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	results := []CleanupResult{
		{ListResult: ListResult{Task: "feature", Repository: "acme/app"}, Eligible: true},
		{ListResult: ListResult{Task: "feature.task-one", Repository: "acme/app"}, Eligible: true},
		{ListResult: ListResult{Task: "unrelated", Repository: "acme/app"}, Eligible: true},
	}
	blockEffortsWithLiveDescendants(results, []string{worktreesRoot})

	if results[0].Eligible {
		t.Fatal("the parent effort must be refused while a child is live")
	}
	if !strings.Contains(results[0].Reason, "feature.task-one") {
		t.Fatalf("the refusal must name the live children: %q", results[0].Reason)
	}
	if !results[1].Eligible {
		t.Fatal("a leaf task effort stays eligible; children terminalize before parents")
	}
	if !results[2].Eligible {
		t.Fatal("an unrelated effort must not be blocked by a lexical prefix")
	}
}

// A sibling that merely shares a prefix is not a child.
func TestCleanupPrefixIsNotParentage(t *testing.T) {
	root := t.TempDir()
	worktreesRoot := filepath.Join(root, "worktrees")
	for _, effort := range []string{"feature", "feature-extended"} {
		if err := os.MkdirAll(filepath.Join(worktreesRoot, effort), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	results := []CleanupResult{{ListResult: ListResult{Task: "feature", Repository: "acme/app"}, Eligible: true}}
	blockEffortsWithLiveDescendants(results, []string{worktreesRoot})
	if !results[0].Eligible {
		t.Fatalf("feature-extended is a sibling, not a child: %q", results[0].Reason)
	}
}

// Adoption must be safe to run repeatedly against a fleet with live agents, so
// it writes nothing without --apply, never touches a working tree, and reaches
// the same state when interrupted and resumed.
func TestBackfillIsDryByDefaultAdditiveAndIdempotent(t *testing.T) {
	fixture := newOrphanFixture(t)
	path := fixture.addWorktree(filepath.Join(fixture.home, "worktrees", "legacy-effort", "acme", "app"), "legacy-effort", true)
	if err := os.WriteFile(filepath.Join(path, "in-progress.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirtyBefore := mustGitOutput(t, path, "status", "--porcelain")

	planned, err := Backfill(t.Context(), BackfillOptions{ProjectsRoot: fixture.projectsRoot, Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned) != 1 || planned[0].Action != BackfillWouldWrite {
		t.Fatalf("a dry run must only plan: %+v", planned)
	}
	if _, err := ReadManifest(path); err == nil {
		t.Fatal("a dry run must not write a manifest")
	}

	applied, err := Backfill(t.Context(), BackfillOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0].Action != BackfillWritten {
		t.Fatalf("apply must write: %+v", applied)
	}
	manifest, err := ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Provenance != ProvenanceReconstructed {
		t.Fatalf("a backfilled manifest is an inference and must say so: %+v", manifest)
	}
	// Uncommitted work is untouched: the journal path is new and excluded.
	if after := mustGitOutput(t, path, "status", "--porcelain"); after != dirtyBefore {
		t.Fatalf("backfill changed the working tree: %q -> %q", dirtyBefore, after)
	}
	// It must never invent instructions nobody gave.
	if prompts, err := ListPrompts(path); err != nil || len(prompts) != 0 {
		t.Fatalf("backfill must not fabricate a prompt: %+v (%v)", prompts, err)
	}

	again, err := Backfill(t.Context(), BackfillOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Apply: true})
	if err != nil {
		t.Fatalf("re-running after an interruption must be safe: %v", err)
	}
	if again[0].Action != BackfillPresent {
		t.Fatalf("a second sweep must recognize existing state: %+v", again)
	}
}

// A registration whose working tree is gone is Git bookkeeping, not a worktree
// to adopt. Reporting it as skipped-with-a-reason is how the operator learns
// `git worktree prune` is the remedy.
func TestBackfillSkipsRegistrationsWithNoWorkingTree(t *testing.T) {
	fixture := newOrphanFixture(t)
	path := fixture.addWorktree(filepath.Join(fixture.home, "worktrees", "vanished", "acme", "app"), "vanished", true)
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	results, err := Backfill(t.Context(), BackfillOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Action != BackfillSkipped {
		t.Fatalf("a vanished working tree cannot be adopted: %+v", results)
	}
	if !strings.Contains(results[0].Reason, "prune") {
		t.Fatalf("the reason must name the remedy: %q", results[0].Reason)
	}
}
