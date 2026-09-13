//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/strongo/cli-helpers/daemonlifecycle"
)

// This is a consumer-level Windows regression test for cli-helpers' named-path
// ACL implementation. ProtectOwnerOnlyFile uses file.Name(), so both WB lock
// handles must retain the actual filesystem path rather than a display label.
func TestWindowsDaemonLifecycleLocksUseRealPaths(t *testing.T) {
	root := t.TempDir()
	controller := newDaemonController(daemonTestDependencies(t, root), root)

	releaseState, err := controller.stateLock()
	if err != nil {
		t.Fatalf("acquire state lock through Windows ACL consumer: %v", err)
	}
	releaseState()

	releaseLifecycle, err := controller.lifecycleLock()
	if err != nil {
		t.Fatalf("acquire lifecycle lock through Windows ACL consumer: %v", err)
	}
	releaseLifecycle()

	for _, path := range []string{
		daemonStateLockPath(root),
		daemonLifecycleLockPath(root),
		daemonLifecycleOwnerPath(root),
	} {
		if err := daemonlifecycle.ValidateOwnerOnly(path); err != nil {
			t.Fatalf("private lifecycle path %s: %v", path, err)
		}
	}
}

func TestWindowsDaemonFileBridgeAcceptsInheritedAncestorsAndProtectsRuntime(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".wb"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Windows temp directories normally carry the runner's inherited ACL,
	// including principals other than the current user. That is a safe project
	// ancestor but deliberately not an owner-only private runtime directory.
	if err := daemonlifecycle.ValidateOwnerOnly(root); err == nil {
		t.Fatal("Windows test fixture unexpectedly has an owner-only inherited root ACL")
	}

	requests, responses, err := prepareDaemonFileBridge(root)
	if err != nil {
		t.Fatalf("prepare bridge below inherited project ancestors: %v", err)
	}
	if _, err := daemonFileBridgeKey(root, true); err != nil {
		t.Fatalf("create bridge key below protected runtime: %v", err)
	}

	for _, path := range []string{
		filepath.Join(root, ".wb", "runtime"),
		daemonFileBridgeDirectory(root),
		requests,
		responses,
		daemonFileBridgeKeyPath(root),
	} {
		if err := daemonlifecycle.ValidateOwnerOnly(path); err != nil {
			t.Fatalf("private bridge path %s: %v", path, err)
		}
	}
}
