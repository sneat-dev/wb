package layout

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR8 is task-9 PR-8's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally matches
// a different test's error by coincidence.
var errBoomPR8 = errors.New("pr8 boom")

func TestWriteManifestInjectedHonoursAWriteFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomPR8}
	if err := writeManifestInjected(path, &migrationManifest{}, inj); !errors.Is(err, errBoomPR8) {
		t.Fatalf("writeManifestInjected(write failure) = %v, want errBoomPR8", err)
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, "manifest.json.tmp-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("write failure left a temp file: %v", matches)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("failed write published a visible manifest: %v", statErr)
	}
}

func TestWriteManifestInjectedHonoursARenameFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	inj := &filewrite.Injector{Step: filewrite.StepRename, Err: errBoomPR8}
	if err := writeManifestInjected(path, &migrationManifest{}, inj); !errors.Is(err, errBoomPR8) {
		t.Fatalf("writeManifestInjected(rename failure) = %v, want errBoomPR8", err)
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, "manifest.json.tmp-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover temp file(s) after a rename failure: %v", matches)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("failed rename published a visible manifest: %v", statErr)
	}
}

func TestWriteManifestInjectedPublishesAt0644(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := writeManifestInjected(path, &migrationManifest{}, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("published manifest mode = %o, want 0644", perm)
	}
}
