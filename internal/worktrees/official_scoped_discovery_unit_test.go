package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfficialScopedDiscoveryReportsInventoryObstructionWithoutInventingAuthority(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "worklogs"), []byte("obstruction"), 0600); err != nil {
		t.Fatal(err)
	}
	results, diagnostics := listTaskScopedClaimedRegistryWorktrees(context.Background(), root, home, nil, map[string]bool{"selected": true}, "main", "", "", false, 1, nil, inspectPolicy{})
	if len(results) != 0 || len(diagnostics) != 1 || diagnostics[0].Task != "" || diagnostics[0].DiscoveryScope != "unattributed" || !strings.Contains(diagnostics[0].Message, "read Work Log efforts") {
		t.Fatalf("%+v %+v", results, diagnostics)
	}
	if err := os.Remove(filepath.Join(home, "worklogs")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "worklogs", "selected"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "worklogs", "selected", "runs"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	results, diagnostics = listTaskScopedClaimedRegistryWorktrees(context.Background(), root, home, nil, map[string]bool{"selected": true}, "main", "", "", false, 1, nil, inspectPolicy{})
	if len(results) != 0 || len(diagnostics) != 1 || diagnostics[0].Task != "selected" || diagnostics[0].DiscoveryScope != "selected-legacy-namespace" {
		t.Fatalf("%+v %+v", results, diagnostics)
	}
	if _, err := activeWorkLogClaimAtPathWithReadNames(home, "missing", nil, func(d *os.File) ([]string, error) {
		names, e := d.Readdirnames(-1)
		if e == nil {
			for _, name := range names {
				if name == "selected" {
					if e = os.Rename(filepath.Join(home, "worklogs", name), filepath.Join(home, "parked")); e != nil {
						t.Fatal(e)
					}
				}
			}
		}
		return names, e
	}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("between-read removal hidden: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "worklogs", "bad name"), 0700); err != nil {
		t.Fatal(err)
	}
	results, diagnostics = listTaskScopedClaimedRegistryWorktrees(context.Background(), root, home, nil, map[string]bool{"selected": true}, "main", "", "", false, 1, nil, inspectPolicy{})
	if len(results) != 0 || len(diagnostics) != 0 {
		t.Fatalf("invalid unselected directory became authority: %+v %+v", results, diagnostics)
	}
	if claim, err := activeWorkLogClaimAtPath(home, "missing", nil); claim != nil || err != nil {
		t.Fatalf("unsafe namespace admitted: %+v %v", claim, err)
	}
	if err := os.MkdirAll(filepath.Join(home, "worklogs", "selected"), 0700); err != nil {
		t.Fatal(err)
	}
	results, diagnostics = listTaskScopedClaimedRegistryWorktrees(context.Background(), root, home, nil, map[string]bool{"selected": true}, "main", "", "", false, 1, nil, inspectPolicy{})
	if len(results) != 0 || len(diagnostics) != 0 {
		t.Fatalf("absent runs became authority/obstruction: %+v %+v", results, diagnostics)
	}
}
