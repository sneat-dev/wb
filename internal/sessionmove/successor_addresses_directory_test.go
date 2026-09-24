package sessionmove

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenSuccessorAddressesAtRejectsWrongMode drives the mode-mismatch
// branch of openSuccessorAddressesAt: the directory exists (so no create is
// attempted) but was not left at the required 0700, which the exact-mode
// invariant treats as untrusted.
func TestOpenSuccessorAddressesAtRejectsWrongMode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	addresses := filepath.Join(root, successorAddressesDirName)
	if err := os.Mkdir(addresses, 0o755); err != nil {
		t.Fatal(err)
	}
	rootFile := openTestDirectory(t, root)
	if _, err := openSuccessorAddressesAt(rootFile, false); err == nil {
		t.Fatal("openSuccessorAddressesAt accepted a 0755 directory, want a mode-mismatch error")
	}
}
