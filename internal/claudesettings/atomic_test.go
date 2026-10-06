package claudesettings

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestWriteSettingsAtomicallyInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.json")
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomForSettings}
	if err := WriteAtomicallyInjected(path, []byte("{}"), inj); !errors.Is(err, errBoomForSettings) {
		t.Fatalf("WriteAtomicallyInjected error = %v", err)
	}
}
func TestWriteSettingsAtomicallyInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.json")
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomForSettings}
	if err := WriteAtomicallyInjected(path, []byte("{}"), inj); !errors.Is(err, errBoomForSettings) {
		t.Fatalf("WriteAtomicallyInjected error = %v", err)
	}
}
func TestWriteSettingsAtomicallyInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomForSettings}
	if err := WriteAtomicallyInjected(path, []byte("{}"), inj); !errors.Is(err, errBoomForSettings) {
		t.Fatalf("WriteAtomicallyInjected error = %v", err)
	}
	assertNoLeftoverSettingsTempFile(t, dir)
}

func TestWriteSettingsAtomicallyInjectedHonoursAnInjectedChmodFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.json")
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Err: errBoomForSettings}
	if err := WriteAtomicallyInjected(path, []byte("{}"), inj); !errors.Is(err, errBoomForSettings) {
		t.Fatalf("WriteAtomicallyInjected error = %v", err)
	}
}

func TestWriteSettingsAtomicallyInjectedHonoursAnInjectedRenameFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	inj := &filewrite.Injector{Step: filewrite.StepRename, Err: errBoomForSettings}
	if err := WriteAtomicallyInjected(path, []byte("{}"), inj); !errors.Is(err, errBoomForSettings) {
		t.Fatalf("WriteAtomicallyInjected error = %v", err)
	}
	assertNoLeftoverSettingsTempFile(t, dir)
}

// assertNoLeftoverSettingsTempFile asserts WriteAtomicallyInjected's
// defer os.Remove(name) ran: no ".wb-settings-*" staging file survives a
// close or rename failure. Mutation evidence (task-9 PR-2 review, B2):
// deleting that defer at sibling call sites survived every test that only
// asserted the returned error, since the staging file's mode (0600) leaves
// it invisible to anything but a directory listing.
func assertNoLeftoverSettingsTempFile(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".wb-settings-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover settings temp file(s) after failure: %v", matches)
	}
}
