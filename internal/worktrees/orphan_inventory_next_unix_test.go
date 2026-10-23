//go:build !windows

package worktrees

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestOrphanResidueResolvesRegisteredAliasWithoutMutation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "registered")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if got := inspectResidue(root, root, LayoutCurrent, "task", "acme/app", alias, map[string]bool{filepath.Clean(target): true}); got != nil {
		t.Fatalf("registered alias reported: %+v", got)
	}
	if got, err := os.Readlink(alias); err != nil || got != target {
		t.Fatalf("alias changed: %q %v", got, err)
	}
}

//nolint:paralleltest // The resolver reads HOME; the loop is confined to a private fixture.
func TestOrphanInventoryPropagatesNativeLayoutRefusal(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	loop := filepath.Join(home, ".wb")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	_, control := wbhome.Resolve(root)
	if control == nil {
		t.Fatal("native layout prerequisite did not refuse symlink loop")
	}
	report, err := Orphans(t.Context(), OrphanOptions{ProjectsRoot: root})
	// EvalSymlinks may allocate an errorString rather than return a PathError.
	// Compare the same native resolver's observed diagnostic/type, not a guessed errno.
	if err == nil || err.Error() != control.Error() || reflect.TypeOf(err) != reflect.TypeOf(control) || len(report.Families) != 0 || len(report.Residue) != 0 {
		t.Fatalf("resolution refusal=%+v %v control=%v", report, err, control)
	}
	if got, err := os.Readlink(loop); err != nil || got != loop {
		t.Fatalf("resolver mutated evidence: %s %v", got, err)
	}
}
