package cmdworktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
	"strings"
	"testing"
)

// These are explicit operation/admission/marker/progress recorders, not native move proof.
func movementRecordedRelocate(root string, run func(context.Context, worktrees.RelocateOptions) (worktrees.RelocateOutcome, error)) *cobra.Command {
	command := NewRelocate(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }}, RelocateDependencies{Run: run, Admit: func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error) {
		return worktrees.AgentIdentity{}, func() {}, nil
	}, AfterApply: func(*cobra.Command, []worktrees.RelocateResult) {}, StartProgress: func(io.Writer, string) func() { return func() {} }})
	command.Flags().String("mode", "auto", "recorded admission mode")
	command.Flags().String("initiator", "", "recorded actor")
	return command
}

type movementRejectedWriter struct{}

func (movementRejectedWriter) Write([]byte) (int, error) { return 0, errors.New("output closed") }

func TestWorktreeRelocateReportsEveryPlannedDisposition(t *testing.T) {
	t.Parallel()
	var requested worktrees.RelocateOptions
	relocateWorktrees := func(_ context.Context, options worktrees.RelocateOptions) (worktrees.RelocateOutcome, error) {
		requested = options
		return worktrees.RelocateOutcome{Results: []worktrees.RelocateResult{
			{Task: "review", Repository: "acme/first", Destination: "/local/first", AlreadyThere: true},
			{Task: "review", Repository: "acme/second", Destination: "/local/second", Eligible: true},
			{Task: "review", Repository: "acme/third", Reason: "external checkout"},
		}}, nil
	}
	command := movementRecordedRelocate(t.TempDir(), relocateWorktrees)
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
	t.Parallel()
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
			t.Parallel()
			relocateWorktrees := func(_ context.Context, options worktrees.RelocateOptions) (worktrees.RelocateOutcome, error) {
				if !options.Apply {
					return worktrees.RelocateOutcome{}, fmt.Errorf("apply was not forwarded")
				}
				return worktrees.RelocateOutcome{Results: tc.results}, nil
			}
			command := movementRecordedRelocate(t.TempDir(), relocateWorktrees)
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
	t.Parallel()
	for _, result := range []worktrees.RelocateResult{
		{Task: "review", Repository: "acme/app", Applied: true},
		{Task: "review", Repository: "acme/app", AlreadyThere: true},
		{Task: "review", Repository: "acme/app", Eligible: true},
		{Task: "review", Repository: "acme/app", Reason: "held"},
	} {
		relocateWorktrees := func(context.Context, worktrees.RelocateOptions) (worktrees.RelocateOutcome, error) {
			return worktrees.RelocateOutcome{Results: []worktrees.RelocateResult{result}}, nil
		}
		command := movementRecordedRelocate(t.TempDir(), relocateWorktrees)
		command.SetOut(movementRejectedWriter{})
		command.SetArgs([]string{"review", "--to=local"})
		if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "output closed") {
			t.Fatalf("result %+v error = %v", result, err)
		}
	}
}
func TestMovementOriginalRenameRendering(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	rename := []worktrees.RenameResult{
		{OldTask: "old", Repository: "acme/a", NewWorktreeDir: "/tmp/new", NewBranch: "new", Applied: true, OldBranchDeleted: true, OldBranch: "old"},
		{OldTask: "old", Repository: "acme/b", NewWorktreeDir: "/tmp/new2", NewBranch: "new", Applied: true},
		{OldTask: "old", Repository: "acme/c", NewWorktreeDir: "/tmp/new3", Eligible: true},
		{OldTask: "old", Repository: "acme/d", Reason: "dirty"},
	}
	out.Reset()
	if err := writeRename(&out, rename, false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"renamed old acme/a -> /tmp/new (new) and deleted old branch old",
		"renamed old acme/b -> /tmp/new2 (new)\n",
		"would rename old acme/c -> /tmp/new3", "skip old acme/d: dirty",
		"1 eligible; dry-run only, pass --apply to rename",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rename output missing %q:\n%s", want, text)
		}
	}
	out.Reset()
	if err := writeRename(&out, rename, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2 renamed") {
		t.Fatalf("rename apply output = %q", out.String())
	}
	out.Reset()
	if err := writeRename(&out, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "no WB worktrees matched\n" {
		t.Fatalf("empty rename = %q", got)
	}

	for allow := 0; allow < 3; allow++ {
		if err := writeRename(&activeLimitedWriter{Allow: allow}, rename, false); err == nil {
			t.Fatalf("printWorktreeRename with %d writes allowed returned nil", allow)
		}
	}
}
func TestMovementOriginalRenameWriterSweep(t *testing.T) {
	t.Parallel()
	rename := []worktrees.RenameResult{
		{OldTask: "old", Repository: "acme/a", NewWorktreeDir: "/tmp/n", NewBranch: "new", Applied: true, OldBranchDeleted: true, OldBranch: "old"},
		{OldTask: "old", Repository: "acme/b", NewWorktreeDir: "/tmp/n2", NewBranch: "new", Applied: true},
		{OldTask: "old", Repository: "acme/c", NewWorktreeDir: "/tmp/n3", Eligible: true},
		{OldTask: "old", Repository: "acme/d", Reason: "dirty"},
	}
	activeSweepWrites(t, 8, func(writer *activeLimitedWriter) error {
		return writeRename(writer, rename, false)
	})

}
