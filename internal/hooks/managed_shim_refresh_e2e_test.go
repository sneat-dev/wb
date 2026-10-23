//go:build e2e

package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2EManagedShimRefreshRetainsPostReadStatFailure(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	config := filepath.Join(t.TempDir(), "hooks.yaml")
	if err := os.WriteFile(config, []byte("version: 1\nprofiles:\n  exclude: [worktree]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	executable := testWBExecutable(t, "wb")
	projects := t.TempDir()
	result, err := Apply(ApplyOptions{RepoPath: repo, ConfigPath: config, WBExecutable: executable, ProjectsRoot: projects})
	if err != nil {
		t.Fatal(err)
	}
	removed := ""
	changed, err := refreshManagedShimsObserved(repo, config, executable, projects, func(path string) {
		removed = path
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	})
	if changed || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "inspect managed hook") {
		t.Fatalf("refresh changed=%v error=%v", changed, err)
	}
	if removed == "" || !strings.HasPrefix(removed, result.Report.ManagedPath+string(filepath.Separator)) {
		t.Fatalf("removed path=%q managed=%q", removed, result.Report.ManagedPath)
	}
	if _, err := os.Stat(removed); !os.IsNotExist(err) {
		t.Fatalf("removed hook recreated after refusal: %v", err)
	}
}
