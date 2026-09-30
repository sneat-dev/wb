package worktrees

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkLogFilewriteAdapters(t *testing.T) {
	t.Parallel()
	directoryPath := t.TempDir()
	directory, err := os.Open(directoryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()

	if err := writeJSONImmutableAt(directory, "immutable.json", map[string]string{"value": "immutable"}, false); err != nil {
		t.Fatal(err)
	}
	if err := writeBytesImmutableAt(directory, "bytes-immutable", []byte("immutable"), 0o600, false); err != nil {
		t.Fatal(err)
	}
	if err := writeBytesImmutableAtInjected(directory, "bytes-immutable-injected", []byte("immutable"), 0o600, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := readBytesAt(directory, "bytes-immutable"); err != nil {
		t.Fatal(err)
	}
	var immutable map[string]string
	if err := readJSONAt(directory, "immutable.json", &immutable); err != nil {
		t.Fatal(err)
	}
	if immutable["value"] != "immutable" {
		t.Fatalf("immutable JSON = %#v", immutable)
	}

	if err := writeJSONAtomicAt(directory, "atomic.json", map[string]string{"value": "atomic"}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeBytesAtomicAt(directory, "bytes-atomic", []byte("atomic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeBytesAtomicAtInjected(directory, "bytes-atomic-injected", []byte("atomic"), 0o600, nil); err != nil {
		t.Fatal(err)
	}
	if err := writeBytesAtomic(directoryPath, "bytes-path", []byte("atomic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeBytesAtomicInjected(directoryPath, "bytes-path-injected", []byte("atomic"), 0o600, nil); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(directoryPath, "atomic-path.json"), map[string]string{"value": "atomic"}, 0o600); err != nil {
		t.Fatal(err)
	}
}
