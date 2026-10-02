package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestLifecycleNextCanonicalDiscoveryRetainsVanishedEntryDiagnostic(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	owner := filepath.Join(projects, "acme")
	repository := filepath.Join(owner, "app")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	observed := false
	readDir := func(path string) ([]os.DirEntry, error) {
		entries, err := os.ReadDir(path)
		if path == owner && err == nil {
			observed = true
			if err := os.Remove(repository); err != nil {
				t.Fatal(err)
			}
		}
		return entries, err
	}
	layouts, diagnostics := discoverCanonicalLocalWorktreeLayoutsWithReadDir(context.Background(), projects, "", readDir)
	if !observed || len(layouts) != 0 || len(diagnostics) != 1 || diagnostics[0].Path != repository || !strings.Contains(diagnostics[0].Message, "inspect canonical repository entry") {
		t.Fatalf("discovery=%+v %+v observed=%v", layouts, diagnostics, observed)
	}
	if entries, err := os.ReadDir(owner); err != nil || len(entries) != 0 {
		t.Fatalf("discovery replaced vanished entry: %v %v", entries, err)
	}
}

func TestLifecycleNextLogicalAliasScanKeepsVanishedNativeNamesSeparate(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"task", "owner"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			task := filepath.Join(root, "session-resume-native")
			owner := filepath.Join(task, "acme")
			if err := os.MkdirAll(owner, 0o700); err != nil {
				t.Fatal(err)
			}
			observed := false
			readDir := func(path string) ([]os.DirEntry, error) {
				entries, err := os.ReadDir(path)
				if err == nil && ((boundary == "task" && path == root) || (boundary == "owner" && path == task)) {
					observed = true
					if boundary == "task" {
						if err := os.Remove(owner); err != nil {
							t.Fatal(err)
						}
						if err := os.Remove(task); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := os.Remove(owner); err != nil {
							t.Fatal(err)
						}
					}
				}
				return entries, err
			}
			tasks, err := resolveLogicalCleanupTasksWithReadDir([]wbhome.Layout{{WorktreesRoot: root}}, []string{"logical"}, readDir)
			if err != nil || !observed || !reflect.DeepEqual(tasks, []string{"logical"}) {
				t.Fatalf("logical alias=%v %v observed=%v", tasks, err, observed)
			}
			if _, err := os.Stat(owner); !os.IsNotExist(err) {
				t.Fatalf("scan republished absent owner: %v", err)
			}
		})
	}
}
