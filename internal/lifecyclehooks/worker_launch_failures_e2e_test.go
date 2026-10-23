//go:build e2e

package lifecyclehooks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestE2EWorkerLaunchRetainsExecutableNullAndNativeStartFailures(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"executable", "null", "start"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			failure := errors.New("executable identity unavailable")
			executable := os.Executable
			if phase == "executable" {
				executable = func() (string, error) { return "", failure }
			}
			if phase == "start" {
				executable = func() (string, error) { return filepath.Join(t.TempDir(), "missing-worker"), nil }
			}
			open := func() (*os.File, error) { return os.OpenFile(os.DevNull, os.O_RDWR, 0) }
			if phase == "null" {
				open = func() (*os.File, error) { return os.OpenFile(filepath.Join(t.TempDir(), "missing-null"), os.O_RDWR, 0) }
			}
			err := launchWorkerWithIO(WorkerRequest{StateDir: t.TempDir()}, executable, open)
			if err == nil {
				t.Fatal("failed worker launch accepted")
			}
			if phase == "executable" && !errors.Is(err, failure) {
				t.Fatalf("executable cause=%v", err)
			}
			if phase != "executable" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("native launch cause=%v", err)
			}
		})
	}
}
