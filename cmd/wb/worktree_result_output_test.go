package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

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
