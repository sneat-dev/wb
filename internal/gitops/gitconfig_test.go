package gitops

import (
	"path/filepath"
	"testing"
)

// GitConfig sets, reads and unsets a key, and reports an unset key as an error.
func TestGitConfigSetsReadsAndUnsetsAKey(t *testing.T) {
	_, clone := seededOriginAndClone(t)
	hooks := filepath.Join(clone, ".git", "wb-hooks")
	if _, err := GitConfig(clone, "core.hooksPath", hooks); err != nil {
		t.Fatal(err)
	}
	if got, err := GitConfig(clone, "--get", "core.hooksPath"); err != nil || got != hooks {
		t.Fatalf("GitConfig --get = (%q, %v), want %q", got, err, hooks)
	}
	if _, err := GitConfig(clone, "--unset", "core.hooksPath"); err != nil {
		t.Fatal(err)
	}
	if _, err := GitConfig(clone, "--get", "core.hooksPath"); err == nil {
		t.Fatal("GitConfig --get of an unset key succeeded")
	}
}
