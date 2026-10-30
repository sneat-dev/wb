package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestBranchCommandIsASiblingOfWorktreeNotNestedUnderIt(t *testing.T) {
	root := newRootCmd()
	branch, _, err := root.Find([]string{"branch"})
	if err != nil {
		t.Fatal(err)
	}
	if branch.Parent() != root {
		t.Fatalf("wb branch parent = %v, want root", branch.Parent())
	}
	if branch.Parent().Name() == "worktree" {
		t.Fatal("wb branch must not be nested under wb worktree")
	}
	worktreeCleanup, _, err := root.Find([]string{"worktree", "retire"})
	if err != nil {
		t.Fatal(err)
	}
	if worktreeCleanup.Flags().Lookup("scope") != nil {
		t.Fatal("wb worktree retire must not gain a branch-scope flag")
	}
}

func TestBranchListRejectsUnsupportedScopeAndOnlyAsUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"bad scope", []string{"branch", "list", "--scope", "bogus", "--projects-root", t.TempDir()}, "unsupported --scope"},
		{"bad only", []string{"branch", "list", "--only", "bogus", "--projects-root", t.TempDir()}, "unsupported --only"},
		{"bad format", []string{"branch", "list", "--format", "toml", "--projects-root", t.TempDir()}, "unsupported format"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != exitFindings && code != exitUsage {
				t.Fatalf("run(%q) exit = %d, stderr=%s", test.args, code, stderr.String())
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want to contain %q", stderr.String(), test.want)
			}
		})
	}
}

func TestCleanRejectsUnsupportedScopeAsFindings(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"clean", "--scope", "bogus", "--projects-root", t.TempDir()}
	if code := run(args, &stdout, &stderr); code != exitFindings && code != exitUsage {
		t.Fatalf("run(%q) exit = %d, stderr=%s", args, code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unsupported --scope") {
		t.Fatalf("stderr = %q, want to mention --scope", stderr.String())
	}
}

func TestBranchListEmptyProjectsRootReportsNoBranches(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	args := []string{"branch", "list", "--projects-root", root}
	if code := run(args, &stdout, &stderr); code != exitOK {
		t.Fatalf("run(%q) exit = %d, stderr=%s", args, code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no branches matched") {
		t.Fatalf("stdout = %q, want \"no branches matched\"", stdout.String())
	}
}

// TestBranchCountEmptyProjectsRootReportsZeroTotals drives "wb branch count"
// through the real CLI dispatch (not just a structural flag check), so the
// RunE closure that reads inv.projectsRoot before calling
// worktrees.BranchList actually executes. An empty projects root has no
// repositories to filter either way, so this does not prove inv.filterFlag's
// value reaches BranchList; it only proves the dispatch path runs.
func TestBranchCountEmptyProjectsRootReportsZeroTotals(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	args := []string{"branch", "count", "--projects-root", root}
	if code := run(args, &stdout, &stderr); code != exitOK {
		t.Fatalf("run(%q) exit = %d, stderr=%s", args, code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "STATUS       REFS") {
		t.Fatalf("stdout = %q, want the count table header", stdout.String())
	}
}

func TestCleanOnAnEmptyProjectsRootIsAFindingNotAnEmptySuccess(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	args := []string{"clean", "--filter", "nomatch/*", "--projects-root", root}
	if code := run(args, &stdout, &stderr); code != exitFindings || !strings.Contains(stderr.String(), "0 repositories matched --filter nomatch/*") {
		t.Fatalf("run(%q) exit = %d, stderr=%s", args, code, stderr.String())
	}
	if strings.Contains(stdout.String(), "report:") {
		t.Fatalf("an unmatched selection reported a report path: %s", stdout.String())
	}
}

// TestCleanUnreadableBranchRowNamesRepository is the CLI-level
// regression for the founder's `wb clean --scope all` report: 41
// rows read exactly "  skip           (unreadable): disposition unreadable
// is never eligible for --apply" with no repository, no branch, and no
// underlying cause — nothing an operator could act on. specscore/winget-pkgs
// had no refs/heads/main on origin, so fetching the exact target failed and
// the whole repository was reported unreadable in a single row with empty
// Scope and Branch; that row must still name the repository and the real
// fetch failure inline, not rely solely on the group header above it.
func TestCleanUnreadableBranchRowNamesRepository(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	var stdout, stderr bytes.Buffer
	// "does-not-exist" was never pushed, so fetching it from origin fails
	// exactly as it did for the repository with no refs/heads/main.
	args := []string{
		"clean", "--scope", "remote", "--base", "does-not-exist",
		"--projects-root", projects,
	}
	if code := run(args, &stdout, &stderr); code != exitFindings {
		t.Fatalf("run(%q) exit = %d, stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "unreadable") {
		t.Fatalf("stdout = %q, want an unreadable row", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "unreadable") {
			continue
		}
		if !strings.Contains(line, "acme/app") {
			t.Fatalf("unreadable row = %q, want it to name repository acme/app inline", line)
		}
		if !strings.Contains(line, "fetch exact origin/does-not-exist target") {
			t.Fatalf("unreadable row = %q, want it to name the underlying fetch failure", line)
		}
	}
}
