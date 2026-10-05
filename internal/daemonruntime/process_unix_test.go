//go:build !windows && !darwin

package daemonruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStartDaemonProcessRefusesATestBinary(t *testing.T) {
	t.Parallel()
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	if _, err := native.startDaemonProcess(os.Args[0], nil, filepath.Join(t.TempDir(), "daemon.log")); err == nil {
		t.Fatal("starting the test binary itself must be refused")
	}
}

func TestDaemonCheckOtherRootNeverRefusesOffMacOS(t *testing.T) {
	t.Parallel()
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	if err := native.daemonCheckOtherRoot(t.TempDir(), false); err != nil {
		t.Fatalf("daemonCheckOtherRoot = %v", err)
	}
}

func TestStartDaemonProcessObservesTestModeBeforeAnyFileEffect(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	logPath := filepath.Join(root, "not-created", "child.log")
	if pid, err := defaultNativeOperations().startDaemonProcess(filepath.Join(root, "ordinary-child"), nil, logPath); pid != 0 || err == nil {
		t.Fatalf("observed test mode pid=%d err=%v", pid, err)
	}
	if _, err := os.Stat(filepath.Dir(logPath)); !os.IsNotExist(err) {
		t.Fatalf("test refusal created log directory: %v", err)
	}
}
