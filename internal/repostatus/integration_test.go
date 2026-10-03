package repostatus

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sneat-dev/wb/internal/reposelection"
)

func TestInspectionCallsConcurrentProgressForRealGitStates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cleanRepo := initTestRepository(t, filepath.Join(root, "clean"))
	dirtyRepo := initTestRepository(t, filepath.Join(root, "dirty"))
	if err := os.WriteFile(filepath.Join(dirtyRepo, "notes.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The completion sink is called from the parallel workers, so the slice
	// needs its own lock or the appends race and drop rows.
	var completedMu sync.Mutex
	var completed []string
	reports := InspectTargets([]reposelection.Target{
		{Repository: "acme/clean", Path: cleanRepo},
		{Repository: "acme/dirty", Path: dirtyRepo},
		{Repository: "acme/missing", Path: filepath.Join(root, "absent")},
	}, 2, func(target reposelection.Target, info Row) {
		completedMu.Lock()
		defer completedMu.Unlock()
		completed = append(completed, target.Repository+":"+info.Status)
	})
	if len(reports) != 3 {
		t.Fatalf("reports = %+v", reports)
	}
	byRepo := map[string]string{}
	for _, report := range reports {
		byRepo[report.Repository] = report.Status
	}
	if byRepo["acme/clean"] != "clean" || byRepo["acme/dirty"] != "attention" || byRepo["acme/missing"] != "error" {
		t.Fatalf("statuses = %+v", byRepo)
	}
	if len(completed) != 3 {
		t.Fatalf("completion callbacks = %v", completed)
	}
	// A nil completion sink is tolerated.
	if reports := InspectTargets([]reposelection.Target{{Repository: "acme/clean", Path: cleanRepo}}, 1, nil); len(reports) != 1 {
		t.Fatalf("nil-progress reports = %+v", reports)
	}
	if reports := InspectTargets(nil, 2, nil); len(reports) != 0 {
		t.Fatalf("no targets = %+v", reports)
	}
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
