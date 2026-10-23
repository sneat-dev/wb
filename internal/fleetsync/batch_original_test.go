package fleetsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

func TestCwDepsRunSyncPlainSyncsLocalClonesInParallel(t *testing.T) {
	t.Parallel()
	projects := cwCovProjectsRoot(t, "acme/one", "acme/two", "beta/three")
	repos, err := discover.ScanLocal(projects)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 3 {
		t.Fatalf("scan found %d repositories", len(repos))
	}
	results := Batch(context.Background(), repos, BatchOptions{ProjectsRoot: projects, Workers: 2, DryRun: true}, BatchObserver{})
	if len(results) != 3 {
		t.Fatalf("results = %+v, want one per repository", results)
	}
	for _, result := range results {
		if result.Status == Failed {
			t.Errorf("%s failed unexpectedly: %v", result.Repo.Slug(), result.Err)
		}
	}
}
func cwCovProjectsRoot(t *testing.T, repos ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, slug := range repos {
		initTestRepository(t, filepath.Join(root, filepath.FromSlash(slug)))
	}
	return root
}
func initTestRepository(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", path, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("init %s: %v\n%s", path, err, output)
	}
	return path
}
