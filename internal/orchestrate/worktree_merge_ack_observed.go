package orchestrate

import "fmt"

// validateLandedFailureObservedSources reads the historical digest-bound format.
// Like the other acknowledgement readers, it checks immutable byte/identity
// evidence; it neither grants a fresh Git custody proof nor writes a new record.
func validateLandedFailureObservedSources(ack WorktreeMergeLandedFailureAcknowledgement, receipt WorktreeMergeReceipt) error {
	if len(ack.ObservedSources) == 0 {
		if ack.ReceiptSHA256 != "" {
			return fmt.Errorf("receipt digest lacks observed sources")
		}
		return nil
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return err
	}
	if validateLandedFailureAcknowledgementReceipt(receipt, receipt.ReceiptPath) != nil ||
		receipt.Status != WorktreeMergePostTargetCIFailed || ack.ReceiptSHA256 != receiptHash ||
		len(ack.ObservedSources) != len(receipt.Sources) || ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() ||
		ack.CandidateWorktree != receipt.Candidate.Worktree || ack.CandidateBranch != receipt.Candidate.Branch {
		return fmt.Errorf("invalid historical descendant evidence")
	}
	for i, source := range receipt.Sources {
		observed := ack.ObservedSources[i]
		observed.SHA = source.SHA
		if observed != source || ack.ObservedSources[i].SHA == "" {
			return fmt.Errorf("mismatched observed source identity")
		}
	}
	return nil
}
