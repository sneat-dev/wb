//go:build e2e

package orchestrate

func preparingContinuationReceipt(f conflictRecoveryFixture) WorktreeMergeReceipt {
	receipt := f.receipt
	receipt.ID = receipt.Candidate.Task
	receipt.Status = WorktreeMergePreparing
	receipt.Candidate.SHA = f.head
	return receipt
}
