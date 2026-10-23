//go:build !windows

package daemonruntime

import (
	"os"
	"testing"
)

// cwWtLockFixture makes a daemon fixture root with a secure runtime directory
// already in place, and returns the controller for it.
func cwWtLockFixture(t *testing.T) (string, Controller, Dependencies) {
	t.Helper()
	root := cwWtDaemonRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	if err := secureDaemonRuntime(root); err != nil {
		t.Fatalf("secureDaemonRuntime: %v", err)
	}
	return root, controller, deps
}

func cwWtWriteDaemonFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
