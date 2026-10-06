//go:build e2e

package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
)

// This observer never substitutes a successful Git result. Its after hook is
// reached only after the named actual command succeeds, allowing private
// filesystem drift after custody rather than fabricated admission evidence.
type protocolBoundaryNativeRunner struct {
	runner.Runner
	after func(string, string, []string, runner.Result)
}

func (r protocolBoundaryNativeRunner) RunOpts(ctx context.Context, dir string, o runner.RunOptions, name string, args ...string) (runner.Result, error) {
	result, err := r.Runner.RunOpts(ctx, dir, o, name, args...)
	if err == nil && r.after != nil {
		r.after(dir, name, args, result)
	}
	return result, err
}
func protocolBoundaryBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func protocolBoundaryRefusal(t *testing.T, err error, text string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), text) {
		t.Fatalf("want exact stage %q, got %v", text, err)
	}
}
func protocolBoundaryPrompts(t *testing.T, worktree string) {
	t.Helper()
	p := filepath.Join(worktree, ".wb", "local", "prompts")
	if err := os.RemoveAll(p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("private native ENOTDIR\n"), 0600); err != nil {
		t.Fatal(err)
	}
}
