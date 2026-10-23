package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredTerminalNextReceiptReadRetainsNativeRootDrift(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reportRoot := filepath.Join(root, ".wb", "reports", "worktree-cleanup")
	if err := os.MkdirAll(reportRoot, 0700); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(root, "checkout")
	observed := false
	var cause error
	got, err := findTerminalCleanupProofWithReadDir(root, "acme/app", "main", "task", worktree, "topic", func(path string) ([]os.DirEntry, error) {
		if path == reportRoot {
			observed = true
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("replacement report-root bytes"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		entries, readErr := os.ReadDir(path)
		if path == reportRoot {
			cause = readErr
		}
		return entries, readErr
	})
	if !observed || got != nil || cause == nil || !errors.Is(err, cause) || !strings.Contains(err.Error(), "read terminal cleanup report root") {
		t.Fatalf("root drift=%+v %v native=%v observed=%v", got, err, cause, observed)
	}
	if raw, err := os.ReadFile(reportRoot); err != nil || string(raw) != "replacement report-root bytes" {
		t.Fatalf("root replacement altered:%q %v", raw, err)
	}
}
