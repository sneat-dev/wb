//go:build !windows

package locallink

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A built package must contain only regular files and directories. A FIFO is
// the portable-on-unix way to present a non-regular entry that is not a
// symlink, so the entry-boundary refusal is asserted for real.
func TestLgCovCopyBuiltPackageRejectsNonRegularEntries(t *testing.T) {
	t.Parallel()
	source := t.TempDir()
	fifo := filepath.Join(source, "a-fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo is not supported here: %v", err)
	}
	lgCovWriteFile(t, filepath.Join(source, "package.json"), `{"name":"@acme/core"}`)

	err := copyBuiltPackageContents(source, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unsupported non-regular file") {
		t.Fatalf("error = %v, want the non-regular-entry refusal", err)
	}
}

// A built package that carries a file the build user cannot read must be
// reported rather than copied as a truncated package.
func TestLgCovCopyBuiltPackageReportsAnUnreadableFile(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root can read a mode-000 file")
	}
	source := t.TempDir()
	path := filepath.Join(source, "package.json")
	lgCovWriteFile(t, path, `{"name":"@acme/core"}`)
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	err := copyBuiltPackageContents(source, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("error = %v, want the unreadable-file report", err)
	}
}
