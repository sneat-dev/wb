//go:build !windows

package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedHookQuarantineRefusesSymlinkSubstitutionWithoutTouchingTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	active := filepath.Join(root, "pre-commit")
	saved := filepath.Join(root, "original")
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(active, []byte("original"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("external occupant"), 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	managed := managedHooksDirectory{path: root, commonPath: root, common: directory, directory: directory}
	expected, err := managedHookIdentityAt(directory, "pre-commit")
	if err != nil {
		t.Fatal(err)
	}
	err = moveExpectedManagedHookNoReplace(managed, "pre-commit", "backup", expected, func(string) {
		if err := os.Rename(active, saved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, active); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "inspect quarantined managed hook") {
		t.Fatalf("symlink refusal=%v", err)
	}
	if got, err := os.Readlink(filepath.Join(root, "backup")); err != nil || got != target {
		t.Fatalf("quarantined symlink=%q error=%v", got, err)
	}
	for path, want := range map[string]string{saved: "original", target: "external occupant"} {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != want {
			t.Fatalf("preserved %s=%q error=%v", path, raw, err)
		}
	}
}
