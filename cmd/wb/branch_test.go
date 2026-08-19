package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestBranchCleanupDefaultsToSafeDryRun(t *testing.T) {
	command := newBranchCleanupCmd()
	apply := command.Flags().Lookup("apply")
	if apply == nil || apply.DefValue != "false" {
		t.Fatalf("--apply default = %#v, want false", apply)
	}
	scope := command.Flags().Lookup("scope")
	if scope == nil || scope.DefValue != "local" {
		t.Fatalf("--scope default = %#v, want local", scope)
	}
	olderThan := command.Flags().Lookup("older-than")
	if olderThan == nil || olderThan.DefValue != (24*time.Hour).String() {
		t.Fatalf("--older-than default = %#v, want %s", olderThan, 24*time.Hour)
	}
	if command.Flags().Lookup("report-dir") == nil {
		t.Fatal("cleanup command has no --report-dir")
	}
	if command.Flags().Lookup("remote") != nil {
		t.Fatal("wb branch cleanup must not define a --remote boolean; scope is selected only by --scope")
	}
}

func TestBranchListDefaultsShowEveryAgeAndDisposition(t *testing.T) {
	command := newBranchListCmd()
	scope := command.Flags().Lookup("scope")
	if scope == nil || scope.DefValue != "local" {
		t.Fatalf("--scope default = %#v, want local", scope)
	}
	olderThan := command.Flags().Lookup("older-than")
	if olderThan == nil || olderThan.DefValue != "0s" {
		t.Fatalf("--older-than default = %#v, want 0s", olderThan)
	}
	only := command.Flags().Lookup("only")
	if only == nil || only.DefValue != "" {
		t.Fatalf("--only default = %#v, want empty", only)
	}
	if command.Flags().Lookup("apply") != nil {
		t.Fatal("wb branch list must be read-only and must not accept --apply")
	}
}

func TestBranchHelpExplainsEvidenceTaxonomyAndInvariants(t *testing.T) {
	list := newBranchListCmd()
	for _, wanted := range []string{
		"contained", "absorbed", "unique", "protected", "in-use", "unreadable",
		"never eligible for --apply", "read-only in every configuration", "[n/N] repository",
	} {
		if !strings.Contains(list.Long, wanted) {
			t.Errorf("branch list help does not mention %q", wanted)
		}
	}
	cleanup := newBranchCleanupCmd()
	for _, wanted := range []string{
		"dry-run plan", "absorbed is never eligible", "compare-and-delete",
		"force-with-lease", "pull-request evidence", "never removes, moves, or modifies any working tree",
		"between plan and apply refuses only itself",
	} {
		if !strings.Contains(cleanup.Long, wanted) {
			t.Errorf("branch cleanup help does not mention %q", wanted)
		}
	}
}

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
	worktreeCleanup, _, err := root.Find([]string{"worktree", "cleanup"})
	if err != nil {
		t.Fatal(err)
	}
	if worktreeCleanup.Flags().Lookup("scope") != nil {
		t.Fatal("wb worktree cleanup must not gain a branch-scope flag")
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
		{"bad format", []string{"branch", "list", "--format", "yaml", "--projects-root", t.TempDir()}, "unsupported format"},
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

func TestBranchCleanupRejectsUnsupportedScopeAsFindings(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"branch", "cleanup", "--scope", "bogus", "--projects-root", t.TempDir()}
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

func TestBranchCleanupDryRunOnEmptyProjectsRootWritesNoReport(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	args := []string{"branch", "cleanup", "--projects-root", root}
	if code := run(args, &stdout, &stderr); code != exitOK {
		t.Fatalf("run(%q) exit = %d, stderr=%s", args, code, stderr.String())
	}
	if strings.Contains(stdout.String(), "report:") {
		t.Fatalf("dry run reported a report path: %s", stdout.String())
	}
}

// TestBranchCleanupUnreadableSkipRowNamesRepository is the CLI-level
// regression for the founder's `wb branch cleanup --scope all` report: 41
// rows read exactly "  skip           (unreadable): disposition unreadable
// is never eligible for --apply" with no repository, no branch, and no
// underlying cause — nothing an operator could act on. specscore/winget-pkgs
// had no refs/heads/main on origin, so fetching the exact target failed and
// the whole repository was reported unreadable in a single row with empty
// Scope and Branch; that row must still name the repository and the real
// fetch failure inline, not rely solely on the group header above it.
func TestBranchCleanupUnreadableSkipRowNamesRepository(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	var stdout, stderr bytes.Buffer
	// "does-not-exist" was never pushed, so fetching it from origin fails
	// exactly as it did for the repository with no refs/heads/main.
	args := []string{
		"branch", "cleanup", "--scope", "all", "--base", "does-not-exist",
		"--projects-root", projects,
	}
	if code := run(args, &stdout, &stderr); code != exitOK {
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
