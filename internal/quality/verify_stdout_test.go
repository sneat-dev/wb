package quality

import (
	"context"
	"strings"
	"testing"
)

// TestRunStdoutKeepsStderrOutOfParsedOutput covers the go list contract: the
// Go tool prints module download notices on stderr, and a runner that merged
// streams once handed "go: downloading ..." to go test as a package path.
func TestRunStdoutKeepsStderrOutOfParsedOutput(t *testing.T) {
	output, err := runStdout(context.Background(), t.TempDir(), "go", "env", "GOOS")
	if err != nil {
		t.Fatalf("go env: %v", err)
	}
	if lines := strings.Fields(output); len(lines) != 1 || strings.HasPrefix(lines[0], "go:") {
		t.Fatalf("stdout = %q, want one GOOS token", output)
	}

	_, err = runStdout(context.Background(), t.TempDir(), "go", "list", "-f", "{{.ImportPath}}", "./definitely-not-a-package")
	if err == nil || !strings.Contains(err.Error(), "go.mod file not found") {
		t.Fatalf("failing go list must surface stderr through the error, got %v", err)
	}
}
