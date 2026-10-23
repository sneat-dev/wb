package worktrees

import (
	"os"
	"path/filepath"
	"strings"
)

// forEachWorktreeMergeReceipt visits readable JSON receipt bytes in the
// directory's sorted order. Returning true stops at the first matching
// receipt, including one whose separate acknowledgement later fails proof.
// Callers retain their own error and proof policies.
func forEachWorktreeMergeReceipt(home string, visit func(string, []byte) bool) error {
	reportsDir := filepath.Join(home, "reports", "worktree-merge")
	entries, err := os.ReadDir(reportsDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") || strings.Contains(name, ".ack.json") {
			continue
		}
		path := filepath.Join(reportsDir, name)
		bytes, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if visit(path, bytes) {
			break
		}
	}
	return nil
}
