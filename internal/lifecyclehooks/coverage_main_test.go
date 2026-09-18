package lifecyclehooks

import (
	"os"
	"strings"
	"testing"
)

// TestMain keeps a spawned launchWorker child from re-running the whole test
// binary. launchWorker invokes the current executable with fixed positional
// arguments, so the child would otherwise recursively run the package tests.
// When the guard environment is present the child records its argv and exits.
func TestMain(m *testing.M) {
	if os.Getenv("HKCOV_LAUNCH_WORKER_CHILD") == "1" {
		if path := os.Getenv("HKCOV_LAUNCH_WORKER_ARGV"); path != "" {
			_ = os.WriteFile(path, []byte(strings.Join(os.Args, "\n")+"\n"), 0o600)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
