package canonicalrescue

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fixture struct {
	ProjectsRoot string
	Canonical    string
	Worktree     string
	Origin       string
}

// newFixture builds a canonical clone with a real origin it can push to, plus
// a linked worktree.
func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	run(t, root, "git", "init", "-q", "--bare", origin)

	projectsRoot := filepath.Join(root, "projects")
	canonical := filepath.Join(projectsRoot, "sneat-co", "backstage")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, root, "git", "clone", "-q", origin, canonical)
	gitIn(t, canonical, "config", "user.email", "rescue@example.test")
	gitIn(t, canonical, "config", "user.name", "rescue")
	write(t, filepath.Join(canonical, "README.md"), "original\n")
	gitIn(t, canonical, "add", "-A")
	gitIn(t, canonical, "commit", "-qm", "init")
	gitIn(t, canonical, "branch", "-M", "main")
	gitIn(t, canonical, "push", "-q", "origin", "main")

	worktree := filepath.Join(root, "worktrees", "task", "sneat-co", "backstage")
	gitIn(t, canonical, "worktree", "add", "-q", "-b", "task", worktree)
	return fixture{ProjectsRoot: projectsRoot, Canonical: canonical, Worktree: worktree, Origin: origin}
}

func run(t *testing.T, directory, name string, arguments ...string) string {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

// gitIn runs git for the fixture. It is named apart from the package's own
// git helper, which takes a context.
func gitIn(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	return run(t, directory, "git", arguments...)
}

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// dirtyTheClone reproduces the 2026-08-27 shape: a modified tracked file, a
// staged file, and — the one that matters — a complete untracked document
// nested in a new directory, which exists nowhere else.
func dirtyTheClone(t *testing.T, repositories fixture) {
	t.Helper()
	write(t, filepath.Join(repositories.Canonical, "README.md"), "edited in the clone\n")
	write(t, filepath.Join(repositories.Canonical, "staged.txt"), "staged\n")
	gitIn(t, repositories.Canonical, "add", "staged.txt")
	write(t,
		filepath.Join(repositories.Canonical, "spec", "lessons", "unlanded", "README.md"),
		"a finished lesson that exists nowhere else\n")
	// Ignored content must survive every step untouched.
	write(t, filepath.Join(repositories.Canonical, ".gitignore"), "/.worktree.md\n")
	gitIn(t, repositories.Canonical, "add", ".gitignore")
	gitIn(t, repositories.Canonical, "commit", "-qm", "ignore the marker")
	write(t, filepath.Join(repositories.Canonical, ".worktree.md"), "generated marker\n")
}

func options(repositories fixture) Options {
	return Options{ProjectsRoot: repositories.ProjectsRoot, Branch: "rescue/test"}
}

// TestInspectFindsUncommittedWorkAndIgnoresIgnoredPaths keeps a generated
// marker from ever reading as work needing rescue.
func TestInspectFindsUncommittedWorkAndIgnoresIgnoredPaths(t *testing.T) {
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	report, err := Inspect(context.Background(), repositories.Canonical, options(repositories))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !report.Dirty() {
		t.Fatal("a dirty clone reported clean")
	}
	if report.UntrackedCount != 1 {
		t.Fatalf("untracked count = %d, want 1 (the lesson)", report.UntrackedCount)
	}
	var paths []string
	for _, change := range report.Changes {
		paths = append(paths, change.Path)
	}
	joined := strings.Join(paths, " ")
	if !strings.Contains(joined, "spec/lessons/unlanded/") {
		t.Fatalf("the untracked lesson is missing from %v", paths)
	}
	if strings.Contains(joined, ".worktree.md") {
		t.Fatalf("an ignored path was reported as work needing rescue: %v", paths)
	}
}

// TestInspectRefusesALinkedWorktree keeps rescue pointed at the checkout where
// uncommitted work is actually at risk.
func TestInspectRefusesALinkedWorktree(t *testing.T) {
	repositories := newFixture(t)
	if _, err := Inspect(context.Background(), repositories.Worktree, options(repositories)); err == nil {
		t.Fatal("a linked worktree was accepted for rescue")
	}
}

// TestCaptureLeavesTheCloneExactlyAsItFoundIt is the property that makes a
// rescue safe to run on a clone somebody else is looking at.
func TestCaptureLeavesTheCloneExactlyAsItFoundIt(t *testing.T) {
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	beforeHead := gitIn(t, repositories.Canonical, "rev-parse", "HEAD")
	beforeBranch := gitIn(t, repositories.Canonical, "rev-parse", "--abbrev-ref", "HEAD")
	beforeStatus := gitIn(t, repositories.Canonical, "status", "--porcelain=v1")
	beforeStash := gitIn(t, repositories.Canonical, "stash", "list")

	report, err := Inspect(context.Background(), repositories.Canonical, options(repositories))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	report, err = Capture(context.Background(), report)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if report.RescueCommit == "" {
		t.Fatal("no rescue commit was recorded")
	}
	if got := gitIn(t, repositories.Canonical, "rev-parse", "HEAD"); got != beforeHead {
		t.Fatalf("HEAD moved: %s -> %s", beforeHead, got)
	}
	if got := gitIn(t, repositories.Canonical, "rev-parse", "--abbrev-ref", "HEAD"); got != beforeBranch {
		t.Fatalf("the branch changed: %s -> %s", beforeBranch, got)
	}
	if got := gitIn(t, repositories.Canonical, "status", "--porcelain=v1"); got != beforeStatus {
		t.Fatalf("the working tree or index changed:\nbefore:\n%s\nafter:\n%s", beforeStatus, got)
	}
	if got := gitIn(t, repositories.Canonical, "stash", "list"); got != beforeStash {
		t.Fatalf("the shared stash stack was written to: %q", got)
	}
	// The content is genuinely in the branch, untracked file included.
	listing := gitIn(t, repositories.Canonical, "ls-tree", "-r", "--name-only", "rescue/test")
	for _, expected := range []string{"spec/lessons/unlanded/README.md", "staged.txt", "README.md"} {
		if !strings.Contains(listing, expected) {
			t.Fatalf("%q is not in the rescue branch:\n%s", expected, listing)
		}
	}
	content := gitIn(t, repositories.Canonical, "show", "rescue/test:spec/lessons/unlanded/README.md")
	if !strings.Contains(content, "exists nowhere else") {
		t.Fatalf("the untracked lesson was captured with the wrong content: %q", content)
	}
	if strings.Contains(listing, ".worktree.md") {
		t.Fatal("an ignored path was committed into the rescue branch")
	}
}

// TestCaptureReusesItsOwnBranchButNeverAnotherOne makes the two-step flow —
// capture, review, restore — work without capturing twice, while refusing to
// write over a branch holding somebody else's work.
func TestCaptureReusesItsOwnBranchButNeverAnotherOne(t *testing.T) {
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	report, err := Inspect(context.Background(), repositories.Canonical, options(repositories))
	if err != nil {
		t.Fatal(err)
	}
	first, err := Capture(context.Background(), report)
	if err != nil {
		t.Fatalf("first Capture: %v", err)
	}
	second, err := Capture(context.Background(), report)
	if err != nil {
		t.Fatalf("second Capture: %v", err)
	}
	if second.RescueCommit != first.RescueCommit {
		t.Fatalf("the second capture created a new commit: %s vs %s", second.RescueCommit, first.RescueCommit)
	}
	// A branch holding something else is never overwritten.
	occupied := report
	occupied.RescueBranch = "someone-elses-work"
	gitIn(t, repositories.Canonical, "branch", "someone-elses-work", "HEAD")
	if _, err := Capture(context.Background(), occupied); err == nil {
		t.Fatal("a branch holding different content was written over")
	}
}

// TestRestoreRefusesUntilTheContentIsProvablySomewhereElse is the guard on the
// only destructive step in the package.
func TestRestoreRefusesUntilTheContentIsProvablySomewhereElse(t *testing.T) {
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	report, err := Inspect(context.Background(), repositories.Canonical, options(repositories))
	if err != nil {
		t.Fatal(err)
	}
	// Nothing captured yet.
	if _, err := Restore(context.Background(), report, true); err == nil {
		t.Fatal("restore ran with nothing captured")
	}
	captured, err := Capture(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	// Captured but not pushed, and the risk not accepted.
	if _, err := Restore(context.Background(), captured, false); err == nil {
		t.Fatal("restore ran against a rescue branch that exists only locally")
	}
	// Without --untracked-files=all, Git collapses the new directory to "spec/".
	if !strings.Contains(gitIn(t, repositories.Canonical, "status", "--porcelain=v1"), "spec/") {
		t.Fatal("a refused restore still changed the clone")
	}
}

// TestRestoreCleansTheCloneAndKeepsIgnoredPaths completes the journey and
// checks the one thing a `git clean -fdx` would have destroyed.
func TestRestoreCleansTheCloneAndKeepsIgnoredPaths(t *testing.T) {
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	report, err := Inspect(context.Background(), repositories.Canonical, options(repositories))
	if err != nil {
		t.Fatal(err)
	}
	report, err = Capture(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	report, err = Push(context.Background(), report, "origin")
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !report.Pushed {
		t.Fatal("the push was not recorded")
	}
	report, err = Restore(context.Background(), report, false)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !report.Restored {
		t.Fatal("the restore was not recorded")
	}
	if status := gitIn(t, repositories.Canonical, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("the clone is still dirty:\n%s", status)
	}
	if _, err := os.Stat(filepath.Join(repositories.Canonical, ".worktree.md")); err != nil {
		t.Fatalf("the ignored marker was destroyed by the clean: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repositories.Canonical, "spec", "lessons", "unlanded", "README.md")); err == nil {
		t.Fatal("the rescued content is still in the clone")
	}
	// And it is recoverable from the remote, not just from this machine.
	remoteListing := run(t, repositories.Origin, "git", "ls-tree", "-r", "--name-only", "rescue/test")
	if !strings.Contains(remoteListing, "spec/lessons/unlanded/README.md") {
		t.Fatalf("the rescued lesson is not on the remote:\n%s", remoteListing)
	}
}

// TestRestoreRefusesWhenTheCaptureIsIncomplete keeps a partial capture from
// being indistinguishable from a complete one at the exact moment that
// difference destroys work.
func TestRestoreRefusesWhenTheCaptureIsIncomplete(t *testing.T) {
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	report, err := Inspect(context.Background(), repositories.Canonical, options(repositories))
	if err != nil {
		t.Fatal(err)
	}
	report, err = Capture(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	report.Pushed = true
	report.Changes = append(report.Changes, Change{Status: "??", Path: "never/captured.md"})
	if _, err := Restore(context.Background(), report, true); err == nil {
		t.Fatal("a restore ran with a path missing from the rescue commit")
	} else if !strings.Contains(err.Error(), "never/captured.md") {
		t.Fatalf("the refusal does not name the missing path: %v", err)
	}
}
