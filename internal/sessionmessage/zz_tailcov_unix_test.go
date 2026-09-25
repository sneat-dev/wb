//go:build unix

package sessionmessage

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// TestTailCovNewOSTmuxRejectsNonRegularExecutable proves the adapter refuses a
// PATH hit that is executable but not a regular file, so the fixed executable
// can never be a device or fifo. Unix-only because fifos do not exist on
// Windows.
func TestTailCovNewOSTmuxRejectsNonRegularExecutable(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "tmux")
	if err := syscall.Mkfifo(fifo, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if _, err := newOSTmux(); err == nil || !strings.Contains(err.Error(), "not a regular executable file") {
		t.Fatalf("non-regular executable err = %v", err)
	}
}

// TestTailCovExecTmuxCommandRunnerWiresStreams exercises the real process
// adapter against a hermetic stub executable: the exact stdin bytes must reach
// the child, and the child's stdout, stderr, and exit status must be surfaced
// unchanged. Unix-only because the stub is a shell script.
func TestTailCovExecTmuxCommandRunnerWiresStreams(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stub := filepath.Join(dir, "stub")
	script := "#!/bin/sh\n" +
		"cat\n" +
		"printf 'diagnostic: %s' \"$1\" >&2\n" +
		"exit 3\n"
	if err := testenv.WriteExecutableFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := execTmuxCommandRunner{}.Run(context.Background(), stub, []string{"pane-7"}, []byte("payload"), &stdout, &stderr)
	if err == nil {
		t.Fatal("a non-zero stub exit must surface as an error")
	}
	if stdout.String() != "payload" {
		t.Fatalf("stdout = %q, want the exact stdin payload", stdout.String())
	}
	if !strings.Contains(stderr.String(), "diagnostic: pane-7") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
