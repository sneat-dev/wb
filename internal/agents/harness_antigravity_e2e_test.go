//go:build e2e

package agents

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

func TestE2EAntigravityPreflightReadsSuccessfulHelpFromStderr(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires POSIX sh")
	}
	cli := filepath.Join(t.TempDir(), "agy")
	script := `#!/bin/sh
if [ "$1" = "--help" ]; then
  printf '%s\n' '--print --output-format --mode --sandbox --model --effort --add-dir --print-timeout' >&2
elif [ "$1" = "--version" ]; then
  printf '%s\n' 'diagnostic on stderr' >&2
  printf '%s\n' '1.4.7'
fi
`
	if err := testenv.WriteExecutableFile(cli, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	deps := DefaultPreflightDeps()
	deps.LookPath = func(name string) (string, error) {
		if name != "agy" {
			t.Fatalf("looked up %q", name)
		}
		return cli, nil
	}
	if err := Preflight(context.Background(), Resolved{Harness: HarnessAntigravity, Model: "test-model"}, deps); err != nil {
		t.Fatal(err)
	}
	if got := DetectVersion(context.Background(), HarnessAntigravity, deps); got != "1.4.7" {
		t.Fatalf("version = %q", got)
	}
}
