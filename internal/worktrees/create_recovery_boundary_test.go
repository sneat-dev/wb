package worktrees

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateFailureReturnsExactRecoveryCoordinatesWhenReceiptAndRollbackFail(t *testing.T) {
	projects := t.TempDir()
	home := filepath.Join(projects, ".wb")
	canonical := filepath.Join(projects, "acme", "app")
	root := filepath.Join(projects, ".worktrees")
	result := CreateResult{Repository: "acme/app", CanonicalDir: canonical,
		WorktreeDir: filepath.Join(root, "task", "acme", "app"), Branch: "task", Base: "main"}
	attempt := createAttempt{plan: &createPlan{result: result, placement: worktreePlacement{Root: root}},
		publication: &createdWorktreePublication{headSHA: strings.Repeat("a", 40), branchCreated: true},
		workLog:     WorkLogPublicationOutcome{ClaimWritten: true, EffortID: "task", RunID: "run", ClaimID: strings.Repeat("b", 64)}}
	options := CreateOptions{ProjectsRoot: projects, Operation: "task",
		beforeCreateBacklogPersist: func(CreateResult) error { return errors.New("receipt storage unavailable") },
		beforeWorkLogRollback:      func(CreateResult) error { return errors.New("rollback unavailable") }}
	outcomes, err := recoverFailedCreatePublications(context.Background(), home, options, []createAttempt{attempt}, errors.New("claim publication failed"))
	if err == nil || !strings.Contains(err.Error(), "receipt storage unavailable") || !strings.Contains(err.Error(), "rollback unavailable") {
		t.Fatalf("both recovery failures were not reported: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Result.Action != "cleanup_required" || outcomes[0].BacklogPersisted ||
		outcomes[0].RollbackCompleted || outcomes[0].HeadSHA != strings.Repeat("a", 40) || outcomes[0].CleanupBacklogID == "" ||
		outcomes[0].CleanupBacklogPath == "" || outcomes[0].Result.CleanupBacklogID != outcomes[0].CleanupBacklogID {
		t.Fatalf("failed create lost exact recovery coordinates: %+v", outcomes)
	}
}
