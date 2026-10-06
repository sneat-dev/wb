package fleetinspect

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetdiscovery"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/layout"
	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/sneat-dev/wb/internal/repostatus"
	"github.com/sneat-dev/wb/internal/wbexec"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func testService() *Service {
	resolver := fleetdiscovery.New(io.Discard)
	return New(Dependencies{Select: reposelection.Select, Inspect: repostatus.InspectTargets, Scan: discover.ScanLocal, Layout: layout.Counts, Worktrees: worktrees.List, Remote: resolver.Discover, Owners: resolver.Owners, Sync: fleetsync.Sync, Hooks: hooks.Check, Executable: wbexec.HookExecutable})
}
func initTestRepository(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", path, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	return path
}
func projectsFixture(t *testing.T, repos ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, repo := range repos {
		initTestRepository(t, filepath.Join(root, filepath.FromSlash(repo)))
	}
	return root
}
