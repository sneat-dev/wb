package continuationinput

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
func TestReadFailuresPreserveTheirIdentity(t *testing.T) {
	t.Parallel()
	want := errors.New("input unavailable")
	if _, err := ReadBounded(failingReader{want}, 16, "context"); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if _, err := ReadHandover(failingReader{want}, "-", 16); !errors.Is(err, want) {
		t.Fatal(err)
	}
}
func TestBoundedRegularFileRejectsUnsafeInputsAndPreservesExactBytes(t *testing.T) {
	t.Parallel()
	if _, err := ReadBounded(strings.NewReader(""), 16, "cwWt body"); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("empty bounded read = %v", err)
	}
	if _, err := ReadBounded(strings.NewReader(strings.Repeat("x", 17)), 16, "cwWt body"); err == nil || !strings.Contains(err.Error(), "exceeds 16 bytes") {
		t.Fatalf("oversized bounded read = %v", err)
	}
	if raw, err := ReadBounded(strings.NewReader("hello"), 16, "cwWt body"); err != nil || string(raw) != "hello" {
		t.Fatalf("bounded read = (%q, %v)", raw, err)
	}

	directory := t.TempDir()
	file := filepath.Join(directory, "body.txt")
	if err := os.WriteFile(file, []byte("from a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if raw, err := ReadRegularFile(file, 64); err != nil || string(raw) != "from a file\n" {
		t.Fatalf("regular file = (%q, %v)", raw, err)
	}

	// The root itself is not a usable message file.
	if _, err := ReadRegularFile(string(filepath.Separator), 64); err == nil || !strings.Contains(err.Error(), "one clean path") {
		t.Fatalf("root path = %v", err)
	}
	// A missing file is an open failure.
	if _, err := ReadRegularFile(filepath.Join(directory, "missing.txt"), 64); err == nil {
		t.Fatal("missing file must fail")
	}
	// A directory is not a regular file.
	if _, err := ReadRegularFile(directory, 64); err == nil || !strings.Contains(err.Error(), "regular single-link") {
		t.Fatalf("directory = %v", err)
	}
	// An empty file is refused.
	empty := filepath.Join(directory, "empty.txt")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFile(empty, 64); err == nil {
		t.Fatal("empty file must fail")
	}
	// An oversized file is refused before it is read.
	big := filepath.Join(directory, "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 128)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFile(big, 64); err == nil {
		t.Fatal("oversized file must fail")
	}
	// A symlink is never followed.
	link := filepath.Join(directory, "link.txt")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFile(link, 64); err == nil || !strings.Contains(err.Error(), "open message file") {
		t.Fatalf("symlink = %v", err)
	}
	// A hard-linked file is refused: Nlink must be exactly one.
	hardlink := filepath.Join(directory, "hard.txt")
	if err := os.Link(file, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFile(hardlink, 64); err == nil {
		t.Fatal("hard-linked file must fail")
	}
	// A file whose parent cannot be resolved is reported.
	blocker := filepath.Join(directory, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFile(filepath.Join(blocker, "child.txt"), 64); err == nil {
		t.Fatal("an unresolvable parent must fail")
	}
}
func TestHandoverPreservesFileAndStdinPolicy(t *testing.T) {
	t.Parallel()
	const limit = 1 << 20
	dir := t.TempDir()
	if _, err := ReadHandover(nil, "", limit); err == nil ||
		!strings.Contains(err.Error(), "--handover-file is required") {
		t.Fatalf("empty path = %v", err)
	}
	if _, err := ReadHandover(nil, filepath.Join(dir, "absent.md"), limit); err == nil ||
		!strings.Contains(err.Error(), "open handover file") {
		t.Fatalf("missing file = %v", err)
	}
	if _, err := ReadHandover(nil, dir, limit); err == nil ||
		!strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("directory = %v", err)
	}
	oversized := filepath.Join(dir, "huge.md")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte("x"), limit+2), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHandover(nil, oversized, limit); err == nil ||
		!strings.Contains(err.Error(), "handover exceeds") {
		t.Fatalf("oversized file = %v", err)
	}
	blank := filepath.Join(dir, "blank.md")
	if err := os.WriteFile(blank, []byte("   \n\t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHandover(nil, blank, limit); err == nil ||
		!strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("blank file = %v", err)
	}
	valid := filepath.Join(dir, "valid.md")
	if err := os.WriteFile(valid, []byte("carry on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, err := ReadHandover(nil, valid, limit)
	if err != nil || string(body) != "carry on\n" {
		t.Fatalf("valid file = %q, %v", body, err)
	}
	// "-" reads the command's stdin so a handover never has to touch disk.
	input := strings.NewReader("from stdin\n")
	body, err = ReadHandover(input, "-", limit)
	if err != nil || string(body) != "from stdin\n" {
		t.Fatalf("stdin handover = %q, %v", body, err)
	}
}
