package wbhome

import (
	"path/filepath"
	"testing"
)

// The remote store clone lives in the dot-named state directory, so a
// single-level enumeration of the projects root never yields it.
func TestRemoteStoreClonePathIsInsideTheStateDirectory(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "p")
	if got, want := RemoteStoreClonePath(root, "team", "wb-state"), filepath.Join(root, ".wb", "remote-store", "team", "wb-state"); got != want {
		t.Fatalf("RemoteStoreClonePath = %q, want %q", got, want)
	}
}
