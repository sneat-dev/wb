//go:build windows

package daemonruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/daemonlifecycle"
	"golang.org/x/sys/windows"
)

func TestWindowsDaemonLifecycleLocksUseRealPaths(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	controller := NewController(daemonTestDependencies(t, root), root)

	releaseState, err := controller.AcquireStateLock()
	if err != nil {
		t.Fatalf("acquire state lock through Windows ACL consumer: %v", err)
	}
	releaseState()
	if err := daemonlifecycle.ValidateOwnerOnly(filepath.Join(root, ".wb", "runtime")); err != nil {
		t.Fatalf("state lock left daemon runtime inheritable: %v", err)
	}

	releaseLifecycle, err := controller.lifecycleLock()
	if err != nil {
		t.Fatalf("acquire lifecycle lock through Windows ACL consumer: %v", err)
	}
	releaseLifecycle()
	if _, _, err := prepareDaemonFileBridge(root); err != nil {
		t.Fatalf("prepare bridge after lifecycle lock initialized the same runtime: %v", err)
	}

	for _, path := range []string{
		mustDaemonPath(t, daemonStateLockPath, root),
		mustDaemonPath(t, daemonLifecycleLockPath, root),
		mustDaemonPath(t, daemonLifecycleOwnerPath, root),
	} {
		if err := daemonlifecycle.ValidateOwnerOnly(path); err != nil {
			t.Fatalf("private lifecycle path %s: %v", path, err)
		}
	}
}

func TestWindowsDaemonFileBridgeRejectsUntrustedAncestorMutation(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	setWindowsTestDACL(t, root, "D:P(A;;GA;;;"+user.User.Sid.String()+")(A;;DC;;;WD)")

	if _, _, err := prepareDaemonFileBridge(root); err == nil || !strings.Contains(err.Error(), "untrusted Windows principal") {
		t.Fatalf("untrusted DELETE_CHILD ancestor error = %v", err)
	}
}

func TestWindowsDaemonFileBridgeAcceptsInheritedAncestorsAndProtectsRuntime(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	setWindowsTestDACL(t, root, "D:P(A;OICI;GA;;;"+user.User.Sid.String()+")(A;OICI;GA;;;SY)(A;OICI;GA;;;BA)(A;OICI;GR;;;WD)")
	if err := os.Mkdir(filepath.Join(root, ".wb"), 0o700); err != nil {
		t.Fatal(err)
	}
	// This mirrors an ordinary inherited Windows ACL: the current user,
	// LocalSystem, and Administrators may mutate it, while Everyone may read.
	// It is a safe project ancestor but not an owner-only private runtime.
	for _, ancestor := range []string{root, filepath.Join(root, ".wb")} {
		if err := daemonlifecycle.ValidateOwnerOnly(ancestor); err == nil {
			t.Fatalf("Windows test fixture ancestor %s unexpectedly has an owner-only inherited ACL", ancestor)
		}
	}

	requests, responses, err := prepareDaemonFileBridge(root)
	if err != nil {
		t.Fatalf("prepare bridge below inherited project ancestors: %v", err)
	}
	key, err := daemonFileBridgeKey(root, true)
	if err != nil {
		t.Fatalf("create bridge key below protected runtime: %v", err)
	}
	envelope := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: "acl-probe", SchedulerGeneration: "1"}
	envelope.PayloadSHA256 = daemonFilePayloadDigest(envelope)
	envelope.MAC = daemonFileEnvelopeMAC(envelope, key)
	if err := writeDaemonFileEnvelope(requests, envelope.ID, envelope); err != nil {
		t.Fatalf("write protected bridge envelope: %v", err)
	}
	if _, err := readDaemonFileEnvelope(filepath.Join(requests, envelope.ID+".json")); err != nil {
		t.Fatalf("read protected bridge envelope: %v", err)
	}

	for _, path := range []string{
		filepath.Join(root, ".wb", "runtime"),
		mustDaemonPath(t, daemonFileBridgeDirectory, root),
		requests,
		responses,
		mustDaemonPath(t, daemonFileBridgeKeyPath, root),
	} {
		if err := daemonlifecycle.ValidateOwnerOnly(path); err != nil {
			t.Fatalf("private bridge path %s: %v", path, err)
		}
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
