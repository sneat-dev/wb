//go:build darwin

package worktrees

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWTCoreCovDarwinGitCapabilityBackend asserts the macOS backend reports a
// usable Git helper while documenting that it confines nothing, and that a Git
// executable that cannot be launched is reported as a helper failure rather
// than silently succeeding.
func TestWTCoreCovDarwinGitCapabilityBackend(t *testing.T) {
	if err := platformGitFilesystemCapabilityAvailable(); err != nil {
		t.Fatalf("macOS Git capability unavailable: %v", err)
	}
	if platformGitFilesystemCapabilityConfines() {
		t.Fatal("the macOS backend must not claim to confine the Git child")
	}
	absent := filepath.Join(t.TempDir(), "absent-git-executable")
	// The backend reports the failure on stderr; capture it so the expected
	// diagnostic does not pollute the package's test output.
	reader, writer, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	originalStderr := os.Stderr
	os.Stderr = writer
	code := runPlatformGitWithFilesystemCapability(gitFilesystemCapability{}, absent, nil, os.Environ())
	os.Stderr = originalStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	diagnostic, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Fatalf("exec of an absent Git executable = %d, want 1", code)
	}
	if !strings.Contains(string(diagnostic), "exec Git") {
		t.Fatalf("stderr %q does not name the failed exec", diagnostic)
	}
}
