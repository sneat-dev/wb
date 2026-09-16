//go:build unix

package recipe

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTailCovLandTemplateSectionWriteFailureIsHard pins that a target the
// mutator can read but not write is a hard failure, not a silent skip. The
// worktree carries a symlink to a read-only file, so the read that plans the
// change succeeds and only the write is refused.
func TestTailCovLandTemplateSectionWriteFailureIsHard(t *testing.T) {
	clone := newRemoteRepo(t)
	readonly := filepath.Join(t.TempDir(), "readonly.md")
	if err := os.WriteFile(readonly, []byte("# Project\n\nIntro.\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(clone, "README.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(readonly, filepath.Join(clone, "README.md")); err != nil {
		t.Fatal(err)
	}
	git(t, clone, "add", "-A")
	git(t, clone, "commit", "-qm", "point the target at a read-only file")
	git(t, clone, "push", "-q", "origin", "main")

	r := writeTemplate(t, "tailcov", "block body")
	r.Name = "tailcov"
	r.Target = "README.md"
	if err := r.applyDefaults(); err != nil {
		t.Fatal(err)
	}
	if _, err := Land(r, clone, "main"); err == nil {
		t.Fatal("a target that cannot be written must be a hard failure")
	}
}
