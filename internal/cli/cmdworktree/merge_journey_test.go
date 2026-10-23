package cmdworktree

import (
	"bytes"
	"context"
	"errors"
	"github.com/spf13/cobra"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
)

func TestMergeTwoPhaseJourneysPreserveTypedOperationsAndErrorOrder(t *testing.T) {
	t.Parallel()
	for _, journey := range []string{"land alias", "prepare", "resume", "revert"} {
		for _, stage := range []string{"success", "writer", "engine", "linked"} {
			t.Run(journey+"/"+stage, func(t *testing.T) {
				t.Parallel()
				flags := shared.Flags{ProjectsRoot: "current/root", Quiet: true, NonInteractive: true}
				inv := mergeRecordingInvocation(&flags)
				refusal := errors.New("owned " + stage)
				receipt := orchestrate.WorktreeMergeReceipt{Repository: "owner/repo", ReceiptPath: "private/receipt", Status: orchestrate.WorktreeMergeComplete}
				var stages []string
				inv.bindings.RefusePaths = func(root string, paths []string) error {
					stages = append(stages, "linked")
					if stage == "linked" {
						return refusal
					}
					return nil
				}
				inv.bindings.RefuseReceipt = func(root, path string) error {
					stages = append(stages, "linked")
					if stage == "linked" {
						return refusal
					}
					return nil
				}
				inv.operations.PeekWorktreeMergeValidationDeferral = func(context.Context, string, []string, string, orchestrate.WorktreeMergeRoute, bool, bool, ...string) (bool, error) {
					return true, nil
				}
				inv.operations.PeekWorktreeMergeReceipt = func(string, string) (orchestrate.WorktreeMergeReceipt, error) { return receipt, nil }
				result := func(root string) (orchestrate.WorktreeMergeReceipt, error) {
					stages = append(stages, "engine")
					if root != "current/root" {
						t.Fatal(root)
					}
					if stage == "engine" {
						return receipt, refusal
					}
					return receipt, nil
				}
				inv.operations.RunWorktreeMerge = func(_ context.Context, p orchestrate.WorktreeMergePrepareOptions, l orchestrate.WorktreeMergeLandOptions) (orchestrate.WorktreeMergeReceipt, error) {
					return result(p.ProjectsRoot)
				}
				inv.operations.PrepareWorktreeMerge = func(_ context.Context, p orchestrate.WorktreeMergePrepareOptions) (orchestrate.WorktreeMergeReceipt, error) {
					if !p.ValidateLocally {
						t.Fatal("prepare lost local validation")
					}
					return result(p.ProjectsRoot)
				}
				inv.operations.ResumeWorktreeMerge = func(_ context.Context, p orchestrate.WorktreeMergeLandOptions) (orchestrate.WorktreeMergeReceipt, error) {
					if p.Receipt != "private/input" {
						t.Fatal(p.Receipt)
					}
					return result(p.ProjectsRoot)
				}
				inv.operations.PrepareWorktreeMergeRevert = func(_ context.Context, root, input string, _ time.Duration, _ int) (orchestrate.WorktreeMergeReceipt, error) {
					stages = append(stages, "revert")
					return result(root)
				}
				inv.operations.LandWorktreeMerge = func(_ context.Context, p orchestrate.WorktreeMergeLandOptions) (orchestrate.WorktreeMergeReceipt, error) {
					if p.Receipt != "private/receipt" {
						t.Fatal(p.Receipt)
					}
					return result(p.ProjectsRoot)
				}
				inv.bindings.ReleaseLane = func(string, orchestrate.WorktreeMergeReceipt) { stages = append(stages, "release") }
				var command *cobra.Command
				switch journey {
				case "land alias":
					command = newWorktreeLandCmd(inv)
				case "prepare":
					command = newWorktreeMergePrepareCmd(inv)
				case "resume":
					command = newWorktreeMergeLandCmd(inv, "resume")
				case "revert":
					command = newWorktreeMergeRevertCmd(inv)
				}
				var out bytes.Buffer
				command.SetOut(&out)
				command.SetErr(io.Discard)
				if stage == "writer" || stage == "engine" {
					command.SetOut(mergeRefusingWriter{refusal})
				}
				err := mergeExecute(command, "private/input")
				// Revert has no linked-path gate; all other commands refuse before effects.
				expectedError := stage == "writer" || stage == "engine" || (stage == "linked" && journey != "revert")
				if expectedError && err != refusal {
					t.Fatalf("error=%v want %v", err, refusal)
				}
				if !expectedError && (err != nil || out.Len() == 0) {
					t.Fatalf("err=%v output=%q", err, out.String())
				}
				if stage == "linked" && journey != "revert" && len(stages) != 1 {
					t.Fatal(stages)
				}
				if stage != "linked" && stages[len(stages)-1] != "release" {
					t.Fatal(stages)
				}
			})
		}
	}
}

func TestMergeReceiptKeepsFindingWriterFailuresAndDirectDeferral(t *testing.T) {
	t.Parallel()
	receipt := orchestrate.WorktreeMergeReceipt{Findings: []orchestrate.WorktreeMergeFinding{{Code: "owned", Message: "actual finding"}}}
	var out bytes.Buffer
	if err := writeWorktreeMergeReceipt(&out, "text", receipt); err != nil || !strings.Contains(out.String(), "finding: owned: actual finding") {
		t.Fatalf("err=%v output=%q", err, out.String())
	}
	if err := writeWorktreeMergeReceipt(&mergeOriginalFailWriter{Allow: 1}, "text", receipt); err == nil {
		t.Fatal("finding write refusal lost")
	}
	receipt.ValidationDeferral = &orchestrate.WorktreeMergeValidationDeferral{Route: orchestrate.WorktreeMergeRouteDirect}
	if hostLoadCheckSkippable(receipt, false, false, orchestrate.WorktreeMergeRouteAuto) {
		t.Fatal("incomplete direct fence must not be skippable")
	}
}

func TestMergeLandedIncompleteUsesTypedExitAndExactResume(t *testing.T) {
	t.Parallel()
	for _, resume := range [][]string{nil, {"worktree", "merge", "resume", "pinned"}} {
		t.Run(strings.Join(resume, " "), func(t *testing.T) {
			t.Parallel()
			flags := shared.Flags{}
			inv := mergeRecordingInvocation(&flags)
			receipt := orchestrate.WorktreeMergeReceipt{Status: orchestrate.WorktreeMergeLanded, Target: "main", ReceiptPath: "private/receipt", ResumeArgs: resume}
			cause := errors.New("tail failed")
			err := landedIncompleteExit(inv.runtime, receipt, cause)
			var coded *mergeCodedError
			if !errors.As(err, &coded) || coded.code != 3 || !strings.Contains(err.Error(), "tail failed") {
				t.Fatal(err)
			}
			want := "wb " + strings.Join(resume, " ")
			if len(resume) == 0 {
				want = "wb worktree merge resume private/receipt"
			}
			if !strings.HasSuffix(err.Error(), "resume with: "+want) {
				t.Fatal(err)
			}
		})
	}
}
