//go:build !windows

package lifecyclehooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsSymlinkedOrGroupWritableConfig(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "indexer")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := writeTestConfig(t, root, executable, "warn")
	if err := os.Chmod(config, 0o620); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(config); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("group-writable config error=%v", err)
	}
	if err := os.Chmod(config, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked.yaml")
	if err := os.Symlink(config, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(link); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("symlinked config error=%v", err)
	}
}
