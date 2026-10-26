package gitrepo

import (
	"path/filepath"
	"testing"
)

// Without an explicit Notify, notices go to stderr rather than nowhere.
func TestProviderNoticesDefaultToStderr(t *testing.T) {
	t.Parallel()
	p := New(Options{})
	if p.opts.Notify == nil {
		t.Fatal("New left Notify nil")
	}
	p.opts.Notify("wb: test notice from TestProviderNoticesDefaultToStderr")
}

func TestWithinPathMatchesTheRootAndItsDescendantsOnly(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "p", "team", "wb-state")
	for path, want := range map[string]bool{
		root:                                 true,
		filepath.Join(root, ".git", "hooks"): true,
		root + "-other":                      false,
		filepath.Join(string(filepath.Separator), "p", "elsewhere"): false,
	} {
		if got := withinPath(path, root); got != want {
			t.Errorf("withinPath(%q) = %v, want %v", path, got, want)
		}
	}
}
