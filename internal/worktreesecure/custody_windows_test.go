//go:build windows

package worktreesecure

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

var errWindowsPathProbe = errors.New("path probe complete")

type windowsPathProbe struct {
	root, child string
}

func (p *windowsPathProbe) OpenRoot(root string) (int, error) {
	p.root = root
	return -1, nil
}
func (p *windowsPathProbe) Mkdir(int, string) error { return nil }
func (p *windowsPathProbe) OpenDir(_ int, child string) (int, error) {
	p.child = child
	return -1, errWindowsPathProbe
}
func (p *windowsPathProbe) IsSymlink(int, string) bool { return false }

func TestWindowsAbsoluteDirectoryStartsAtVolumeRoot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ path, root, first string }{
		{`C:\fixtures\info`, `C:\`, "fixtures"},
		{`\\server\share\fixtures\info`, `\\server\share\`, "fixtures"},
	} {
		probe := &windowsPathProbe{}
		_, err := OpenAbsoluteDirectoryNoFollowWith(probe, tc.path, false)
		if !errors.Is(err, errWindowsPathProbe) {
			t.Fatalf("walk %s: %v", tc.path, err)
		}
		if probe.root != tc.root || probe.child != tc.first {
			t.Fatalf("walk %s opened root %q then child %q; want %q then %q", tc.path, probe.root, probe.child, tc.root, tc.first)
		}
	}
	volume := filepath.VolumeName(os.TempDir())
	if volume == "" {
		t.Skip("temporary directory has no Windows volume")
	}
	root := volume + string(filepath.Separator)
	directory, err := OpenAbsoluteDirectoryNoFollow(root, false)
	if err != nil {
		t.Fatalf("open volume root %s: %v", root, err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
}
