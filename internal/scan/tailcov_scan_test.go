package scan

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTailCovHasExtTreatsWalkErrorsAsNoMatch covers the documented contract for
// entries the walk cannot read: an unreadable subdirectory or a root that is
// not there is "not a source file", not a fatal error. A caller deciding
// whether to run a language toolchain must not have the whole check fail
// because one directory lacked permission.
func TestTailCovHasExtTreatsWalkErrorsAsNoMatch(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	found, err := HasExt(missing, ".go")
	if err != nil {
		t.Fatalf("HasExt(missing root) = %v, want a non-fatal answer", err)
	}
	if found {
		t.Fatal("HasExt(missing root) = true, want false")
	}
}

// TestTailCovHasExtKeepsWalkingPastAnUnreadableDirectory pins the second half
// of the same contract: a directory the process cannot list is skipped, and a
// readable match elsewhere in the tree is still found.
func TestTailCovHasExtKeepsWalkingPastAnUnreadableDirectory(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "a-blocked")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "hidden.go"), []byte("package hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "visible.rb"), []byte("puts 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	// RemoveAll cannot descend into a 0o000 directory, so restore the mode
	// before the testing package tears the fixture down.
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })

	if found, err := HasExt(root, ".go"); err != nil || found {
		t.Fatalf("HasExt over an unreadable directory = %v, %v; want false, nil", found, err)
	}
	if found, err := HasExt(root, ".rb"); err != nil || !found {
		t.Fatalf("HasExt(root, .rb) = %v, %v; want the readable match found despite the blocked sibling", found, err)
	}
}
