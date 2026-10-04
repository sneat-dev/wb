//go:build darwin

package daemonruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/daemon"
)

func TestNativeLaunchdObserversPreserveFailureAndBoundedPresence(t *testing.T) {
	native := defaultNativeOperations()
	native.runLaunchctl = func(...string) ([]byte, error) { return nil, errors.New("private launchd observer refused") }
	if pid, ok := native.launchdPID("private"); ok || pid != 0 {
		t.Fatalf("pid=%d ok=%v", pid, ok)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "launchctl"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if present, label := native.daemonSupervisorPresent(daemon.SupervisorLaunchd, "private"); !present || label != "private" {
		t.Fatalf("present=%v label=%s", present, label)
	}
}

func TestNativeLogResolutionFailurePrecedesChildLaunch(t *testing.T) {
	root := cwWtDaemonRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	provenance, err := controller.Provenance()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "")
	if _, err := DaemonStartLogPath(root); err == nil {
		t.Fatal("empty HOME accepted")
	}
	controller.deps.Start = func(string, []string, string) (int, error) {
		t.Fatal("child launched after log refusal")
		return 0, nil
	}
	if _, err := controller.launch(context.Background(), nil, DefaultListen, provenance, "start", false); err == nil {
		t.Fatal("missing log home accepted")
	}
}
