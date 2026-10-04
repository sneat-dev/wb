package workerrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerIndependentlyRefusesAssignedDirectoryOutsidePermittedRoots(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "repo")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	permitted, err := CanonicalRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := workerPermitsDirectory(permitted, inside); err != nil || !ok {
		t.Fatalf("inside directory = %t, %v", ok, err)
	}
	if ok, err := workerPermitsDirectory(permitted, outside); err != nil || ok {
		t.Fatalf("outside directory = %t, %v", ok, err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if ok, err := workerPermitsDirectory(permitted, link); err != nil || ok {
		t.Fatalf("symlink escape = %t, %v", ok, err)
	}
}
func TestCwCovCanonicalWorkerRootsAndPermissions(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "repo")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalRoots(nil); err == nil || !strings.Contains(err.Error(), "at least one --root") {
		t.Fatalf("empty roots error = %v", err)
	}
	if _, err := CanonicalRoots([]string{"relative/path"}); err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("relative root error = %v", err)
	}
	if _, err := CanonicalRoots([]string{filepath.Join(root, "absent")}); err == nil {
		t.Fatal("a missing root must be refused")
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalRoots([]string{file}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file root error = %v", err)
	}
	// The same root named twice is canonicalized to one entry.
	permitted, err := CanonicalRoots([]string{root, root + string(filepath.Separator)})
	if err != nil {
		t.Fatal(err)
	}
	if len(permitted) != 1 {
		t.Fatalf("permitted = %v, want one deduplicated root", permitted)
	}
	if ok, err := workerPermitsDirectory(permitted, nested); err != nil || !ok {
		t.Fatalf("nested directory = (%t, %v), want permitted", ok, err)
	}
	if ok, err := workerPermitsDirectory(permitted, "relative"); err == nil || ok {
		t.Fatalf("relative cwd = (%t, %v), want a refusal", ok, err)
	}
	if ok, err := workerPermitsDirectory(permitted, filepath.Join(root, "absent")); err == nil || ok {
		t.Fatalf("missing cwd = (%t, %v), want a refusal", ok, err)
	}
	if ok, err := workerPermitsDirectory(permitted, t.TempDir()); err != nil || ok {
		t.Fatalf("outside cwd = (%t, %v), want a refusal", ok, err)
	}
}
