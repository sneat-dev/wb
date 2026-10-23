package testenv

import (
	"os"
	"path/filepath"
	"testing"
)

// SnapshotTrees records exact file bytes keyed by path, tolerating absent roots.
func SnapshotTrees(t testing.TB, roots ...string) map[string][]byte {
	t.Helper()
	snapshot := make(map[string][]byte)
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
			if os.IsNotExist(walkErr) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot[path] = append([]byte(nil), raw...)
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot trees: %v", err)
		}
	}
	return snapshot
}
