//go:build !windows && !darwin

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// startDaemonProcess refuses a Go test binary before spawning anything
// (sneat-dev/wb#622: a test previously reached this function for real and
// took a real daemon down).
func TestStartDaemonProcessRefusesATestBinary(t *testing.T) {
	if _, err := startDaemonProcess(os.Args[0], nil, filepath.Join(t.TempDir(), "daemon.log")); err == nil {
		t.Fatal("starting the test binary itself must be refused")
	}
}

// Off macOS there is no fixed-label launchd service for a start to remove, so
// the other-root check never refuses.
func TestDaemonCheckOtherRootNeverRefusesOffMacOS(t *testing.T) {
	if err := daemonCheckOtherRoot(t.TempDir(), false); err != nil {
		t.Fatalf("daemonCheckOtherRoot = %v", err)
	}
}
