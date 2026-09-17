package lifecyclehooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// hkCovWriteFile writes content to path with the given mode and fails the test
// on error.
func hkCovWriteFile(t *testing.T, path string, content string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// hkCovValidConfigYAML returns a minimal valid hooks configuration that matches
// github.com/*/* and runs the given executable.
func hkCovValidConfigYAML(executable string) string {
	return "hooks:\n  version: 1\n  executors:\n    index:\n      run: " + executable +
		"\n      args: [sync, .]\n      cwd: repository\n      mode: coalesced\n      timeout: 2m\n      failure: warn\n" +
		"  bindings:\n    - on: [checkout-updated]\n      match:\n        repositories:\n          include: [github.com/*/*]\n      execute: [index]\n"
}

// hkCovEnv builds a hermetic dispatcher rooted at a temp dir with a valid config
// and an executable outside the checkout. It returns the dispatcher and the
// checkout directory.
func hkCovEnv(t *testing.T) (Dispatcher, string) {
	t.Helper()
	root := t.TempDir()
	return hkCovEnvIn(t, root), hkCovCheckout(t, root)
}

// hkCovCheckout creates (wide open, not private) a checkout directory under root.
func hkCovCheckout(t *testing.T, root string) string {
	t.Helper()
	checkout := filepath.Join(root, "checkout")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	return checkout
}

// hkCovEnvIn builds a hermetic dispatcher rooted at root. The state directory
// and receipt stream live under root, so tests never touch the real user state.
func hkCovEnvIn(t *testing.T, root string) Dispatcher {
	t.Helper()
	executable := filepath.Join(root, "bin", "indexer")
	hkCovWriteFile(t, executable, "#!/bin/sh\nexit 0\n", 0o755)
	config := hkCovWriteFile(t, filepath.Join(root, "wb.yaml"), hkCovValidConfigYAML(executable), 0o600)
	return hkCovDispatcherFor(t, root, config)
}

// hkCovDispatcherFor wires a dispatcher for an existing config, with permissive
// checkout verification so tests can exercise queue mechanics without git.
func hkCovDispatcherFor(t *testing.T, root, config string) Dispatcher {
	t.Helper()
	return Dispatcher{
		ConfigPath:   config,
		StateDir:     filepath.Join(root, "state"),
		ReceiptPath:  filepath.Join(root, "receipts.jsonl"),
		Now:          time.Now,
		EvalSymlinks: filepath.EvalSymlinks,
		LaunchWorker: func(WorkerRequest) error { return nil },
		VerifyCheckout: func(event Event) (string, os.FileInfo, error) {
			physical, err := filepath.EvalSymlinks(event.Checkout)
			if err != nil {
				return "", nil, err
			}
			info, err := os.Stat(physical)
			return physical, info, err
		},
	}
}

// hkCovEvent returns a matching checkout-updated event for the given checkout.
func hkCovEvent(checkout string) Event {
	return Event{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: checkout, OldSHA: "a", NewSHA: "b", Cause: "pull"}
}

// hkCovEnqueueOne dispatches a single matching event and verifies it landed in
// the pending queue.
func hkCovEnqueueOne(t *testing.T, dispatcher Dispatcher, checkout string) {
	t.Helper()
	if _, err := dispatcher.Dispatch(context.Background(), []Event{hkCovEvent(checkout)}); err != nil {
		t.Fatal(err)
	}
}

// hkCovLockFile replaces the lock file at path with a self-referential symlink
// so flock acquisition fails with an observable error. A directory does not
// work: opening a directory for writing succeeds on some platforms.
func hkCovLockFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Symlink(filepath.Base(path), path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// hkCovSeedJob places a queue job directly into the pending directory, avoiding
// plan-time control-path validation for tests that only exercise the worker.
func hkCovSeedJob(t *testing.T, dispatcher Dispatcher, job queuedJob) {
	t.Helper()
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	hkCovWriteJob(t, dispatcher.pendingDir(), job)
}
