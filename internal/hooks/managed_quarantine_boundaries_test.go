package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedHookQuarantineRefusesLostDirectoryAuthority(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "pre-commit")
	original := []byte("retained active hook")
	if err := os.WriteFile(path, original, 0700); err != nil {
		t.Fatal(err)
	}
	directory := openOwnedHookTestDirectory(t, root)
	managed := managedHooksDirectory{path: root, commonPath: root, common: directory, directory: directory}
	expected, err := managedHookIdentityAt(directory, "pre-commit")
	if err != nil {
		t.Fatal(err)
	}
	_, err = quarantineManagedHook(managed, "pre-commit", expected, func(string) {
		if err := directory.Close(); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil {
		t.Fatal("rename accepted a closed directory authority")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatalf("active hook=%q error=%v", after, err)
	}
	if _, err := os.Stat(filepath.Join(root, "backup")); !os.IsNotExist(err) {
		t.Fatalf("backup exists after refusal: %v", err)
	}
}

func TestManagedHookQuarantinePreservesSubstitutedContent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	active := filepath.Join(root, "pre-commit")
	saved := filepath.Join(root, "original")
	if err := os.WriteFile(active, []byte("original"), 0700); err != nil {
		t.Fatal(err)
	}
	directory := openOwnedHookTestDirectory(t, root)
	managed := managedHooksDirectory{path: root, commonPath: root, common: directory, directory: directory}
	expected, err := managedHookIdentityAt(directory, "pre-commit")
	if err != nil {
		t.Fatal(err)
	}
	err = moveExpectedManagedHookNoReplace(managed, "pre-commit", "backup", expected, func(string) {
		if err := os.Rename(active, saved); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(active, []byte("replacement"), 0700); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "changed after inspection") {
		t.Fatalf("substitution refusal=%v", err)
	}
	raw, err := os.ReadFile(active)
	if err != nil || string(raw) != "replacement" {
		t.Fatalf("substitution=%q error=%v", raw, err)
	}
	raw, err = os.ReadFile(saved)
	if err != nil || string(raw) != "original" {
		t.Fatalf("original=%q error=%v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(root, "backup")); !os.IsNotExist(err) {
		t.Fatalf("substitution not restored: %v", err)
	}
}

func TestManagedHookQuarantineRetainsBothFilesWhenSubstitutionRestoreIsBlocked(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	active := filepath.Join(root, "pre-commit")
	saved := filepath.Join(root, "original")
	if err := os.WriteFile(active, []byte("original"), 0700); err != nil {
		t.Fatal(err)
	}
	directory := openOwnedHookTestDirectory(t, root)
	managed := managedHooksDirectory{path: root, commonPath: root, common: directory, directory: directory}
	expected, err := managedHookIdentityAt(directory, "pre-commit")
	if err != nil {
		t.Fatal(err)
	}
	moves := 0
	err = moveExpectedManagedHookWithRename(managed, "pre-commit", "backup", expected, func(string) {
		if err := os.Rename(active, saved); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(active, []byte("substituted"), 0700); err != nil {
			t.Fatal(err)
		}
	}, func(fromFD int, from string, toFD int, to string) error {
		err := renameNoReplace(fromFD, from, toFD, to)
		moves++
		if err == nil && moves == 1 {
			if err := os.WriteFile(active, []byte("new occupant"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "preserve substituted hook") {
		t.Fatalf("restore refusal=%v", err)
	}
	if moves != 2 {
		t.Fatalf("native rename attempts=%d", moves)
	}
	for path, want := range map[string]string{active: "new occupant", saved: "original", filepath.Join(root, "backup"): "substituted"} {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != want {
			t.Fatalf("preserved %s=%q error=%v", path, raw, err)
		}
	}
}
