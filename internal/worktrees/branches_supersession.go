package worktrees

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
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
	return fileSHA256(path)
}

func fileSHA256(path string) (digest string, resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
