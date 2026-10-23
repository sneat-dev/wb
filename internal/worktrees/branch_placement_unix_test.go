//go:build !windows

package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBranchPlacementNativeResolutionRefusals(t *testing.T) {
	t.Parallel()
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(parent, "future")
	calls := 0
	got, err := resolvePlacementPathWithResolver(child, func(path string) (string, error) {
		calls++
		resolved, err := filepath.EvalSymlinks(path)
		if path == child && errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(parent, parent+"-held"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(parent, parent); err != nil {
				t.Fatal(err)
			}
		}
		return resolved, err
	})
	if got != "" || err == nil || errors.Is(err, os.ErrNotExist) || calls != 2 {
		t.Fatalf("native parent refusal=(%q,%v), calls=%d", got, err, calls)
	}
	if _, _, err := canonicalPathAddress(parent, child); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native root-address refusal=%v", err)
	}
	if _, _, err := loadBranchConfigFile(parent); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native config resolution refusal=%v", err)
	}
}
