//go:build unix

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
)

// dqCovZeroFileSizeLimit forbids writes to regular files for the remainder of
// the calling test. The Go runtime ignores SIGXFSZ, so an over-limit write
// returns EFBIG instead of killing the process, and the previous limit is
// restored by t.Cleanup before the temporary directory is removed.
func dqCovZeroFileSizeLimit(t *testing.T) {
	t.Helper()
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatalf("read RLIMIT_FSIZE: %v", err)
	}
	limited := syscall.Rlimit{Cur: 0, Max: original.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Fatalf("lower RLIMIT_FSIZE: %v", err)
	}
	t.Cleanup(func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
			t.Fatalf("restore RLIMIT_FSIZE: %v", err)
		}
	})
}

// TestDqCovStoreSaveReportsUnwritableTemporaryFile proves a state file that
// cannot be written is reported and never published over an existing record.
func TestDqCovStoreSaveReportsUnwritableTemporaryFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	state := NewStarting(nil, "127.0.0.1:8766", Provenance{Executable: "/wb"}, "owner", time.Unix(1, 0))

	dqCovZeroFileSizeLimit(t)
	err := (Store{Path: path}).Save(state)
	if err == nil || !strings.Contains(err.Error(), "file too large") {
		t.Fatalf("Save() with an unwritable temporary file = %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("failed Save published a state file: %v", statErr)
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed Save left %d entries behind", len(entries))
	}
}

// TestDqCovPersistRecordReportsUnwritableOperationFile proves a durable
// transition that cannot be written is reported instead of being published.
func TestDqCovPersistRecordReportsUnwritableOperationFile(t *testing.T) {
	service, err := NewService(t.TempDir(), "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	service.directory = directory
	item := &record{Schema: QueueSchema, Operation: &daemonv1.Operation{SchemaVersion: QueueSchema, OperationId: "unwritable"}}

	dqCovZeroFileSizeLimit(t)
	if err := service.persistRecord(item); err == nil || !strings.Contains(err.Error(), "write daemon operation") {
		t.Fatalf("persistRecord with an unwritable file = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(directory, "unwritable.json")); !os.IsNotExist(statErr) {
		t.Fatalf("failed persistRecord published an operation file: %v", statErr)
	}
}
