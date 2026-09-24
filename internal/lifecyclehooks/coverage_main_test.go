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
			// The reader (TestHkCovLaunchWorkerSpawnsDetachedRunPending) polls
			// this path with a plain os.ReadFile. os.WriteFile creates the
			// file (truncating it to empty) and THEN writes the content as a
			// separate step; under enough scheduling pressure between those
			// two steps -- exactly what a test suite with far more parallel
			// tests now has -- the reader can observe the file after
			// creation but before the write lands, and read zero bytes. A
			// same-directory write-then-rename makes the file appear with
			// its full content already in place, or not at all.
			tmp := path + ".tmp"
			if err := os.WriteFile(tmp, []byte(strings.Join(os.Args, "\n")+"\n"), 0o600); err == nil {
				_ = os.Rename(tmp, path)
			} else {
				_ = os.Remove(tmp)
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
