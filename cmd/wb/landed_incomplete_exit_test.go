package main

import (
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

// Every write of the landed-incomplete report is checked: a closed stdout
// surfaces as an error at whichever line it fails.
