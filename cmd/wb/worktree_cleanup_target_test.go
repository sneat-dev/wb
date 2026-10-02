package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// stubCleanupEngine replaces the cleanup engine for one test and returns the
// options every invocation handed it.
//
// It also points XDG_CONFIG_HOME at an empty directory. The cleanup command
// releases a task's remote claim after an apply, through the remote configured
// in the operator's own wb.yaml; a test must never reach a real state store,
// whatever task names it uses.
func stubCleanupEngine(t *testing.T, outcome worktrees.CleanupOutcome) *[]worktrees.CleanupOptions {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	previous := cleanupWorktreeTasks
	t.Cleanup(func() { cleanupWorktreeTasks = previous })
	requested := &[]worktrees.CleanupOptions{}
	cleanupWorktreeTasks = func(_ context.Context, options worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
		*requested = append(*requested, options)
		return outcome, nil
	}
	return requested
}

// A base the operator names is the target the head is judged against; a base
// left at its default is only the fallback for a worktree with no recorded
// one. Reported 2026-10-02: `wb worktree cleanup <task> --base main` answered
// with the base recorded in the task's claim.
//
//nolint:paralleltest // swaps the package-level cleanup engine and sets the environment.
func TestWorktreeCleanupTreatsOnlyANamedBaseAsTheExplicitTarget(t *testing.T) {
	for _, test := range []struct {
		name     string
		args     []string
		base     string
		explicit bool
	}{
		{name: "default", args: []string{"fixture-stacked-task"}, base: "main"},
		{name: "named", args: []string{"fixture-stacked-task", "--base", "main"}, base: "main", explicit: true},
		{name: "named with equals", args: []string{"--base=release", "fixture-stacked-task"}, base: "release", explicit: true},
		{name: "fleet sweep", args: []string{"--all-merged", "--base", "main"}, base: "main", explicit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requested := stubCleanupEngine(t, worktrees.CleanupOutcome{})
			command := newWorktreeCleanupCmd(&invocation{projectsRoot: t.TempDir()})
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			command.SetArgs(test.args)
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if len(*requested) != 1 || (*requested)[0].Apply || (*requested)[0].Base != test.base || (*requested)[0].ExplicitBase != test.explicit {
				t.Fatalf("cleanup options = %#v, want a dry run with base %q explicit %t", *requested, test.base, test.explicit)
			}
		})
	}
}

func TestWorktreeCleanupReportNamesTheTargetThatProvedTheWork(t *testing.T) {
	t.Parallel()
	const proof = "contained in origin/main at 0123456789ab, via recorded base integration (absent)"
	results := func(applied bool) []worktrees.CleanupResult {
		return []worktrees.CleanupResult{
			{ListResult: worktrees.ListResult{Task: "fixture-integration-task", Repository: "acme/app", IntegrationProof: proof},
				Eligible: true, Applied: applied, RemoteDeleted: applied},
			{ListResult: worktrees.ListResult{Task: "fixture-plain-task", Repository: "acme/app"},
				Eligible: true, Applied: applied, RemoteDeleted: applied},
		}
	}
	for _, test := range []struct {
		name    string
		applied bool
		want    string
	}{
		{name: "plan", want: "would remove fixture-integration-task acme/app (" + proof + ")\n" +
			"would remove fixture-plain-task acme/app\n" +
			"2 eligible; dry-run only, pass --apply to remove\n"},
		{name: "apply", applied: true, want: "removed fixture-integration-task acme/app and remote branch (" + proof + ")\n" +
			"removed fixture-plain-task acme/app and remote branch\n" +
			"2 removed\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			command := &cobra.Command{}
			command.SetOut(&stdout)
			if err := printWorktreeCleanup(command, results(test.applied), test.applied); err != nil {
				t.Fatal(err)
			}
			if stdout.String() != test.want {
				t.Fatalf("cleanup report = %q, want %q", stdout.String(), test.want)
			}
		})
	}
}
