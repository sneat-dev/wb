package worktrees

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
)

// branchSupersessionReceipt reuses the worktree supersession validator for a
// branch with no task. It intentionally permits only Task == "": a receipt
// for a managed worktree belongs to worktree cleanup, not this branch-only
// command.
func branchSupersessionReceipt(ctx context.Context, receiptPath string, entry ListResult) (*SupersessionReceipt, string, error) {
	receipt, rejection, err := supersessionReceiptForEntry(ctx, receiptPath, entry)
	if err != nil {
		return nil, "", err
	}
	if rejection != "" {
		return nil, rejection, nil
	}
	if receipt.Task != "" {
		return nil, "branch-only supersession receipts must have an empty task", nil
	}
	return receipt, "", nil
}

func supersessionFileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
