package worktrees

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// TestInventoryReportsEachCheckoutsPlacement encodes the inventory half of
// projects-root-layout#ac:existing-placements-remain-operable: an operator must
// be able to see which layout each row uses, and asking for the inventory must
// not move any of them.
func TestInventoryReportsEachCheckoutsPlacement(t *testing.T) {
	ctx := context.Background()
	for _, testCase := range []struct {
		name      string
		mode      string
		want      string
		wantLocal bool
	}{
		{name: "central default", mode: "", want: "central"},
		{name: "repository local", mode: StoreModeRepositoryLocal, want: "repository-local", wantLocal: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newStoreModeFixture(t, "app")
			if testCase.mode != "" {
				fixture.selectStoreMode(t, testCase.mode)
			}
			created, err := Create(ctx, []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot, Operation: "placement-row", WorkLog: WorkLogOptions{Model: "unknown"},
			})
			if err != nil || len(created) != 1 {
				t.Fatalf("create = %#v, err=%v", created, err)
			}
			checkout := created[0].WorktreeDir

			listed, err := List(ctx, ListOptions{ProjectsRoot: fixture.projectsRoot, Task: "placement-row"})
			if err != nil || len(listed) != 1 {
				t.Fatalf("list = %#v, err=%v", listed, err)
			}
			row := listed[0]
			if row.Placement != testCase.want {
				t.Fatalf("placement = %q, want %q", row.Placement, testCase.want)
			}
			if row.Local != testCase.wantLocal {
				t.Fatalf("local = %t, want %t", row.Local, testCase.wantLocal)
			}
			// The placement is reported, not applied: reading the inventory
			// must leave the checkout exactly where it was.
			if row.WorktreeDir != checkout {
				t.Fatalf("inventory moved the checkout: %q became %q", checkout, row.WorktreeDir)
			}
		})
	}
}

// TestListResultPlacementNamesEachRecognizedLayout covers the placements the
// inventory walk can hand to a row, including the two historic home-relative
// layouts that must stay readable in place and must NOT be reported as the
// central store.
func TestListResultPlacementNamesEachRecognizedLayout(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		layout   wbhome.Layout
		external bool
		want     string
	}{
		{name: "projects-root store", layout: wbhome.Layout{WorktreesRoot: filepath.Join("/projects", ".worktrees")}, want: "central"},
		{name: "configured store root", layout: wbhome.Layout{WorktreesRoot: filepath.Join("/mnt", "wb-worktrees")}, want: "central"},
		{name: "canonical local", layout: wbhome.Layout{WorktreesRoot: filepath.Join("/projects", "github.com", "acme", "app", ".worktrees"), Local: true}, want: "repository-local"},
		{name: "retired user home", layout: wbhome.Layout{Home: filepath.Join("/home", "alex", ".wb"), WorktreesRoot: filepath.Join("/home", "alex", ".wb", "worktrees"), Legacy: true}, want: "legacy"},
		{
			// wbhome.Resolve registers <root>/.wb/worktrees — the state
			// namespace holding retired stages and locks — as its own readable
			// layout. It is a sibling of the store <root>/.worktrees, so a
			// checkout found there is not in the central store, and saying it
			// is would send an operator to a directory it is not in.
			name:   "historic projects-root home",
			layout: wbhome.Layout{Home: filepath.Join("/projects", ".wb"), WorktreesRoot: filepath.Join("/projects", ".wb", "worktrees")},
			want:   "legacy",
		},
		{name: "adopted external", layout: wbhome.Layout{WorktreesRoot: filepath.Join("/tmp", "borrowed"), Local: true}, external: true, want: "external"},
	} {
		if got := listResultPlacement(testCase.layout, testCase.external); got != testCase.want {
			t.Fatalf("%s placement = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}
