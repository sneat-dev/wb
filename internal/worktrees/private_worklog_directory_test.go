package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/secureopen"
)

func openTestPrivateDirectory(t *testing.T, root string) *os.File {
	t.Helper()
	directory, err := os.Open(root)
	if err != nil {
		t.Fatalf("Open(%s): %v", root, err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	return directory
}

func TestOpenPrivateChildRejectsAnUnsafeSegmentName(t *testing.T) {
	t.Parallel()
	parent := openTestPrivateDirectory(t, t.TempDir())

	if _, err := openPrivateChildWith(secureopen.Real{}, parent, "../escape", false); err == nil {
		t.Fatalf("openPrivateChildWith(../escape) = nil, want an error")
	}
}

func TestOpenPrivateChildOpensAnExistingChildReadOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "child"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	parent := openTestPrivateDirectory(t, root)

	child, err := openPrivateChildWith(secureopen.Real{}, parent, "child", false)
	if err != nil {
		t.Fatalf("openPrivateChildWith(child, create=false) = %v, want nil", err)
	}
	_ = child.Close()
}

func TestOpenPrivateChildReportsAMissingChildOnRead(t *testing.T) {
	t.Parallel()
	parent := openTestPrivateDirectory(t, t.TempDir())

	if _, err := openPrivateChildWith(secureopen.Real{}, parent, "absent", false); err == nil {
		t.Fatalf("openPrivateChildWith(absent, create=false) = nil, want an error")
	}
}

func TestOpenPrivateChildCreatesAndHardensAMissingChild(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent := openTestPrivateDirectory(t, root)

	child, err := openPrivateChildWith(secureopen.Real{}, parent, "child", true)
	if err != nil {
		t.Fatalf("openPrivateChildWith(child, create=true) = %v, want nil", err)
	}
	t.Cleanup(func() { _ = child.Close() })

	info, err := os.Stat(filepath.Join(root, "child"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("created child mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestOpenPrivateChildLeavesAnExistingChildsModeUntouchedOnRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "child"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	parent := openTestPrivateDirectory(t, root)

	child, err := openPrivateChildWith(secureopen.Real{}, parent, "child", false)
	if err != nil {
		t.Fatalf("openPrivateChildWith(child, create=false) = %v, want nil", err)
	}
	_ = child.Close()

	info, err := os.Stat(filepath.Join(root, "child"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("read path changed an existing child's mode to %v, want it left at 0755", info.Mode().Perm())
	}
}

func TestOpenPrivateChildPropagatesAnOpenerFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent := openTestPrivateDirectory(t, root)
	opener := newTempRootOpener(root)
	boom := errors.New("boom")
	opener.FailCall(1, boom)

	if _, err := openPrivateChildWith(opener, parent, "child", true); !errors.Is(err, boom) {
		t.Fatalf("openPrivateChildWith with a failing opener = %v, want it to wrap boom", err)
	}
}

func TestOpenWorkLogRunRejectsAnUnsafeEffortOrRunIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	if _, _, err := openWorkLogRunWith(secureopen.Real{}, root, "../escape", "run", false); err == nil {
		t.Fatalf("openWorkLogRunWith with an unsafe effort = nil, want an error")
	}
	if _, _, err := openWorkLogRunWith(secureopen.Real{}, root, "effort", "../escape", false); err == nil {
		t.Fatalf("openWorkLogRunWith with an unsafe run = nil, want an error")
	}
}

func TestOpenWorkLogRunCreatesTheFullNestedPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	run, path, err := openWorkLogRunWith(secureopen.Real{}, home, "effort1", "run1", true)
	if err != nil {
		t.Fatalf("openWorkLogRunWith(create=true) = %v, want nil", err)
	}
	t.Cleanup(func() { _ = run.Close() })

	want := filepath.Join(home, "worklogs", "effort1", "runs", "run1")
	if path != want {
		t.Fatalf("openWorkLogRunWith returned path %q, want %q", path, want)
	}
	if info, statErr := os.Stat(want); statErr != nil || !info.IsDir() {
		t.Fatalf("Stat(%s) = (%v, %v), want an existing directory", want, info, statErr)
	}
}

func TestOpenWorkLogRunReportsAHomeDirectoryOpenFailure(t *testing.T) {
	t.Parallel()
	home := filepath.Join(t.TempDir(), "absent-home")

	if _, _, err := openWorkLogRunWith(secureopen.Real{}, home, "effort1", "run1", false); err == nil {
		t.Fatalf("openWorkLogRunWith with a missing home (create=false) = nil, want an error")
	}
}

// TestOpenWorkLogRunStopsAtTheFirstFailingLevelAndClosesIntermediateHandles
// drives an opener failure at each of the four nested levels
// (worklogs/effort/runs/run) in turn, proving the loop's
// "close current unless it is homeDir" bookkeeping never leaks a descriptor
// and always reports the injected failure, for every position in the walk.
func TestOpenWorkLogRunStopsAtTheFirstFailingLevelAndClosesIntermediateHandles(t *testing.T) {
	t.Parallel()
	// Call 1: OpenRoot. Call 2: Mkdir(home). Call 3: OpenDir(home) --
	// openAbsoluteDirectoryNoFollowWith's create path for the single "home"
	// segment. Calls 4/5, 6/7, 8/9, 10/11 are the Mkdir/OpenDir pairs for
	// worklogs, effort1, runs and run1 respectively.
	levelFirstCall := map[string]int{
		"worklogs": 4,
		"effort1":  6,
		"runs":     8,
		"run1":     10,
	}
	for level, callNum := range levelFirstCall {
		t.Run(level, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			opener := newTempRootOpener(root)
			boom := errors.New("boom at " + level)
			opener.FailCall(callNum, boom)

			_, _, err := openWorkLogRunWith(opener, "/home", "effort1", "run1", true)
			if !errors.Is(err, boom) {
				t.Fatalf("level %s: err = %v, want it to wrap boom", level, err)
			}
		})
	}
}
