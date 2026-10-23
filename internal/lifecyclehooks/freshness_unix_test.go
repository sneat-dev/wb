//go:build unix

package lifecyclehooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// readWithin runs Read and fails the test if it does not return in time.
func readWithin(t *testing.T, reader *FreshnessReader) (*FreshnessView, error) {
	t.Helper()
	type result struct {
		view *FreshnessView
		err  error
	}
	done := make(chan result, 1)
	go func() {
		view, err := reader.Read()
		done <- result{view, err}
	}()
	select {
	case r := <-done:
		return r.view, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("reading the queue blocked")
		return nil, nil
	}
}

func jobBytes(t *testing.T, checkout string) []byte {
	t.Helper()
	raw, err := json.Marshal(queuedJob{SchemaVersion: jobSchemaVersion, Key: queueKey("index", checkout), Executor: "index", Event: hkCovEvent(checkout)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFreshnessQueueReadIgnoresWhatIsNotABoundedRegularFile(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	pending := dispatcher.pendingDir()
	// A symlink to a valid job outside the state directory is not followed.
	outside := hkCovWriteFile(t, filepath.Join(t.TempDir(), "job.json"), string(jobBytes(t, checkout)), 0o600)
	if err := os.Symlink(outside, filepath.Join(pending, "link.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// A named pipe must not be opened for reading.
	if err := syscall.Mkfifo(filepath.Join(pending, "pipe.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A job over the size limit is skipped, whatever it holds.
	oversize := string(jobBytes(t, checkout)) + strings.Repeat(" ", maxJobBytes)
	hkCovWriteFile(t, filepath.Join(pending, "big.json"), oversize, 0o600)
	// A job that cannot be opened is skipped.
	hidden := hkCovWriteFile(t, filepath.Join(pending, "hidden.json"), string(jobBytes(t, checkout)), 0o600)
	if err := os.Chmod(hidden, 0); err != nil {
		t.Fatal(err)
	}
	view, err := readWithin(t, NewFreshnessReader(dispatcher))
	if err != nil {
		t.Fatal(err)
	}
	if view.Record("index", freshnessRepository, checkout).Pending {
		t.Fatal("a symlinked, piped, oversize or unreadable job counted as queued")
	}
	// The same job, plainly written, does count: the test is not vacuous.
	hkCovWriteFile(t, filepath.Join(pending, "ok.json"), string(jobBytes(t, checkout)), 0o600)
	view, err = readWithin(t, NewFreshnessReader(dispatcher))
	if err != nil || !view.Record("index", freshnessRepository, checkout).Pending {
		t.Fatalf("a plain job is queued: %v", err)
	}
	_ = os.Chmod(hidden, 0o600)
}

func TestFreshnessQueueReadRefusesUntrustedDirectories(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	// A symlinked queue directory.
	moved := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Mkdir(moved, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dispatcher.runningDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, dispatcher.runningDir()); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := NewFreshnessReader(dispatcher).Read(); err == nil {
		t.Fatal("a symlinked queue directory is not trusted")
	}
	if err := os.Remove(dispatcher.runningDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dispatcher.runningDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// A state directory others can write.
	if err := os.Chmod(dispatcher.StateDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFreshnessReader(dispatcher).Read(); err == nil {
		t.Fatal("a group-writable state directory is not trusted")
	}
	if err := os.Chmod(dispatcher.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A queue directory that cannot be listed.
	if os.Geteuid() != 0 {
		if err := os.Chmod(dispatcher.pendingDir(), 0); err != nil {
			t.Fatal(err)
		}
		_, err := NewFreshnessReader(dispatcher).Read()
		_ = os.Chmod(dispatcher.pendingDir(), 0o700)
		if err == nil {
			t.Fatal("an unlistable queue directory is an error")
		}
	}
	// A queue directory that is a file.
	if err := os.Remove(dispatcher.pendingDir()); err != nil {
		t.Fatal(err)
	}
	hkCovWriteFile(t, dispatcher.pendingDir(), "x", 0o600)
	if _, err := NewFreshnessReader(dispatcher).Read(); err == nil {
		t.Fatal("a queue directory that is a file is an error")
	}
}

func TestFreshnessQueueWithOnlyAStateDirectoryIsEmpty(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	if err := os.MkdirAll(dispatcher.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	view, err := NewFreshnessReader(dispatcher).Read()
	if err != nil || view.Record("index", freshnessRepository, checkout).Pending {
		t.Fatalf("an empty state directory has nothing queued: %v", err)
	}
}
