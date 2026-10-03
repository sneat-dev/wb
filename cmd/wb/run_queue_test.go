package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildTinyGoModule writes a trivial buildable module to dir so integration
// tests can drive real `go build`/`go vet` subprocesses through `wb run --`
// without depending on this repository's own build graph.
func buildTinyGoModule(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module tinyfixture\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRunCommandQuietSuppressesQueueVisibilityLines proves --quiet silences
// every queue receipt while leaving the command's own exit code and streams
// alone.
func TestRunCommandQuietSuppressesQueueVisibilityLines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the WB fleet runs on macOS and Linux")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	t.Setenv("WB_ADMISSION_LOAD_FLOOR", "100000")
	root := t.TempDir()

	module := t.TempDir()
	buildTinyGoModule(t, module)
	t.Chdir(module)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--projects-root", root, "--quiet", "--", "go", "build", "./..."}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%s", code, exitOK, stderr.String())
	}
	if strings.Contains(stderr.String(), "wb run:") {
		t.Fatalf("--quiet did not suppress queue receipts: %q", stderr.String())
	}
}
