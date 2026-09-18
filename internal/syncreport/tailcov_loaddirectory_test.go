package syncreport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTailCovLoadDirectoryRejectsMissingEmptyAndNonMarkdownDirectories pins
// the two ways LoadDirectory can find nothing to publish: an unreadable
// directory and a directory whose readable entries are all ineligible.
func TestTailCovLoadDirectoryRejectsMissingEmptyAndNonMarkdownDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := LoadDirectory(filepath.Join(root, "missing")); err == nil || !strings.Contains(err.Error(), "read sync report directory") {
		t.Fatalf("LoadDirectory on a missing directory = %v, want a read failure", err)
	}

	if _, err := LoadDirectory(t.TempDir()); err == nil || !strings.Contains(err.Error(), "no top-level Markdown records found") {
		t.Fatalf("LoadDirectory on an empty directory = %v, want a no-records error", err)
	}

	nonMarkdown := t.TempDir()
	if err := os.WriteFile(filepath.Join(nonMarkdown, "notes.txt"), []byte("not a record"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(nonMarkdown, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDirectory(nonMarkdown); err == nil || !strings.Contains(err.Error(), "no top-level Markdown records found") {
		t.Fatalf("LoadDirectory with only non-Markdown entries = %v, want a no-records error", err)
	}
}

// TestTailCovLoadDirectoryRejectsSymlinkedRecord proves an agent cannot smuggle
// a path outside the report directory by publishing a symlink that ends in
// .md.
func TestTailCovLoadDirectoryRejectsSymlinkedRecord(t *testing.T) {
	t.Parallel()
	tailCovRequireUnixFilesystem(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(validRecord), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}
	_, err := LoadDirectory(dir)
	if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("LoadDirectory with a symlinked record = %v, want a regular-file refusal", err)
	}
}

// TestTailCovLoadDirectoryReportsInspectionFailure covers the case where the
// directory can be listed but an entry cannot be inspected: a directory
// without search permission lists names yet refuses to stat them.
func TestTailCovLoadDirectoryReportsInspectionFailure(t *testing.T) {
	t.Parallel()
	tailCovRequireUnixFilesystem(t)
	parent := t.TempDir()
	dir := filepath.Join(parent, "records")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte(validRecord), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o444); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()

	_, err := LoadDirectory(dir)
	if err == nil || !strings.Contains(err.Error(), "inspect a.md") {
		t.Fatalf("LoadDirectory = %v, want an inspect failure naming a.md", err)
	}
}

// TestTailCovLoadDirectoryReportsUnreadableRecord covers a regular record file
// that cannot be opened for reading.
func TestTailCovLoadDirectoryReportsUnreadableRecord(t *testing.T) {
	t.Parallel()
	tailCovRequireUnixFilesystem(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.md")
	if err := os.WriteFile(path, []byte(validRecord), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(path, 0o600) }()

	_, err := LoadDirectory(dir)
	if err == nil || !strings.Contains(err.Error(), "read secret.md") {
		t.Fatalf("LoadDirectory = %v, want a read failure naming secret.md", err)
	}
}

// TestTailCovLoadDirectoryRejectsAnUnparsableRecord proves a malformed record
// names its own file so an agent can fix the right one.
func TestTailCovLoadDirectoryRejectsAnUnparsableRecord(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.md"), []byte("not a record"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadDirectory(dir)
	if err == nil || !strings.Contains(err.Error(), "bad.md: ") {
		t.Fatalf("LoadDirectory = %v, want the failing record's file name", err)
	}
}
