package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

type rejectedWorktreeOutput struct{}

func (rejectedWorktreeOutput) Write([]byte) (int, error) { return 0, errors.New("output closed") }

func TestWorktreeRelocateReportsEveryPlannedDisposition(t *testing.T) {
	previous := relocateWorktrees
	t.Cleanup(func() { relocateWorktrees = previous })
	var requested worktrees.RelocateOptions
	relocateWorktrees = func(_ context.Context, options worktrees.RelocateOptions) (worktrees.RelocateOutcome, error) {
		requested = options
		return worktrees.RelocateOutcome{Results: []worktrees.RelocateResult{
			{Task: "review", Repository: "acme/first", Destination: "/local/first", AlreadyThere: true},
			{Task: "review", Repository: "acme/second", Destination: "/local/second", Eligible: true},
			{Task: "review", Repository: "acme/third", Reason: "external checkout"},
		}}, nil
	}
	command := newWorktreeRelocateCmd(&invocation{projectsRoot: t.TempDir()})
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"review", "--to=local"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if requested.Task != "review" || requested.To != "local" || requested.Apply {
		t.Fatalf("relocate options = %+v", requested)
	}
	for _, line := range []string{
		"already there review acme/first /local/first",
		"would relocate review acme/second -> /local/second",
		"skip review acme/third: external checkout",
		"1 eligible; dry-run only, pass --apply to relocate",
	} {
		if !strings.Contains(stdout.String(), line) {
			t.Errorf("stdout %q does not contain %q", stdout.String(), line)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected diagnostic: %s", stderr.String())
	}
}

func TestWorktreeRelocateApplyReportsMoveAndRefusesUnfulfilledPlan(t *testing.T) {
	previous := relocateWorktrees
	t.Cleanup(func() { relocateWorktrees = previous })
	for _, tc := range []struct {
		name    string
		results []worktrees.RelocateResult
		want    string
		wantErr string
	}{
		{name: "moved", results: []worktrees.RelocateResult{{Task: "review", Repository: "acme/app", Destination: "/local/app", Applied: true}}, want: "relocated review acme/app -> /local/app\n1 relocated\n"},
		{name: "no move", results: []worktrees.RelocateResult{{Task: "review", Repository: "acme/app", Destination: "/local/app", Eligible: true}}, want: "would relocate review acme/app -> /local/app\n", wantErr: "no planned worktree was relocated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relocateWorktrees = func(_ context.Context, options worktrees.RelocateOptions) (worktrees.RelocateOutcome, error) {
				if !options.Apply {
					return worktrees.RelocateOutcome{}, fmt.Errorf("apply was not forwarded")
				}
				return worktrees.RelocateOutcome{Results: tc.results}, nil
			}
			command := newWorktreeRelocateCmd(&invocation{projectsRoot: t.TempDir()})
			command.SilenceUsage = true
			command.SilenceErrors = true
			var stdout bytes.Buffer
			command.SetOut(&stdout)
			command.SetArgs([]string{"review", "--to=local", "--apply", "--mode=manual", "--initiator=test"})
			err := command.Execute()
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			if stdout.String() != tc.want {
				t.Fatalf("stdout = %q, want %q", stdout.String(), tc.want)
			}
		})
	}
}

func TestWorktreeRelocatePropagatesOutputFailure(t *testing.T) {
	previous := relocateWorktrees
	t.Cleanup(func() { relocateWorktrees = previous })
	for _, result := range []worktrees.RelocateResult{
		{Task: "review", Repository: "acme/app", Applied: true},
		{Task: "review", Repository: "acme/app", AlreadyThere: true},
		{Task: "review", Repository: "acme/app", Eligible: true},
		{Task: "review", Repository: "acme/app", Reason: "held"},
	} {
		relocateWorktrees = func(context.Context, worktrees.RelocateOptions) (worktrees.RelocateOutcome, error) {
			return worktrees.RelocateOutcome{Results: []worktrees.RelocateResult{result}}, nil
		}
		command := newWorktreeRelocateCmd(&invocation{projectsRoot: t.TempDir()})
		command.SetOut(rejectedWorktreeOutput{})
		command.SetArgs([]string{"review", "--to=local"})
		if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "output closed") {
			t.Fatalf("result %+v error = %v", result, err)
		}
	}
}

func TestWorktreeRecoverStagesReportsPlansAndArchives(t *testing.T) {
	previous := recoverRetiredStages
	t.Cleanup(func() { recoverRetiredStages = previous })
	var tasks []string
	recoverRetiredStages = func(_ context.Context, options worktrees.RetiredStageRecoveryOptions) (worktrees.RetiredStageRecoveryOutcome, error) {
		tasks = append(tasks, options.Task)
		if options.Task == "second" {
			return worktrees.RetiredStageRecoveryOutcome{Apply: options.Apply, ReceiptPath: "/receipts/second.json", Results: []worktrees.RetiredStageRecoveryResult{
				{Path: "/stages/eligible", Eligible: true, Applied: options.Apply, Reason: "verified"},
				{Path: "/stages/blocked", Reason: "changed"},
			}}, nil
		}
		return worktrees.RetiredStageRecoveryOutcome{}, nil
	}
	for _, tc := range []struct {
		name  string
		apply bool
		state string
	}{
		{"plan", false, "would archive"}, {"apply", true, "archived"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks = nil
			command := newWorktreeCleanupCmd(&invocation{projectsRoot: t.TempDir()})
			var stdout bytes.Buffer
			command.SetOut(&stdout)
			args := []string{"--recover-stages", "first", "second"}
			if tc.apply {
				args = append(args, "--apply")
			}
			command.SetArgs(args)
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if strings.Join(tasks, ",") != "first,second" {
				t.Fatalf("tasks = %v", tasks)
			}
			for _, want := range []string{"receipt: /receipts/second.json", tc.state + " ", "/stages/eligible: verified", "preserved ", "/stages/blocked: changed"} {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout %q does not contain %q", stdout.String(), want)
				}
			}
		})
	}
}
