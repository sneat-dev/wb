//go:build e2e && !windows

package worktrees

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

//nolint:paralleltest // HOME changes exclusively in the isolated self-reexec child; parent is parallel.
func TestE2EShellResidueNextLogicalInventoryRetainsNativeHomeRefusal(t *testing.T) {
	const marker = "WB_SHELL_RESIDUE_NEXT_HOME_CHILD"
	if os.Getenv(marker) == "1" {
		projects, root := setUpShellRetirementFixture(t)
		task := filepath.Join(root, "terminal-shell")
		if err := os.Mkdir(task, 0700); err != nil {
			t.Fatal(err)
		}
		resolution, err := wbhome.Resolve(projects)
		if err != nil {
			t.Fatal(err)
		}
		loop := filepath.Join(t.TempDir(), "native-home-loop")
		if err := os.Symlink(loop, loop); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", loop)
		if _, err := wbhome.Resolve(projects); err == nil {
			t.Fatal("native HOME loop did not refuse subsequent inventory resolution")
		}
		outcome, err := retireResolvedTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: projects, Apply: true}, resolution)
		if err != nil || len(outcome.Results) != 1 || outcome.Results[0].Eligible || outcome.Results[0].Applied || !strings.Contains(outcome.Results[0].Reason, "inspect logical task shell:") {
			t.Fatalf("failed corroboration granted retirement: %+v %v", outcome, err)
		}
		if entries, err := os.ReadDir(task); err != nil || len(entries) != 0 {
			t.Fatalf("refusal changed empty task shell: %v %v", entries, err)
		}
		return
	}
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestE2EShellResidueNextLogicalInventoryRetainsNativeHomeRefusal$")
	command.Env = append(os.Environ(), marker+"=1")
	if directory := wtLifeCovCoverDir(); testing.CoverMode() != "" && directory != "" {
		command.Args = append(command.Args, "-test.gocoverdir="+directory)
		command.Env = append(command.Env, "GOCOVERDIR="+directory)
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native home child=%v\n%s", err, output)
	}
}
