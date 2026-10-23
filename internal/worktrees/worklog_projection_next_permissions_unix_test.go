//go:build !windows

package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkLogProjectionNextLegacyCountRetainsNativeWalkPermissionCause(t *testing.T) {
	t.Parallel()
	root := projectionNextTemp(t)
	private := filepath.Join(root, "unreadable")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(private, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(private, 0700); err != nil {
			t.Error(err)
		}
	})
	count, err := countLegacyWorkLogProjections(root, "task", "run")
	if count != 0 || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("native walk refusal=%d %v", count, err)
	}
	if count, err := countLegacyWorkLogProjections(filepath.Join(root, "missing"), "task", "run"); count != 0 || err != nil {
		t.Fatalf("native absent root=%d %v", count, err)
	}
}
