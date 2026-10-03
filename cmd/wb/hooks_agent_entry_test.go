package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMergeAgentHookSettingsSurfacesNonNotExistReadErrors proves the
// `err != nil && !os.IsNotExist(err)` branch in mergeAgentHookSettings: a
// settings path that exists but cannot be read as a file (here, a
// directory) must surface a read error rather than being treated as an
// absent settings file.
func TestMergeAgentHookSettingsSurfacesNonNotExistReadErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings-is-a-directory.json")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := mergeAgentHookSettings(path, "wb hooks agent guard")
	if err == nil || !strings.Contains(err.Error(), "read "+path) {
		t.Fatalf("err = %v, want a read error naming %s", err, path)
	}
}

// TestAgentHookEntryMatcherStaleRejectsNonObjectEntry proves the
// `entry.(map[string]any)` !ok branch in agentHookEntryMatcherStale returns
// false rather than panicking on a malformed settings document.
func TestAgentHookEntryMatcherStaleRejectsNonObjectEntry(t *testing.T) {
	t.Parallel()
	if agentHookEntryMatcherStale(42) {
		t.Fatal("want false for a non-object entry")
	}
}
