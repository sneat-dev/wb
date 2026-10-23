package wbhome

import (
	"path/filepath"
	"testing"
)

// resolveAbs walks up to the nearest existing ancestor when the leaf itself
// does not exist yet, exercising the filepath.Dir(absolute) parent step.
func TestResolveAbsWalksUpToExistingParent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(root): %v", err)
	}
	missing := filepath.Join(root, "does-not-exist-yet", "leaf")

	got, err := resolveAbs(missing)
	if err != nil {
		t.Fatalf("resolveAbs: %v", err)
	}
	want := filepath.Join(resolvedRoot, "does-not-exist-yet", "leaf")
	if got != want {
		t.Fatalf("resolveAbs(%q) = %q, want %q", missing, got, want)
	}
}
