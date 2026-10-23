//go:build !windows

package quality

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidationCacheNativeManifestFailuresAndPruning(t *testing.T) {
	t.Parallel()
	t.Run("missing root", func(t *testing.T) {
		t.Parallel()
		_, err := NewValidationCacheKey("repo", "rev", filepath.Join(t.TempDir(), "missing"), "wb", nil, nil, RunOptions{})
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("walk error = %v", err)
		}
	})
	t.Run("policy read failure", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		path := filepath.Join(root, repositoryQualityConfigPath)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := NewValidationCacheKey("repo", "rev", root, "wb", nil, nil, RunOptions{}); err == nil {
			t.Fatal("unreadable policy accepted")
		}
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("policy occupant changed: %v", err)
		}
	})
	t.Run("manifest read failure", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.Symlink("missing", filepath.Join(root, "go.mod")); err != nil {
			t.Fatal(err)
		}
		if _, err := NewValidationCacheKey("repo", "rev", root, "wb", nil, nil, RunOptions{}); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("manifest read error = %v", err)
		}
	})
	t.Run("descendants and excluded trees", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		for _, name := range []string{"go.mod", "nested/go.sum", ".git/go.mod", "vendor/go.mod", "node_modules/go.mod"} {
			path := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(name), 0600); err != nil {
				t.Fatal(err)
			}
		}
		validators := map[string]string{"specscore": "original"}
		key, err := NewValidationCacheKey("repo", "rev", root, "wb", []Check{CheckTest}, validators, RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		validators["specscore"] = "changed"
		if len(key.ModuleFiles) != 2 || !strings.HasPrefix(key.ModuleFiles[0], "go.mod=") || !strings.HasPrefix(key.ModuleFiles[1], "nested/go.sum=") || key.ValidatorSHAs["specscore"] != "original" {
			t.Fatalf("manifest inventory = %+v", key)
		}
	})
}
