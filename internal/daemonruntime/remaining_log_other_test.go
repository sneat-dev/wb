//go:build !darwin && !windows

package daemonruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/daemon"
)

//nolint:paralleltest // Mutates process-wide HOME to exercise the native WB-home refusal.
func TestDaemonLogPathRefusesANativeHomeAncestorFile(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(t.TempDir(), "home-file")
	if err := os.WriteFile(home, []byte("private home blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(home, "child"))
	if path, err := daemonLogPath(root); path != "" || err == nil || !strings.Contains(err.Error(), "resolve WB home") {
		t.Fatalf("log path=%q err=%v", path, err)
	}
}

//nolint:paralleltest // Mutates process-wide HOME after the genuine starting-state save and restores it before receipt observation.
func TestControllerLateLogRefusalPreservesTheDurableStartingReceipt(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	provenance, err := controller.Provenance()
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "home-file")
	if err := os.WriteFile(home, []byte("private home blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	priorHome, hadHome := os.LookupEnv("HOME")
	restore := func() {
		if hadHome {
			_ = os.Setenv("HOME", priorHome)
		} else {
			_ = os.Unsetenv("HOME")
		}
	}
	t.Cleanup(restore)
	save := controller.state.save
	calls := 0
	var recorded daemon.State
	controller.state.save = func(state daemon.State) error {
		if err := save(state); err != nil {
			return err
		}
		calls++
		recorded = state
		t.Setenv("HOME", filepath.Join(home, "child"))
		return nil
	}
	starts := 0
	controller.deps.Start = func(string, []string, string) (int, error) {
		starts++
		return 0, errors.New("start must not run after log refusal")
	}
	_, err = controller.launch(context.Background(), nil, DefaultListen, provenance, "start", false)
	restore()
	if err == nil || !strings.Contains(err.Error(), "resolve WB home") || calls != 1 || starts != 0 || recorded.Status != daemon.StatusStarting {
		t.Fatalf("err=%v saves=%d starts=%d recorded=%+v", err, calls, starts, recorded)
	}
	stored, found, err := controller.LoadState()
	if err != nil || !found || !reflect.DeepEqual(stored, recorded) {
		t.Fatalf("stored=%+v found=%v err=%v want=%+v", stored, found, err, recorded)
	}
}
