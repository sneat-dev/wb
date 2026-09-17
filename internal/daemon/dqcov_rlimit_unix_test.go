//go:build unix

package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
)

// dqCovFileSizeLimitChildEnv marks the re-executed child that runs one probe
// with a lowered RLIMIT_FSIZE.
//
// The limit is per-process, and Go's testing framework records every file
// operation in its own testlog whenever the test-result cache is enabled --
// which is how CI invokes `go test`. Lowering the limit in the test binary also
// caps those framework writes, so the package fails with "can't write
// testlog.txt: file too large" even when every test passed. The child runs
// without -test.testlogfile, so the fault stays honest.
const dqCovFileSizeLimitChildEnv = "WB_DAEMON_FSIZE_LIMIT_CHILD"

// dqCovSpawnFileSizeLimitedChild re-executes the calling test in a child and
// reports whether this process is the parent, which must then return without
// running the probe body.
func dqCovSpawnFileSizeLimitedChild(t *testing.T) bool {
	t.Helper()
	if os.Getenv(dqCovFileSizeLimitChildEnv) == "1" {
		return false
	}
	command := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	command.Env = append(os.Environ(), dqCovFileSizeLimitChildEnv+"=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("probe under a lowered RLIMIT_FSIZE failed: %v\n%s", err, output)
	}
	return true
}

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
	if dqCovSpawnFileSizeLimitedChild(t) {
		return
	}
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
	if dqCovSpawnFileSizeLimitedChild(t) {
		return
	}
	root := t.TempDir()
	service, err := NewService(root, dqCovOperationsDir(root), "build", "1", allowRawForTest)
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
