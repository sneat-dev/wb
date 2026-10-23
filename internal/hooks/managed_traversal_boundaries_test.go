package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

func TestOwnedHookTraversalRefusesUntrustedSegments(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		segments []string
	}{
		{"empty", []string{""}},
		{"dot", []string{"."}},
		{"parent", []string{".."}},
		{"nested parent", []string{"child", ".."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
				t.Fatal(err)
			}
			original := openOwnedHookTestDirectory(t, root)
			ownedFD, err := unix.Dup(int(original.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			directory, err := openHooksDirectorySegments(ownedFD, tc.segments)
			if directory != nil || err == nil || !strings.Contains(err.Error(), "invalid secure hooks directory segment") {
				t.Fatalf("traversal=%v error=%v", directory, err)
			}
			// The traversal owns its duplicate; it must preserve the caller's authority.
			if _, err := original.Stat(); err != nil {
				t.Fatalf("caller authority lost: %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || entries[0].Name() != "child" {
				t.Fatalf("directory changed=%v error=%v", entries, err)
			}
		})
	}
}
