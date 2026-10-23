package main

import (
	"bytes"
	"context"
	"io"
	"slices"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// stubCleanupEngine replaces the cleanup engine for one test and returns the
// options every invocation handed it.
//
// A stubbed engine must not leave the command's real side effects in place:
// after an apply the command releases each task's claim in the fleet's shared
// store. The stub therefore replaces that release too and returns the tasks it
// was asked to release, so a test of the command can never write to a real
// claim store, whatever task names it uses.
func stubCleanupEngine(t *testing.T, outcome worktrees.CleanupOutcome) (requested *[]worktrees.CleanupOptions, released *[]string) {
	t.Helper()
	previousEngine, previousRelease := cleanupWorktreeTasks, releaseRemoteClaim
	t.Cleanup(func() { cleanupWorktreeTasks, releaseRemoteClaim = previousEngine, previousRelease })
	requested, released = &[]worktrees.CleanupOptions{}, &[]string{}
	cleanupWorktreeTasks = func(_ context.Context, options worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
		*requested = append(*requested, options)
		return outcome, nil
	}
	releaseRemoteClaim = func(_, task string, _ io.Writer) autoReleaseResult {
		*released = append(*released, task)
		return autoReleaseResult{Outcome: "released"}
	}
	return requested, released
}

// The exact invocation that released a real claim on 2026-10-02: a stubbed
// engine reporting an applied named task. The release now goes to the stub.
//
//nolint:paralleltest // swaps the package-level cleanup engine and claim release.
func TestStubbedWorktreeCleanupApplyReleasesClaimsOnlyThroughTheStub(t *testing.T) {
	requested, released := stubCleanupEngine(t, worktrees.CleanupOutcome{Results: []worktrees.CleanupResult{
		{ListResult: worktrees.ListResult{Task: "fixture-landed-task", Repository: "acme/app"}, Eligible: true, Applied: true, RemoteDeleted: true},
	}})
	command := newWorktreeCleanupCmd(&invocation{projectsRoot: t.TempDir()})
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"fixture-landed-task", "fixture-skipped-task", "--apply", "--remote"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(*requested) != 1 || !(*requested)[0].Apply || !(*requested)[0].DeleteRemote {
		t.Fatalf("cleanup options = %#v, want one apply with remote retirement", *requested)
	}
	// Only the task the engine actually retired has its claim released.
	if !slices.Equal(*released, []string{"fixture-landed-task"}) {
		t.Fatalf("released claims = %v, want only the applied task", *released)
	}
}

// A base the operator names is the target the head is judged against; a base
// left at its default is only the fallback for a worktree with no recorded
// one. Reported 2026-10-02: `wb worktree cleanup <task> --base main` answered
// with the base recorded in the task's claim.
//
//nolint:paralleltest // swaps the package-level cleanup engine and claim release.
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
			requested, released := stubCleanupEngine(t, worktrees.CleanupOutcome{})
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
			if len(*released) != 0 {
				t.Fatalf("a dry run released claims: %v", *released)
			}
		})
	}
}

// The help promises that the plan names the target that proved the work. With
// --base that is the branch the operator named, on every candidate of a sweep.
//
//nolint:paralleltest // swaps the package-level cleanup engine and claim release.
func TestWorktreeCleanupSweepWithANamedBasePrintsThatTargetOnEveryLine(t *testing.T) {
	const proof = "contained in origin/release at 0123456789ab, the base named with --base"
	requested, _ := stubCleanupEngine(t, worktrees.CleanupOutcome{Results: []worktrees.CleanupResult{
		{ListResult: worktrees.ListResult{Task: "fixture-one", Repository: "acme/app", IntegrationProof: proof}, Eligible: true},
		{ListResult: worktrees.ListResult{Task: "fixture-two", Repository: "acme/lib", IntegrationProof: proof + " (recorded base integration)"}, Eligible: true},
	}})
	command := newWorktreeCleanupCmd(&invocation{projectsRoot: t.TempDir()})
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"--all-merged", "--base", "release"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(*requested) != 1 || !(*requested)[0].AllMerged || !(*requested)[0].ExplicitBase || (*requested)[0].Base != "release" {
		t.Fatalf("cleanup options = %#v", *requested)
	}
	want := "would remove fixture-one acme/app (" + proof + ")\n" +
		"would remove fixture-two acme/lib (" + proof + " (recorded base integration))\n" +
		"2 eligible; dry-run only, pass --apply to remove\n"
	if stdout.String() != want {
		t.Fatalf("sweep output = %q, want %q", stdout.String(), want)
	}
}

// `wb worktree end` releases the fleet-wide claim through the same seam as
// every other command, so stubbing the seam covers it too.
//
//nolint:paralleltest // swaps the package-level claim release.
func TestWorktreeEndReleasesTheClaimThroughTheSharedSeam(t *testing.T) {
	_, released := stubCleanupEngine(t, worktrees.CleanupOutcome{})
	root := t.TempDir()
	engine, err := worktreeEndEngine(root, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	message := engine.Claims.Release(root, "fixture-ended-task")
	if !slices.Equal(*released, []string{"fixture-ended-task"}) || message != "released through the remote-claim path" {
		t.Fatalf("released = %v, message = %q", *released, message)
	}
}
