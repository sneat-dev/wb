package worktrees

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCleanupMemberApplyPhaseFaultMatrix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		fault string
		want  string
	}{
		{fault: "success"},
		{fault: "open", want: "denied"},
		{fault: "recheck", want: "denied"},
		{fault: "canonical open", want: "open cleanup canonical repository /repo: denied"},
		{fault: "canonical validate", want: "cleanup canonical repository changed before Git operations: denied"},
		{fault: "validate 1", want: "denied"},
		{fault: "work log recovery success"},
		{fault: "work log recovery fail", want: "recover legacy Work Log repository relocation before removing /worktree: denied"},
		{fault: "seal cleanup", want: "seal work log before removing /worktree: denied"},
		{fault: "seal supersession success"},
		{fault: "seal supersession fail", want: "seal work log before removing /worktree: denied"},
		{fault: "persist sealed", want: "denied"},
		{fault: "persist retiring_remote", want: "denied"},
		{fault: "validate 2", want: "denied"},
		{fault: "validate 3", want: "denied"},
		{fault: "recovered 1", want: "denied"},
		{fault: "remote push", want: "delete remote branch topic at abc: denied"},
		{fault: "persist remote_retired", want: "denied"},
		{fault: "validate 4", want: "denied"},
		{fault: "recovered 2", want: "denied"},
		{fault: "persist removing_worktree", want: "denied"},
		{fault: "remove residue inspect", want: "inspect its registration afterwards: denied"},
		{fault: "remove no residue", want: "remove worktree /worktree: denied"},
		{fault: "remove before residue", want: "denied"},
		{fault: "remove repair", want: "remove worktree /worktree: denied; denied"},
		{fault: "remove repaired"},
		{fault: "persist worktree_removed", want: "denied"},
		{fault: "after removal", want: "after worktree removal for acme/app: denied"},
		{fault: "task validate", want: "denied"},
		{fault: "recovered 3", want: "denied"},
		{fault: "persist removing_local_branch", want: "denied"},
		{fault: "local delete", want: "delete local branch topic at abc: denied"},
		{fault: "parent", want: "denied"},
		{fault: "external bad identity", want: "resolve adopted worktree registration identity"},
		{fault: "external registration", want: "denied"},
		{fault: "external success"},
		{fault: "detached success"},
		{fault: "persist complete", want: "denied"},
		{fault: "no remote success"},
	} {
		t.Run(tc.fault, func(t *testing.T) {
			t.Parallel()
			denied := errors.New("denied")
			refreshed := ListResult{
				Task: "task", Repository: "acme/app", CanonicalDir: "/repo",
				WorktreeDir: "/worktree", Branch: "topic", Base: "main",
				HeadSHA: "abc", RemoteHeadSHA: "abc",
			}
			if strings.HasPrefix(tc.fault, "seal supersession") {
				refreshed.SupersededAtOrigin = true
				refreshed.supersessionReceipt = &SupersessionReceipt{}
			}
			if strings.HasPrefix(tc.fault, "external") {
				refreshed.External = true
			}
			if tc.fault == "external bad identity" {
				refreshed.Repository = "bad"
			}
			if tc.fault == "detached success" {
				refreshed.Branch = ""
				refreshed.RemoteHeadSHA = ""
			}
			run := &cleanupRun{
				ctx: context.Background(),
				normalized: CleanupOptions{
					DeleteRemote:                        tc.fault != "no remote success",
					beforeCleanupWorktreeRemoval:        func(string) {},
					beforeCleanupNetworkBranchOperation: func(string) {},
					afterCleanupGitAuthorization:        func(string) {},
					beforeCleanupResidueRemoval: func(string) error {
						if tc.fault == "remove before residue" {
							return denied
						}
						return nil
					},
					afterCleanupWorktreeRemoval: func(string) error {
						if tc.fault == "after removal" {
							return denied
						}
						return nil
					},
				},
				outcome: CleanupOutcome{Results: []CleanupResult{{ListResult: refreshed}}},
			}
			var stages []string
			var gitCalls int
			var validates, recovered int
			ports := cleanupApplyMemberPorts{
				OpenWorktree: func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error) {
					if tc.fault == "open" {
						return nil, denied
					}
					return &cleanupWorktreeHandle{}, nil
				},
				CloseWorktree: func(*cleanupWorktreeHandle) {},
				Recheck: func(context.Context, cleanupMemberRecheck, *cleanupWorktreeHandle) (ListResult, error) {
					if tc.fault == "recheck" {
						return ListResult{}, denied
					}
					return refreshed, nil
				},
				OpenCanonical: func(string) (*canonicalRepository, error) {
					if tc.fault == "canonical open" {
						return nil, denied
					}
					return &canonicalRepository{}, nil
				},
				CloseCanonical: func(*canonicalRepository) {},
				ValidateCanonical: func(*canonicalRepository) error {
					if tc.fault == "canonical validate" {
						return denied
					}
					return nil
				},
				ValidateWorktree: func(*cleanupWorktreeHandle) error {
					validates++
					if tc.fault == fmt.Sprintf("validate %d", validates) {
						return denied
					}
					return nil
				},
				PreflightWorkLog: func(string, string, string) error {
					if strings.HasPrefix(tc.fault, "work log recovery") {
						return denied
					}
					return nil
				},
				RecoverLegacy: func(context.Context, string, string, ListResult, func() error) error {
					if tc.fault == "work log recovery fail" {
						return denied
					}
					return nil
				},
				SealSupersession: func(string, string, string, *SupersessionReceipt) error {
					if tc.fault == "seal supersession fail" {
						return denied
					}
					return nil
				},
				SealCleanup: func(string, string, string) error {
					if tc.fault == "seal cleanup" {
						return denied
					}
					return nil
				},
				NewBacklog: func(string, ListResult, string) lifecycleBacklogRecord { return lifecycleBacklogRecord{ID: "backlog"} },
				Persist: func(_ string, _ *lifecycleBacklogRecord, stage string) error {
					stages = append(stages, stage)
					if tc.fault == "persist "+stage {
						return denied
					}
					return nil
				},
				ValidateRecovered: func(bool, *cleanupTaskHandle) error {
					recovered++
					if tc.fault == fmt.Sprintf("recovered %d", recovered) {
						return denied
					}
					return nil
				},
				WorktreeGit: func(_ context.Context, _ *canonicalRepository, _ *cleanupWorktreeHandle, _ string, args ...string) error {
					gitCalls++
					if args[0] == "push" && tc.fault == "remote push" {
						return denied
					}
					if args[0] == "worktree" && strings.HasPrefix(tc.fault, "remove ") {
						return denied
					}
					return nil
				},
				CanonicalGit: func(context.Context, *canonicalRepository, ...string) error {
					gitCalls++
					if tc.fault == "local delete" {
						return denied
					}
					return nil
				},
				Residue: func(context.Context, *canonicalRepository, string) (bool, error) {
					if tc.fault == "remove residue inspect" {
						return false, denied
					}
					return tc.fault != "remove no residue", nil
				},
				RemoveResidue: func(*cleanupWorktreeHandle, string) (bool, error) {
					if tc.fault == "remove repair" {
						return false, denied
					}
					return true, nil
				},
				ValidateTask: func(*cleanupTaskHandle) error {
					if tc.fault == "task validate" {
						return denied
					}
					return nil
				},
				RemoveParent: func(*cleanupWorktreeHandle, func(string), func(string)) error {
					if tc.fault == "parent" {
						return denied
					}
					return nil
				},
				RemoveAdopted: func(*cleanupTaskHandle, string, string) error {
					if tc.fault == "external registration" {
						return denied
					}
					return nil
				},
			}
			pending := 0
			err := run.applyCleanupMemberWithPorts(&cleanupTaskHandle{}, 0, nil, false, &pending, ports)
			if tc.want == "" {
				if err != nil || !run.outcome.Results[0].Applied || pending != 0 {
					t.Fatalf("apply err=%v pending=%d result=%#v", err, pending, run.outcome.Results[0])
				}
				if len(stages) == 0 || stages[0] != lifecycleStageSealed || stages[len(stages)-1] != lifecycleStageComplete {
					t.Fatalf("durable phase order = %q", stages)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("apply err=%v, want %q; stages=%q", err, tc.want, stages)
			}
			if len(stages) == 0 && gitCalls != 0 {
				t.Fatalf("Git ran before sealed backlog: %d calls", gitCalls)
			}
		})
	}
}
