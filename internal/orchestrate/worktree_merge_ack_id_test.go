package orchestrate

import "testing"

// The acknowledgement ID is a pure function: this golden test needs no Git, so it
// stays in the default tier while the retired-source journeys run in the e2e tier.

const goldenLandedFailureAcknowledgementID = "061cdd3eb7c795c2aabc73cbd501992baa75998d26bd8cf0f826e9df1ad66bee"

func TestLandedFailureAcknowledgementIDIsUnchangedWithoutRetiredEvidence(t *testing.T) {
	t.Parallel()
	ack := WorktreeMergeLandedFailureAcknowledgement{
		ReceiptID: "r", ReceiptPath: "/p", ReceiptStatus: WorktreeMergePostTargetCIFailed, ReceiptTargetSHA: "t", ReceiptLandingSHA: "l",
		CurrentTargetSHA: "c", CandidateSHA: "d", Sources: []WorktreeMergeSource{{Task: "s", Worktree: "/w", Branch: "b", SHA: "x"}},
	}
	// Golden value computed before retired-source evidence existed.
	if got := landedFailureAcknowledgementID(ack); got != goldenLandedFailureAcknowledgementID {
		t.Fatalf("ID changed for an acknowledgement without retired evidence: %s", got)
	}
}
