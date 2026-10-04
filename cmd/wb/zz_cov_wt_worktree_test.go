package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func cwWtCmdWriter(writer *cwWtFailWriter) *cobra.Command {
	command := &cobra.Command{}
	command.SetOut(writer)
	return command
}

func TestCwWtRequireOutputFormat(t *testing.T) {
	if err := requireOutputFormat("json", "text", "json"); err != nil {
		t.Fatalf("allowed value = %v", err)
	}
	err := requireOutputFormat("yaml", "text", "json")
	if err == nil || !strings.Contains(err.Error(), "text or json") {
		t.Fatalf("refused value = %v", err)
	}
}

func TestCwWtWorktreeCmdsRejectBadFormatInProcess(t *testing.T) {
	projects := t.TempDir()
	builders := map[string]func() *cobra.Command{
		"log-show":    func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) },
		"log-refresh": func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) },
		"backfill":    func() *cobra.Command { return newWorktreeBackfillCmd(&invocation{}) },
		"adopt":       func() *cobra.Command { return newWorktreeAdoptCmd(&invocation{}) },
		"orphans":     func() *cobra.Command { return newWorktreeOrphansCmd(&invocation{}) },
		"list":        func() *cobra.Command { return newWorktreeListCmd(&invocation{}) },
		"marker":      func() *cobra.Command { return newWorktreeMarkerCmd(&invocation{}) },
		"relocate":    func() *cobra.Command { return newWorktreeRelocateCmd(&invocation{}) },
		"checkpoint":  newWorktreeCheckpointFetchCmd,
		"rescue":      func() *cobra.Command { return newWorktreeRescueCmd(&invocation{}) },
		"end":         func() *cobra.Command { return newWorktreeEndCmd(&invocation{}) },
		"gc":          func() *cobra.Command { return newWorktreeGCCmd(&invocation{}) },
		"cleanup":     func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{}) },
	}
	arguments := map[string][]string{
		"checkpoint":  {"--task", "t"},
		"relocate":    {"t"},
		"rescue":      {"."},
		"end":         {"t"},
		"cleanup":     {"t"},
		"marker":      {"."},
		"adopt":       {"."},
		"log-show":    {"show", "."},
		"log-refresh": {"refresh", "."},
	}
	for name, build := range builders {
		args := append(append([]string{}, arguments[name]...), "--format", "bogus")
		_, _, err := cwCovExec(t, projects, build, args...)
		if err == nil || !strings.Contains(err.Error(), "unsupported format") {
			t.Errorf("%s --format bogus error = %v", name, err)
		}
	}
}

func TestCwWtWorktreeCleanupArgValidation(t *testing.T) {
	projects := t.TempDir()
	cases := []struct {
		args []string
		want string
	}{
		{args: nil, want: "supply one or more tasks or use --all-merged"},
		{args: []string{"--recover-stages"}, want: "--recover-stages requires one or more named tasks"},
		{args: []string{"--recover-stages", "--retire-shells", "t"}, want: "--recover-stages cannot be combined"},
		{args: []string{"--retire-shells", "t"}, want: "--retire-shells sweeps every task"},
		{args: []string{"--retire-shells", "--all-merged"}, want: "--retire-shells and --all-merged cannot be combined"},
		{args: []string{"t", "--all-merged"}, want: "tasks and --all-merged cannot be combined"},
		{args: []string{"t", "--resume-interrupted", "--apply", "extra"}, want: "--resume-interrupted requires one explicit task"},
	}
	for _, test := range cases {
		_, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{}) }, test.args...)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("cleanup %v error = %v, want %q", test.args, err, test.want)
		}
	}
}

func TestCwWtWorktreeListAndSummaryInProcess(t *testing.T) {
	projects := cwCovProjectsRoot(t, "acme/app")
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeListCmd(&invocation{projectsRoot: projects}) })
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if !strings.Contains(stdout, "no WB worktrees") {
		t.Fatalf("worktree list stdout = %q", stdout)
	}

	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeListCmd(&invocation{projectsRoot: projects}) }, "--format", "json")
	if err != nil {
		t.Fatalf("worktree list json: %v", err)
	}
	if !strings.Contains(stdout, "\"results\"") {
		t.Fatalf("worktree list json stdout = %q", stdout)
	}

	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeListCmd(&invocation{projectsRoot: projects}) }, "--finalized", "--not-finalized")
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("conflicting finalize filters = %v", err)
	}

	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeSummaryCmd(&invocation{projectsRoot: projects}) }, "absent-task")
	if err != nil {
		t.Fatalf("worktree summary: %v", err)
	}
	if !strings.Contains(stdout, "no live worktrees for this task") {
		t.Fatalf("worktree summary stdout = %q", stdout)
	}

	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeSummaryCmd(&invocation{projectsRoot: projects}) }, "absent-task", "--format", "json")
	if err != nil {
		t.Fatalf("worktree summary json: %v", err)
	}
	if !strings.Contains(stdout, "\"results\"") {
		t.Fatalf("worktree summary json stdout = %q", stdout)
	}
}

// TestWorktreeListFilterNarrowsResultsToMatchingRepository proves --filter
// actually narrows worktree list's results, not just how they are described:
// with live worktrees in two different orgs, an unfiltered list names both
// tasks, and adding --filter for one org's slug names only that one.
// Mutating ListOptions.Filter's application (internal/worktrees) to a no-op
// turns this from PASS to FAIL, proving the value.
func TestWorktreeListFilterNarrowsResultsToMatchingRepository(t *testing.T) {
	projects := t.TempDir()
	seeds := t.TempDir()
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	prompt := writeOriginalPromptFixture(t, "filter fixture")

	acmeClone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seeds, "app", acmeClone)
	otherClone := filepath.Join(projects, "other-org", "app2")
	cwCovCloneWithOrigin(t, seeds, "app2", otherClone)

	for _, spec := range []struct{ task, repo string }{
		{"cwwt-acme-task", "acme/app"},
		{"cwwt-other-task", "other-org/app2"},
	} {
		if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCreateCmd(&invocation{projectsRoot: projects}) },
			spec.task, spec.repo, "--model", "unknown", "--mode", "manual", "--initiator", "cwWt",
			"--original-prompt-file", prompt, "--no-claim"); err != nil {
			t.Fatalf("create %s/%s: %v", spec.task, spec.repo, err)
		}
	}

	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeListCmd(&invocation{projectsRoot: projects}) })
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if !strings.Contains(stdout, "cwwt-acme-task") || !strings.Contains(stdout, "cwwt-other-task") {
		t.Fatalf("unfiltered list = %q, want both tasks", stdout)
	}

	filtered, _, err := cwCovExec(t, projects, func() *cobra.Command {
		return newWorktreeListCmd(&invocation{projectsRoot: projects, filterFlag: "acme"})
	})
	if err != nil {
		t.Fatalf("worktree list --filter acme: %v", err)
	}
	if !strings.Contains(filtered, "cwwt-acme-task") {
		t.Fatalf("filtered list = %q, want cwwt-acme-task", filtered)
	}
	if strings.Contains(filtered, "cwwt-other-task") {
		t.Fatalf("filtered list still names the excluded task: %q", filtered)
	}
}

func TestCwWtWorktreeBackfillAdoptOrphansInProcess(t *testing.T) {
	projects := cwCovProjectsRoot(t, "acme/app")
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeBackfillCmd(&invocation{projectsRoot: projects}) })
	if err != nil {
		t.Fatalf("backfill dry run: %v", err)
	}
	if !strings.Contains(stdout, "dry-run only, pass --apply to write") {
		t.Fatalf("backfill stdout = %q", stdout)
	}
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeBackfillCmd(&invocation{projectsRoot: projects}) }, "--apply")
	if err != nil {
		t.Fatalf("backfill apply: %v", err)
	}
	if strings.Contains(stdout, "dry-run only") {
		t.Fatalf("backfill apply stdout = %q", stdout)
	}
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeBackfillCmd(&invocation{projectsRoot: projects}) }, "--format", "json"); err != nil {
		t.Fatalf("backfill json: %v", err)
	}

	// adopt requires exactly one selector.
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeAdoptCmd(&invocation{projectsRoot: projects}) }, "--apply"); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("adopt with no selector = %v", err)
	}
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeAdoptCmd(&invocation{projectsRoot: projects}) }, ".", "--all-external"); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("adopt with both selectors = %v", err)
	}
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeAdoptCmd(&invocation{projectsRoot: projects}) }, "--all-external")
	if err != nil {
		t.Fatalf("adopt dry run: %v", err)
	}
	if !strings.Contains(stdout, "dry-run only, pass --apply to write") {
		t.Fatalf("adopt stdout = %q", stdout)
	}
	if _, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeAdoptCmd(&invocation{projectsRoot: projects}) }, "--all-external", "--format", "json"); err != nil {
		t.Fatalf("adopt json: %v", err)
	}

	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeOrphansCmd(&invocation{projectsRoot: projects}) })
	if err != nil {
		t.Fatalf("orphans: %v", err)
	}
	if !strings.Contains(stdout, "worktrees in") {
		t.Fatalf("orphans stdout = %q", stdout)
	}
	if _, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeOrphansCmd(&invocation{projectsRoot: projects}) }, "--format", "json"); err != nil {
		t.Fatalf("orphans json: %v", err)
	}
	if _, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeOrphansCmd(&invocation{projectsRoot: projects}) }, "--only", "remove"); err != nil {
		t.Fatalf("orphans --only: %v", err)
	}
}

func TestCwWtWorktreeRenameInProcess(t *testing.T) {
	projects := cwCovProjectsRoot(t, "acme/app")
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	// An old task that does not exist is refused with the backend's reason.
	_, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRenameCmd(&invocation{projectsRoot: projects}) }, "absent-old", "absent-new", "--model", "unknown")
	if err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("rename of an absent task = %v", err)
	}
	if _, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRenameCmd(&invocation{projectsRoot: projects}) }, "absent-old", "absent-new", "--model", "unknown", "--format", "json"); err == nil {
		t.Fatal("rename json of an absent task must fail")
	}
	// --branch and --branch-prefix together are refused before the backend.
	if _, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRenameCmd(&invocation{projectsRoot: projects}) }, "a", "b", "--branch", "x", "--branch-prefix", "y"); err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("rename --branch with --branch-prefix = %v", err)
	}
	// --model is required for the new Work Log claim.
	if _, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRenameCmd(&invocation{projectsRoot: projects}) }, "a", "b"); err == nil || !strings.Contains(err.Error(), "--model is required") {
		t.Fatalf("rename without --model = %v", err)
	}
}

func TestCwWtWorktreeRenameRealTaskInProcess(t *testing.T) {
	projects, _, _ := initGCFixture(t)
	// The fixture worktree is dirty, so the dry-run plans a skip whose reason
	// is printed; either way the text renderer runs.
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRenameCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "gc-cli-renamed", "--model", "unknown")
	if err != nil && exitCodeOf(t, err) != exitFindings {
		t.Fatalf("rename dry run of a real task: %v", err)
	}
	if !strings.Contains(stdout, "gc-cli") {
		t.Fatalf("rename dry-run stdout = %q", stdout)
	}
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRenameCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "gc-cli-renamed", "--model", "unknown", "--format", "json"); err != nil {
		t.Fatalf("rename json of a real task: %v", err)
	}
}

func TestCwWtWorktreeRelocateInProcess(t *testing.T) {
	projects := cwCovProjectsRoot(t, "acme/app")
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	_, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRelocateCmd(&invocation{projectsRoot: projects}) }, "absent-task", "--to", "local")
	if err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("relocate of an absent task = %v", err)
	}
	// An invalid destination is refused before the backend.
	if _, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRelocateCmd(&invocation{projectsRoot: projects}) }, "absent-task", "--to", "elsewhere"); err == nil {
		t.Fatal("relocate with an invalid --to must fail")
	}
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRelocateCmd(&invocation{projectsRoot: projects}) }, "absent-task", "--to", "local", "--json", "--format", "text"); err == nil {
		t.Fatal("--json with a conflicting --format must be refused")
	}
}

func TestCwWtWorktreeRelocateRealTaskInProcess(t *testing.T) {
	projects, _, worktree := initGCFixture(t)
	// The fixture task starts dirty, and relocate refuses a dirty checkout, so
	// clean it: then the repository-local layout reports it as already there.
	if err := os.Remove(filepath.Join(worktree, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRelocateCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--to", "local")
	if err != nil {
		t.Fatalf("relocate dry run of a real task: %v", err)
	}
	if !strings.Contains(stdout, "already there") && !strings.Contains(stdout, "would relocate") {
		t.Fatalf("relocate dry-run stdout = %q", stdout)
	}
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRelocateCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--to", "local", "--json")
	if err != nil {
		t.Fatalf("relocate json of a real task: %v", err)
	}
	if !strings.Contains(stdout, "schema_version") {
		t.Fatalf("relocate json stdout = %q", stdout)
	}
}

func TestCwWtWorktreeGuardAndCheckpointFetchInProcess(t *testing.T) {
	seed := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seed, "app", clone)
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeGuardCmd(&invocation{projectsRoot: projects}) }, clone)
	if err != nil {
		t.Fatalf("guard on a clean canonical clone: %v", err)
	}
	if !strings.Contains(stdout, "ok: ") {
		t.Fatalf("guard stdout = %q", stdout)
	}
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeGuardCmd(&invocation{projectsRoot: projects}) }, clone, "--format", "json")
	if err != nil {
		t.Fatalf("guard json: %v", err)
	}
	if !strings.Contains(stdout, "\"kind\"") {
		t.Fatalf("guard json stdout = %q", stdout)
	}
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeGuardCmd(&invocation{projectsRoot: projects}) }, clone, "--quiet")
	if err != nil {
		t.Fatalf("guard --quiet: %v", err)
	}
	if stdout != "" {
		t.Fatalf("guard --quiet stdout = %q", stdout)
	}
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeGuardCmd(&invocation{projectsRoot: projects}) }, clone, "--admission", "bogus"); err == nil || !strings.Contains(err.Error(), "unsupported admission mode") {
		t.Fatalf("guard bad admission = %v", err)
	}
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeGuardCmd(&invocation{projectsRoot: projects}) }, clone, "--published"); err != nil {
		// A branch pushed to this local bare origin is verified; either way
		// the code path through PublicationFinding must not crash.
		if !strings.Contains(err.Error(), "not verified as published") {
			t.Fatalf("guard --published error = %v", err)
		}
	}

	// checkpoint-fetch needs a --task and an explicit format.
	if _, _, err := cwCovExec(t, projects, newWorktreeCheckpointFetchCmd, clone); err == nil || !strings.Contains(err.Error(), "--task is required") {
		t.Fatalf("checkpoint-fetch without --task = %v", err)
	}
	_, _, err = cwCovExec(t, projects, newWorktreeCheckpointFetchCmd, clone, "--task", "t")
	if err == nil {
		t.Fatal("checkpoint-fetch for an absent ref must fail")
	}
}

func TestCwWtWorktreeSetAndLogVerbsInProcess(t *testing.T) {
	projects := t.TempDir()
	checkout := cwWtGitRepo(t, filepath.Join(projects, "acme", "app"))
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	// set requires a prompt source and refuses an empty one.
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeSetCmd(&invocation{}) }, checkout); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("set without a source = %v", err)
	}
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeSetCmd(&invocation{}) }, checkout, "--prompt", "   "); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("set with a blank prompt = %v", err)
	}

	// log show on a path that does not exist reports the backend error.
	_, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "show", filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("log show on a missing path must fail")
	}
	// log show on a real checkout with no journal is a valid, empty read.
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "show", checkout); err != nil {
		t.Fatalf("log show on a clean checkout: %v", err)
	}

	// log init on a plain git checkout is the first step that can succeed.
	// The admission flags live on the parent `worktree log` command, so the
	// whole subcommand tree is exercised.
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "init", checkout, "--format", "json")
	if err != nil {
		t.Fatalf("log init: %v", err)
	}
	if !strings.Contains(stdout, "\"verb\"") {
		t.Fatalf("log init stdout = %q", stdout)
	}
}

func TestCwWtWorktreeErrorPropagationFromBackend(t *testing.T) {
	// A projects root beneath a regular file cannot be resolved, which drives
	// the "backend failed" branches without any fixture.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	unreadableRoot := filepath.Join(blocker, "projects")
	builders := []struct {
		name  string
		build func() *cobra.Command
		args  []string
	}{
		{name: "list", build: func() *cobra.Command { return newWorktreeListCmd(&invocation{}) }},
		{name: "summary", build: func() *cobra.Command { return newWorktreeSummaryCmd(&invocation{}) }, args: []string{"t"}},
		{name: "backfill", build: func() *cobra.Command { return newWorktreeBackfillCmd(&invocation{}) }},
		{name: "orphans", build: func() *cobra.Command { return newWorktreeOrphansCmd(&invocation{}) }},
		{name: "gc", build: func() *cobra.Command { return newWorktreeGCCmd(&invocation{}) }},
		{name: "cleanup", build: func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{}) }, args: []string{"t"}},
		{name: "rename", build: func() *cobra.Command { return newWorktreeRenameCmd(&invocation{}) }, args: []string{"a", "b"}},
		{name: "relocate", build: func() *cobra.Command { return newWorktreeRelocateCmd(&invocation{}) }, args: []string{"t"}},
		{name: "adopt", build: func() *cobra.Command { return newWorktreeAdoptCmd(&invocation{}) }, args: []string{"--all-external"}},
	}
	for _, builder := range builders {
		if _, _, err := cwCovExec(t, unreadableRoot, builder.build, builder.args...); err == nil {
			t.Errorf("%s against an unreadable projects root returned no error", builder.name)
		}
	}
}
