package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

// A landing whose merge succeeded but whose cleanup failed exits with its own
// status and names the exact resume command (sneat-dev/wb#824): exit 1 reads as
// "not landed", which sends a caller back to re-land finished work.
func TestWorktreeLandExitsDistinctlyWhenTheMergeLandedButTheTailDidNot(t *testing.T) {
	t.Parallel()
	cause := errors.New("cleanup task landed: remote branch still exists")
	for _, test := range []struct {
		name        string
		status      orchestrate.WorktreeMergeStatus
		err         error
		wantCode    int
		wantResume  string
		wantPlainOf error
	}{
		{name: "cleanup pending", status: orchestrate.WorktreeMergeLanded, err: cause, wantCode: exitLandedIncomplete, wantResume: "wb worktree merge resume /r/receipt.json --progress"},
		{name: "canonical sync blocked", status: orchestrate.WorktreeMergeCanonicalSyncBlocked, err: cause, wantCode: exitLandedIncomplete, wantResume: "wb worktree merge resume /r/receipt.json --progress"},
		{name: "post-target CI failed stays a finding", status: orchestrate.WorktreeMergePostTargetCIFailed, err: cause, wantPlainOf: cause},
		{name: "not landed stays a finding", status: orchestrate.WorktreeMergeValidationFailed, err: cause, wantPlainOf: cause},
		{name: "success", status: orchestrate.WorktreeMergeComplete},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			receipt := orchestrate.WorktreeMergeReceipt{
				Status: test.status, ReceiptPath: "/r/receipt.json",
				ResumeArgs: []string{"worktree", "merge", "resume", "/r/receipt.json", "--progress"},
			}
			got := landedIncompleteExit(receipt, test.err)
			if test.wantPlainOf != nil || test.err == nil {
				if got != test.err {
					t.Fatalf("landedIncompleteExit = %v, want the error unchanged (%v)", got, test.err)
				}
				return
			}
			if code := exitCodeFor(got, true); code != test.wantCode {
				t.Fatalf("exit code = %d, want %d (%v)", code, test.wantCode, got)
			}
			if !strings.Contains(got.Error(), "landed") || !strings.Contains(got.Error(), "resume with: "+test.wantResume) || !strings.Contains(got.Error(), cause.Error()) {
				t.Fatalf("error does not say the change landed and name the resume command: %v", got)
			}
		})
	}
}

func TestWorktreeLandResumeCommandFallsBackToTheReceiptPath(t *testing.T) {
	t.Parallel()
	receipt := orchestrate.WorktreeMergeReceipt{Status: orchestrate.WorktreeMergeLanded, ReceiptPath: "/r/receipt.json"}
	got := landedIncompleteExit(receipt, errors.New("boom"))
	if !strings.Contains(got.Error(), "resume with: wb worktree merge resume /r/receipt.json") {
		t.Fatalf("no fallback resume command: %v", got)
	}
}

func TestPullRequestLandExitsDistinctlyWhenTheMergeLandedButTheTailDidNot(t *testing.T) {
	t.Parallel()
	result := orchestrate.PullRequestLandResult{
		Outcome: orchestrate.LandLandedIncomplete, RefusalCode: "branch-retirement-failed",
		Reason: "landed on main (abc) but the follow-up did not finish: boom", ResumeCommand: "wb pr land acme/app#7",
	}
	if result.ExitCode() != exitLandedIncomplete {
		t.Fatalf("exit = %d, want %d", result.ExitCode(), exitLandedIncomplete)
	}
	command, out := newOutputCapturingCommand()
	if err := printPullRequestLand(command, result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"landed-incomplete: landed on main", "refusal: branch-retirement-failed", "resume with: wb pr land acme/app#7"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRootHelpDocumentsTheLandedIncompleteExitCode(t *testing.T) {
	t.Parallel()
	var stdout, stderr strings.Builder
	if code := run([]string{"--help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "3  landed, follow-up incomplete") {
		t.Fatalf("root help does not document exit 3:\n%s", stdout.String())
	}
}

// A landed-incomplete result is what the verbs map to exit 3 with the resume
// command in the error, in text and JSON alike.
func TestPullRequestLandAndCreateLandMapALandedIncompleteResultToExitThree(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	landed := orchestrate.PullRequestLandResult{
		Repository: "acme/app", PullRequest: 9, Outcome: orchestrate.LandLandedIncomplete,
		RefusalCode: "branch-retirement-failed", Reason: "landed on main but the follow-up did not finish: boom",
		ResumeCommand: "wb pr land acme/app#9",
	}
	previousLand, previousCreate := landPullRequest, createPullRequest
	t.Cleanup(func() { landPullRequest, createPullRequest = previousLand, previousCreate })
	landPullRequest = func(context.Context, orchestrate.PullRequestLandOptions) (orchestrate.PullRequestLandResult, error) {
		return landed, nil
	}
	createPullRequest = func(context.Context, orchestrate.PullRequestCreateOptions) (orchestrate.PullRequestCreateResult, error) {
		return orchestrate.PullRequestCreateResult{Outcome: orchestrate.CreateLandedIncomplete, LandResult: &landed, Reason: landed.Reason}, nil
	}
	projects := t.TempDir()
	for _, test := range []struct {
		name string
		args []string
	}{
		{"pr land text", []string{"pr", "land", "acme/app#9", "--non-interactive"}},
		{"pr land json", []string{"pr", "land", "acme/app#9", "--non-interactive", "--format", "json"}},
		{"pr create --land", []string{"pr", "create", "--land"}},
	} {
		var stdout, stderr strings.Builder
		code := run(append([]string{"--projects-root", projects}, test.args...), &stdout, &stderr)
		if code != exitLandedIncomplete {
			t.Errorf("%s: exit = %d, want %d\nstdout=%s\nstderr=%s", test.name, code, exitLandedIncomplete, stdout.String(), stderr.String())
		}
		if !strings.Contains(stderr.String(), "resume with: wb pr land acme/app#9") {
			t.Errorf("%s: the resume command is not on stderr: %q", test.name, stderr.String())
		}
	}
}

// Every write of the landed-incomplete report is checked: a closed stdout
// surfaces as an error at whichever line it fails.
func TestPrintPullRequestLandSurfacesAFailedWriteOfTheResumeLine(t *testing.T) {
	t.Parallel()
	result := orchestrate.PullRequestLandResult{
		Outcome: orchestrate.LandLandedIncomplete, RefusalCode: "branch-retirement-failed", Reason: "boom",
		SanctionedCommand: "wb worktree guard .", ResumeCommand: "wb pr land acme/app#9",
	}
	for failAt := 1; failAt <= 5; failAt++ {
		if err := printPullRequestLand(newFailingCommand(failAt), result); err == nil {
			t.Errorf("a write failing at call %d was swallowed", failAt)
		}
	}
	if err := printPullRequestLand(newFailingCommand(6), result); err != nil {
		t.Errorf("only five lines are written: %v", err)
	}
}
