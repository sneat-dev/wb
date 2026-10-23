package integration

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/canonicalrescue"
	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/testenv"
	"os"
	"path/filepath"
	"testing"
)

func cwWtDirtyCanonicalClone(t *testing.T) (projects, clone string) {
	t.Helper()
	seeds := t.TempDir()
	projects = t.TempDir()
	clone = filepath.Join(projects, "acme", "app")
	testenv.CloneWithOrigin(t, seeds, "app", clone)
	if err := os.WriteFile(filepath.Join(clone, "wip.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return
}

type endRescueFailWriter struct{}

func (*endRescueFailWriter) Write([]byte) (int, error) { return 0, errors.New("writer failed") }
func TestCwWtRunFleetRescueReportFailurePropagation(t *testing.T) {
	t.Parallel()
	projects, _ := cwWtDirtyCanonicalClone(t)

	command := cmdworktree.NewRescue(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: projects} }}, cmdworktree.RescueOperations{Inspect: canonicalrescue.Inspect, Capture: canonicalrescue.Capture, Push: canonicalrescue.Push, Restore: canonicalrescue.Restore, ScanLocal: discover.ScanLocal})
	command.SetContext(context.Background())
	command.SetOut(&endRescueFailWriter{})
	command.SetArgs([]string{"--fleet"})
	if err := command.Execute(); err == nil {
		t.Fatal("runFleetRescueReport did not propagate the write failure")
	}
}
