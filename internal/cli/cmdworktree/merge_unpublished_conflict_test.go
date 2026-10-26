package cmdworktree

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
)

func TestUnpublishedConflictCommandProjectsReviewedProofAndAuditIdentity(t *testing.T) {
	t.Parallel()
	inv := mergeTestInvocation(shared.Flags{ProjectsRoot: "private-root"})
	var out bytes.Buffer
	inv.operations.AcknowledgeUnpublishedConflict = func(_ context.Context, o orchestrate.WorktreeMergeUnpublishedConflictOptions) (orchestrate.WorktreeMergeUnpublishedConflictAcknowledgement, error) {
		if o.ProjectsRoot != "private-root" || o.Receipt != "receipt" || o.Actor != "operator" || o.Reason != "reviewed recovery" || o.ExpectedProofID != "exact-reviewed-proof" || o.Apply {
			t.Fatalf("lost proof/audit identity: %+v", o)
		}
		return orchestrate.WorktreeMergeUnpublishedConflictAcknowledgement{ID: o.ExpectedProofID, Status: "unpublished_conflict_preserved"}, nil
	}
	command := newWorktreeMergeAcknowledgeUnpublishedConflictCmd(inv)
	command.SetOut(&out)
	if err := mergeExecute(command, "receipt", "--actor", "operator", "--reason", "reviewed recovery", "--expected-proof-id", "exact-reviewed-proof"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "proof-id: exact-reviewed-proof") || !strings.Contains(out.String(), "dry-run only") {
		t.Fatal(out.String())
	}
	if err := mergeExecute(newWorktreeMergeAcknowledgeUnpublishedConflictCmd(inv), "receipt", "--format", "yaml"); err == nil {
		t.Fatal("accepted unsupported format")
	}
}
