package worktrees

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestWorkLogFilewriteAdapters(t *testing.T) {
	t.Parallel()
	directoryPath := t.TempDir()
	directory, err := os.Open(directoryPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })

	if err := writeJSONImmutableAt(directory, "immutable.json", map[string]string{"value": "immutable"}, false); err != nil {
		t.Fatal(err)
	}
	if err := writeBytesImmutableAt(directory, "bytes-immutable", []byte("immutable"), 0o600, false); err != nil {
		t.Fatal(err)
	}
	if err := filewrite.WriteBytesImmutableAtInjected(directory, "bytes-immutable-injected", []byte("immutable"), 0o600, false, nil, writeBytesImmutableAtBeforeRename); err != nil {
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
	if err := filewrite.WriteBytesAtomicAtInjected(directory, "bytes-atomic-injected", []byte("atomic"), 0o600, nil); err != nil {
		t.Fatal(err)
	}
	if err := writeBytesAtomic(directoryPath, "bytes-path", []byte("atomic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := filewrite.WriteBytesAtomicInjected(directoryPath, "bytes-path-injected", []byte("atomic"), 0o600, nil); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(directoryPath, "atomic-path.json"), map[string]string{"value": "atomic"}, 0o600); err != nil {
		t.Fatal(err)
	}
}
