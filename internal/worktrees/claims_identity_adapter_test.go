package worktrees

import (
	"context"
	"testing"
)

func TestClaimsIdentityAndHeartbeatAdapters(t *testing.T) {
	prior := InvokedCommand()
	t.Cleanup(func() { SetInvokedCommand(prior) })
	SetInvokedCommand("worktree set")
	if got := InvokedCommand(); got != "worktree set" {
		t.Fatalf("command: %q", got)
	}
	if !ValidExecutionIdentifier("model", false) {
		t.Fatal("rejected model")
	}
	if got := NewestChangedFileTime(context.Background(), t.TempDir()); !got.IsZero() {
		t.Fatalf("non-git activity: %v", got)
	}
}

func TestClaimsParkedOwnerHandoffAdapter(t *testing.T) {
	claim := workLogClaim{AcquiredVia: "parked_session_resume", Repository: "owner/repo", AgentID: "successor",
		ExternalHandoff: &workLogExternalHandoffEvidence{HandoffID: "handoff", MemberID: "member", RequestDigest: "digest", PredecessorWBSessionID: "source", SourceWorkLogReference: "source-ref", TargetWorkLogReference: "target-ref"}}
	got, ok := legacyHandoffFromClaim(claim, nil)
	if !ok || got.Repository != "owner/repo" || got.AgentID != "successor" || got.HandoffID != "handoff" {
		t.Fatalf("handoff: %+v %t", got, ok)
	}
	if _, ok := legacyHandoffFromClaim(workLogClaim{}, nil); ok {
		t.Fatal("accepted ordinary claim")
	}
	if got := ownerPorts().ExpectedCompletionID("member", "digest"); got == "" {
		t.Fatal("missing completion ID")
	}
}
