package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimePathResolutionRefusesANativeNonDirectoryAncestor(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(blocker, "projects")
	for name, resolve := range map[string]func(string) (string, error){"state": StatePath, "state-lock": daemonStateLockPath, "lifecycle-lock": daemonLifecycleLockPath, "owner": daemonLifecycleOwnerPath, "socket": daemonLocalAddress, "bridge": daemonFileBridgeDirectory} {
		if path, err := resolve(root); err == nil || path != "" {
			t.Errorf("%s resolved invalid ancestor: %q,%v", name, path, err)
		}
	}
	controller := NewController(daemonTestDependencies(t, t.TempDir()), root)
	if _, err := controller.AcquireStateLock(); err == nil {
		t.Error("state lock resolved invalid ancestor")
	}
	if _, _, _, err := controller.openLifecycleLock(true); err == nil {
		t.Error("lifecycle lock resolved invalid ancestor")
	}
	if _, err := controller.lifecycleOwnerPID(nil); err == nil {
		t.Error("owner resolved invalid ancestor")
	}
	if err := controller.writeLifecycleOwnerPID(1); err == nil {
		t.Error("owner writer resolved invalid ancestor")
	}
	if _, err := controller.RecoverLifecycleLock(context.Background(), false); err == nil {
		t.Error("recovery resolved invalid ancestor")
	}
	if _, err := ResolveLocation(root); err == nil {
		t.Error("location resolved invalid ancestor")
	}
	if _, err := ListenLocal(root); err == nil {
		t.Error("listener resolved invalid ancestor")
	}
	if _, err := LocalHTTPClient(root, "token"); err == nil {
		t.Error("client resolved invalid ancestor")
	}
	if err := secureBridgeRuntime(root); err == nil {
		t.Error("bridge runtime resolved invalid ancestor")
	}
	if _, err := newDaemonFileBridgeHTTPClient(root, "generation"); err == nil {
		t.Error("bridge client resolved invalid ancestor")
	}

	data, err := os.ReadFile(blocker)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("ancestor changed: %q,%v", data, err)
	}
}
