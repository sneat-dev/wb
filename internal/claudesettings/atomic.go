package claudesettings

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// WriteAtomically replaces the settings file through a temporary file
// in the same directory, so an interrupted write can never leave the user
// without a settings file.
func WriteAtomically(path string, document []byte) error {
	return WriteAtomicallyInjected(path, document, nil)
}

// WriteAtomicallyInjected is WriteAtomically's test seam
// (task-9 PR-2): every production call site reaches it only through
// WriteAtomically, which always passes a nil *filewrite.Injector,
// so production behaviour is unchanged; a test passes its own Injector
// directly to reach a create/write/close/chmod/rename failure branch
// deterministically. The chmod runs path-based, after close, exactly as
// before -- unlike this PR's other sites, which chmod the still-open fd
// -- so it uses filewrite.ChmodPath rather than filewrite.Chmod.
func WriteAtomicallyInjected(path string, document []byte, inj *filewrite.Injector) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", directory, err)
	}
	temporary, err := filewrite.CreateTemp(directory, ".wb-settings-*", inj)
	if err != nil {
		return fmt.Errorf("stage a replacement for %s: %w", path, err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := filewrite.Write(temporary, document, name, inj); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := filewrite.Close(temporary, name, inj); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	if err := filewrite.ChmodPath(name, 0o600, inj); err != nil {
		return fmt.Errorf("secure %s: %w", name, err)
	}
	if err := filewrite.Rename(name, path, inj); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
