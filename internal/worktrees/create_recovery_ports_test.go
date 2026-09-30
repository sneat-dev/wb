package worktrees

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateRecoveryAttemptsReceiptBeforeRollbackAndPreservesExactOutcomes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name          string
		failStage     string
		failRollback  bool
		failSeal      bool
		wantSequence  string
		wantAction    string
		wantPersisted bool
		wantRolled    bool
	}{
		{name: "success", wantSequence: "receipt:removing_worktree,rollback,receipt:worktree_removed,seal,receipt:complete", wantAction: "rolled_back", wantPersisted: true, wantRolled: true},
		{name: "receipt unavailable still rolls back", failStage: lifecycleStageRemovingWorktree, wantSequence: "receipt:removing_worktree,rollback,seal", wantAction: "rolled_back", wantRolled: true},
		{name: "rollback unavailable retains receipt", failRollback: true, wantSequence: "receipt:removing_worktree,rollback", wantAction: "cleanup_required", wantPersisted: true},
		{name: "post-rollback receipt unavailable", failStage: lifecycleStageWorktreeRemoved, wantSequence: "receipt:removing_worktree,rollback,receipt:worktree_removed", wantAction: "rolled_back", wantPersisted: true, wantRolled: true},
		{name: "terminal seal unavailable", failSeal: true, wantSequence: "receipt:removing_worktree,rollback,receipt:worktree_removed,seal", wantAction: "rolled_back", wantPersisted: true, wantRolled: true},
		{name: "completion receipt unavailable", failStage: lifecycleStageComplete, wantSequence: "receipt:removing_worktree,rollback,receipt:worktree_removed,seal,receipt:complete", wantAction: "rolled_back", wantPersisted: true, wantRolled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			result := CreateResult{Repository: "acme/app", CanonicalDir: filepath.Join(root, "canonical"),
				WorktreeDir: filepath.Join(root, "store", "task", "acme", "app"), Branch: "feature", Base: "main"}
			attempt := createAttempt{plan: &createPlan{result: result, placement: worktreePlacement{Root: filepath.Join(root, "store")}},
				publication: &createdWorktreePublication{headSHA: strings.Repeat("a", 40), branchCreated: true},
				workLog:     WorkLogPublicationOutcome{ClaimWritten: true, EffortID: "task", RunID: "run", ClaimID: strings.Repeat("b", 64)}}
			var sequence []string
			ports := createRecoveryPorts{
				persistBacklog: func(_ string, record *lifecycleBacklogRecord, stage string) error {
					sequence = append(sequence, "receipt:"+stage)
					if stage == test.failStage {
						return errors.New("receipt unavailable")
					}
					record.Stage = stage
					return nil
				},
				rollback: func(_ context.Context, _ *canonicalRepository, _ preparedOperationRoot, _ *createdWorktreePublication) error {
					sequence = append(sequence, "rollback")
					if test.failRollback {
						return errors.New("rollback unavailable")
					}
					return nil
				},
				sealClaim: func(_ string, record lifecycleBacklogRecord) error {
					sequence = append(sequence, "seal")
					if record.WorkLogClaim != attempt.workLog.ClaimID {
						t.Fatalf("seal lost immutable claim identity: %+v", record)
					}
					if test.failSeal {
						return errors.New("seal unavailable")
					}
					return nil
				},
			}
			outcomes, err := recoverFailedCreatePublicationsWith(context.Background(), filepath.Join(root, ".wb"),
				CreateOptions{ProjectsRoot: root, Operation: "task"}, []createAttempt{attempt}, errors.New("publication failed"), ports)
			if got := strings.Join(sequence, ","); got != test.wantSequence {
				t.Fatalf("recovery order = %q, want %q", got, test.wantSequence)
			}
			if len(outcomes) != 1 || outcomes[0].Result.Action != test.wantAction ||
				outcomes[0].BacklogPersisted != test.wantPersisted || outcomes[0].RollbackCompleted != test.wantRolled ||
				outcomes[0].HeadSHA != attempt.publication.headSHA || outcomes[0].CleanupBacklogID == "" || outcomes[0].CleanupBacklogPath == "" {
				t.Fatalf("recovery outcome = %+v", outcomes)
			}
			if test.failStage != "" || test.failRollback || test.failSeal {
				if err == nil || !strings.Contains(err.Error(), "unavailable") {
					t.Fatalf("missing phase failure: outcome=%+v err=%v", outcomes[0], err)
				}
				// A failed initial receipt leaves the checkout rolled back and
				// reports its storage failure in the joined error. Later phase
				// failures additionally populate the per-outcome RecoveryError.
				if test.failStage != lifecycleStageRemovingWorktree && outcomes[0].RecoveryError == "" {
					t.Fatalf("missing per-outcome phase failure: outcome=%+v err=%v", outcomes[0], err)
				}
			} else if err != nil || outcomes[0].RecoveryError != "" {
				t.Fatalf("unexpected recovery failure: outcome=%+v err=%v", outcomes[0], err)
			}
		})
	}
}

func TestCreateRecoveryLeavesPreexistingCheckoutUntouched(t *testing.T) {
	t.Parallel()
	attempt := createAttempt{plan: &createPlan{result: CreateResult{Repository: "acme/app"}}}
	called := false
	outcomes, err := recoverFailedCreatePublicationsWith(context.Background(), "home", CreateOptions{}, []createAttempt{attempt},
		errors.New("claim failed"), createRecoveryPorts{rollback: func(context.Context, *canonicalRepository, preparedOperationRoot, *createdWorktreePublication) error {
			called = true
			return nil
		}})
	if called || err == nil || len(outcomes) != 1 || outcomes[0].Result.Action != "recovery_required" || outcomes[0].RecoveryError == "" {
		t.Fatalf("pre-existing checkout compensation = %+v err=%v rollbackCalled=%t", outcomes, err, called)
	}
}
