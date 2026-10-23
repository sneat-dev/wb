package recipe

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasSourceMissingTreeDoesNotApply(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "missing"), filepath.Join(root, "file", "child")} {
		got, err := hasSource(path, []string{"go", "ts"})
		if err != nil || got {
			t.Fatalf("hasSource(%q) = %v, %v", path, got, err)
		}
	}
}

func TestHasSourceNativeControl(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.ts"), []byte("export const ok = true;"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := hasSource(root, []string{"go", "ts"})
	if err != nil || !got {
		t.Fatalf("source tree = %v, %v", got, err)
	}
}
