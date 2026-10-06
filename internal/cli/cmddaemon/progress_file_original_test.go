package cmddaemon

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCwWtDaemonOperationProgressWriter(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	writer, closeWriter, err := progressWriter(testRuntime(), &stderr, false, "")
	if err != nil || writer == nil {
		t.Fatalf("disabled progress = (%v, %v)", writer, err)
	}
	closeWriter()

	if _, _, err := progressWriter(testRuntime(), &stderr, false, "/tmp/x"); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("disabled progress with a file = %v", err)
	}

	writer, closeWriter, err = progressWriter(testRuntime(), &stderr, true, "")
	if err != nil || writer != &stderr {
		t.Fatalf("stderr progress = (%v, %v)", writer, err)
	}
	closeWriter()

	path := filepath.Join(t.TempDir(), "progress.log")
	writer, closeWriter, err = progressWriter(testRuntime(), &stderr, true, path)
	if err != nil {
		t.Fatalf("file progress: %v", err)
	}
	if _, err := writer.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	closeWriter()
	if contents, err := os.ReadFile(path); err != nil || string(contents) != "hello\n" {
		t.Fatalf("progress file = (%q, %v)", contents, err)
	}

	// An unopenable destination is an error, never a silent fallback.
	if _, _, err := progressWriter(testRuntime(), &stderr, true, filepath.Join(t.TempDir(), "missing", "progress.log")); err == nil || !strings.Contains(err.Error(), "open human progress file") {
		t.Fatalf("unopenable progress file = %v", err)
	}
}
