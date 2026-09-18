package worktrees

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/repopath"
)

// newHostLevelCleanupTaskFixture acquires a bare cleanup task descriptor
// rooted at a fresh temp directory, the same low-level entry point Cleanup
// itself uses (acquireCleanupTaskAtOrCreate), without any Git or Work Log
// machinery. That keeps these tests focused on openCleanupWorktree and
// removeEmptyParent's descriptor-anchored directory handling — the exact
// surface #594 broke — rather than re-proving the merge/eligibility pipeline
// integration tests elsewhere already cover.
func newHostLevelCleanupTaskFixture(t *testing.T) *cleanupTaskHandle {
	t.Helper()
	// acquireCleanupTaskAtOrCreate's create fallback derives its WB home as
	// the parent of worktreesRoot and then reopens "<home>/worktrees" itself
	// (see prepareOperationRoot), so worktreesRoot's own leaf name must
	// literally be "worktrees" for the two to resolve to the same directory.
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	task, err := acquireCleanupTaskAtOrCreate(worktreesRoot, "host-level-task")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = task.lock.release()
		task.close()
	})
	return task
}

// TestOpenCleanupWorktreeAcceptsHostLevelHierarchy proves the 3-segment
// <task>/<host>/<owner>/<repository> layout `wb worktree create` writes is
// opened descriptor-anchored, exactly like the existing 1- and 2-segment
// cases, instead of being refused as "unsupported hierarchy" (#594).
func TestOpenCleanupWorktreeAcceptsHostLevelHierarchy(t *testing.T) {
	task := newHostLevelCleanupTaskFixture(t)
	repoPath := filepath.Join(task.taskPath, "github.com", "acme", "app")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}

	handle, err := openCleanupWorktree(task, CleanupResult{ListResult: ListResult{WorktreeDir: repoPath}})
	if err != nil {
		t.Fatalf("openCleanupWorktree refused the host-level hierarchy: %v", err)
	}
	defer handle.close()

	if handle.ancestor == nil || handle.ancestorName != "github.com" || !handle.closeAncestor {
		t.Fatalf("handle did not track the host ancestor: %+v", handle)
	}
	if handle.parent == nil || handle.parentName != "acme" || !handle.closeParent || !handle.ownParent {
		t.Fatalf("handle did not track the owner parent: %+v", handle)
	}
	if err := handle.validate(); err != nil {
		t.Fatalf("freshly opened handle failed validation: %v", err)
	}
}

// TestCleanupRetiresHostLevelWorktreeAndBothEmptyAncestors is the mechanical
// core of the fix: once the checkout itself is gone, removeEmptyParent must
// retire <owner> and then <host> in turn, exactly mirroring what it already
// does for the legacy <task>/<owner>/<repository> layout's single parent.
func TestCleanupRetiresHostLevelWorktreeAndBothEmptyAncestors(t *testing.T) {
	task := newHostLevelCleanupTaskFixture(t)
	repoPath := filepath.Join(task.taskPath, "github.com", "acme", "app")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}

	handle, err := openCleanupWorktree(task, CleanupResult{ListResult: ListResult{WorktreeDir: repoPath}})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.close()

	// Simulate the worktree removal a real cleanup transaction performs
	// (git worktree remove / the residue repair path) before it calls
	// removeEmptyParent.
	if err := os.Remove(repoPath); err != nil {
		t.Fatal(err)
	}

	if err := handle.removeEmptyParent(nil); err != nil {
		t.Fatal(err)
	}

	ownerDir := filepath.Join(task.taskPath, "github.com", "acme")
	hostDir := filepath.Join(task.taskPath, "github.com")
	if _, statErr := os.Stat(ownerDir); !os.IsNotExist(statErr) {
		t.Fatalf("empty owner directory %s was not retired: %v", ownerDir, statErr)
	}
	if _, statErr := os.Stat(hostDir); !os.IsNotExist(statErr) {
		t.Fatalf("empty host directory %s was not retired: %v", hostDir, statErr)
	}
}

// TestCleanupLeavesNonEmptyHostLevelSiblingsIntact proves a sibling
// repository under the same <owner>, and a sibling owner under the same
// <host>, are both left in place — AT_REMOVEDIR's own ENOTEMPTY refusal, not
// a size check, is what protects them.
func TestCleanupLeavesNonEmptyHostLevelSiblingsIntact(t *testing.T) {
	task := newHostLevelCleanupTaskFixture(t)
	appPath := filepath.Join(task.taskPath, "github.com", "acme", "app")
	otherPath := filepath.Join(task.taskPath, "github.com", "acme", "other")
	toolPath := filepath.Join(task.taskPath, "github.com", "beta", "tool")
	for _, path := range []string{appPath, otherPath, toolPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	retire := func(path string) {
		t.Helper()
		handle, err := openCleanupWorktree(task, CleanupResult{ListResult: ListResult{WorktreeDir: path}})
		if err != nil {
			t.Fatal(err)
		}
		defer handle.close()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := handle.removeEmptyParent(nil); err != nil {
			t.Fatal(err)
		}
	}

	ownerDir := filepath.Join(task.taskPath, "github.com", "acme")
	hostDir := filepath.Join(task.taskPath, "github.com")

	// Retiring "app" must leave "acme" in place: its sibling "other" is still
	// there, and "acme" itself is still a sibling of "beta" under "github.com".
	retire(appPath)
	if _, statErr := os.Stat(ownerDir); statErr != nil {
		t.Fatalf("non-empty owner directory %s was wrongly removed: %v", ownerDir, statErr)
	}
	if _, statErr := os.Stat(otherPath); statErr != nil {
		t.Fatalf("sibling repository %s did not survive: %v", otherPath, statErr)
	}
	if _, statErr := os.Stat(hostDir); statErr != nil {
		t.Fatalf("non-empty host directory %s was wrongly removed: %v", hostDir, statErr)
	}

	// Retiring "other" now empties "acme", which is retired, but "github.com"
	// still holds the sibling owner "beta" and must survive.
	retire(otherPath)
	if _, statErr := os.Stat(ownerDir); !os.IsNotExist(statErr) {
		t.Fatalf("empty owner directory %s was not retired: %v", ownerDir, statErr)
	}
	if _, statErr := os.Stat(hostDir); statErr != nil {
		t.Fatalf("host directory %s with a surviving sibling owner was wrongly removed: %v", hostDir, statErr)
	}
	if _, statErr := os.Stat(toolPath); statErr != nil {
		t.Fatalf("sibling under another owner %s did not survive: %v", toolPath, statErr)
	}

	// Retiring the last repository finally empties both "beta" and
	// "github.com" in turn.
	retire(toolPath)
	if _, statErr := os.Stat(hostDir); !os.IsNotExist(statErr) {
		t.Fatalf("host directory %s was not retired once empty: %v", hostDir, statErr)
	}
}

// TestOpenCleanupWorktreeRefusesSymlinkAtHostOwnerOrRepositorySegment proves
// every level of the host-level hierarchy is opened with O_NOFOLLOW, so a
// symlink swapped in at the host, owner, or repository segment is refused
// rather than followed.
func TestOpenCleanupWorktreeRefusesSymlinkAtHostOwnerOrRepositorySegment(t *testing.T) {
	elsewhere := t.TempDir()

	t.Run("host", func(t *testing.T) {
		task := newHostLevelCleanupTaskFixture(t)
		target := filepath.Join(elsewhere, "host-target")
		if err := os.MkdirAll(filepath.Join(target, "acme", "app"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(task.taskPath, "github.com")); err != nil {
			t.Fatal(err)
		}
		if _, err := openCleanupWorktree(task, CleanupResult{ListResult: ListResult{
			WorktreeDir: filepath.Join(task.taskPath, "github.com", "acme", "app"),
		}}); err == nil {
			t.Fatal("a symlinked host segment was followed instead of refused")
		}
	})

	t.Run("owner", func(t *testing.T) {
		task := newHostLevelCleanupTaskFixture(t)
		target := filepath.Join(elsewhere, "owner-target")
		if err := os.MkdirAll(filepath.Join(target, "app"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(task.taskPath, "github.com"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(task.taskPath, "github.com", "acme")); err != nil {
			t.Fatal(err)
		}
		if _, err := openCleanupWorktree(task, CleanupResult{ListResult: ListResult{
			WorktreeDir: filepath.Join(task.taskPath, "github.com", "acme", "app"),
		}}); err == nil {
			t.Fatal("a symlinked owner segment was followed instead of refused")
		}
	})

	t.Run("repository", func(t *testing.T) {
		task := newHostLevelCleanupTaskFixture(t)
		target := filepath.Join(elsewhere, "repo-target")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(task.taskPath, "github.com", "acme"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(task.taskPath, "github.com", "acme", "app")); err != nil {
			t.Fatal(err)
		}
		if _, err := openCleanupWorktree(task, CleanupResult{ListResult: ListResult{
			WorktreeDir: filepath.Join(task.taskPath, "github.com", "acme", "app"),
		}}); err == nil {
			t.Fatal("a symlinked repository segment was followed instead of refused")
		}
	})
}

// TestHostLevelSegmentValidatorsRefuseTraversalEmptyAndSeparatorSegments pins
// the exact validators openCleanupWorktree's 3-segment case relies on: the
// host segment goes through repopath.IsForgeHost (which also accepts an
// explicit port, unlike validSafeSegment's charset), and the owner/repository
// segments keep the same validSafeSegment/validRepositorySegment predicates
// the 2-segment case already used. All three refuse "..", empty, and any
// segment carrying a path separator.
func TestHostLevelSegmentValidatorsRefuseTraversalEmptyAndSeparatorSegments(t *testing.T) {
	for _, value := range []string{"..", "", "a/b", "a" + string(filepath.Separator) + "b"} {
		if validSafeSegment(value) {
			t.Errorf("validSafeSegment(%q) = true, want false", value)
		}
		if validRepositorySegment(value) {
			t.Errorf("validRepositorySegment(%q) = true, want false", value)
		}
	}
	// The host validator has its own charset (dotted labels, optional
	// :port) and must independently refuse the same unsafe inputs.
	for _, value := range []string{"..", "", "github.com/acme", "github.com" + string(filepath.Separator) + "acme"} {
		if repopath.IsForgeHost(value) {
			t.Errorf("repopath.IsForgeHost(%q) = true, want false", value)
		}
	}
	// A plain dotted host is exactly what validSafeSegment alone would also
	// accept — openCleanupWorktree's host branch uses repopath.IsForgeHost
	// specifically because it also accepts an explicit port
	// (validSafeSegment's charset has no ":" and would reject it).
	if !repopath.IsForgeHost("github.com") || !validSafeSegment("github.com") {
		t.Fatal("both validators must accept a plain dotted host like github.com")
	}
	if !repopath.IsForgeHost("github.com:8443") {
		t.Fatal("repopath.IsForgeHost must accept an explicit port")
	}
	if validSafeSegment("github.com:8443") {
		t.Fatal("validSafeSegment's charset has no colon; a port-hosted forge level must go through repopath.IsForgeHost instead")
	}
}
