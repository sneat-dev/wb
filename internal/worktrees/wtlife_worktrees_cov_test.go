package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWtLifeCovPrepareWorktreeDestinationPlansDirectAndOwnedLayouts(t *testing.T) {
	t.Parallel()
	operationRoot := t.TempDir()
	operationDirectory := wtLifeCovOpenDirectory(t, operationRoot)

	// Direct layout: an absent destination is reported as not-yet-existing.
	planned, exists, err := prepareWorktreeDestination(operationRoot, operationDirectory, "", "app")
	if err != nil || exists || planned != filepath.Join(operationRoot, "app") {
		t.Fatalf("absent direct destination = %q, %t, %v", planned, exists, err)
	}

	// Direct layout: an existing directory is reported as already present.
	if err := os.Mkdir(filepath.Join(operationRoot, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	planned, exists, err = prepareWorktreeDestination(operationRoot, operationDirectory, "", "app")
	if err != nil || !exists || planned != filepath.Join(operationRoot, "app") {
		t.Fatalf("existing direct destination = %q, %t, %v", planned, exists, err)
	}

	// Direct layout: a regular file at the destination is refused.
	if err := os.WriteFile(filepath.Join(operationRoot, "file-repo"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareWorktreeDestination(operationRoot, operationDirectory, "", "file-repo"); err == nil ||
		!strings.Contains(err.Error(), "worktree destination is not a directory") {
		t.Fatalf("file direct destination error = %v", err)
	}

	// Direct layout: a symlink at the destination is refused rather than
	// followed.
	if err := os.Symlink(t.TempDir(), filepath.Join(operationRoot, "linked-repo")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareWorktreeDestination(operationRoot, operationDirectory, "", "linked-repo"); err == nil ||
		!strings.Contains(err.Error(), "refusing symlinked worktree destination") {
		t.Fatalf("symlinked direct destination error = %v", err)
	}

	// Owned layout.
	owned, exists, err := prepareWorktreeDestination(operationRoot, operationDirectory, "acme", "app")
	if err != nil || exists || owned != filepath.Join(operationRoot, "acme", "app") {
		t.Fatalf("absent owned destination = %q, %t, %v", owned, exists, err)
	}
	if err := os.MkdirAll(filepath.Join(operationRoot, "acme", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	owned, exists, err = prepareWorktreeDestination(operationRoot, operationDirectory, "acme", "app")
	if err != nil || !exists || owned != filepath.Join(operationRoot, "acme", "app") {
		t.Fatalf("existing owned destination = %q, %t, %v", owned, exists, err)
	}
}

func TestWtLifeCovPrepareWorktreeDestinationRefusesRedirectedOperation(t *testing.T) {
	t.Parallel()
	operationRoot := t.TempDir()
	operationDirectory := wtLifeCovOpenDirectory(t, operationRoot)
	moved := operationRoot + "-moved"
	if err := os.Rename(operationRoot, moved); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareWorktreeDestination(operationRoot, operationDirectory, "", "app"); err == nil ||
		!strings.Contains(err.Error(), "secure worktree operation path changed before planning") {
		t.Fatalf("redirected operation error = %v", err)
	}
}

func TestWtLifeCovPrepareWorktreeDestinationRefusesUnsafeOwnerAndRepository(t *testing.T) {
	t.Parallel()
	operationRoot := t.TempDir()
	operationDirectory := wtLifeCovOpenDirectory(t, operationRoot)
	if err := os.WriteFile(filepath.Join(operationRoot, "owner-file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareWorktreeDestination(operationRoot, operationDirectory, "owner-file", "app"); err == nil {
		t.Fatal("file owner directory accepted")
	}

	ownerRoot := filepath.Join(operationRoot, "acme")
	if err := os.Mkdir(ownerRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(ownerRoot, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareWorktreeDestination(operationRoot, operationDirectory, "acme", "linked"); err == nil ||
		!strings.Contains(err.Error(), "refusing symlinked worktree destination") {
		t.Fatalf("symlinked owned destination error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(ownerRoot, "file-repo"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareWorktreeDestination(operationRoot, operationDirectory, "acme", "file-repo"); err == nil ||
		!strings.Contains(err.Error(), "worktree destination is not a directory") {
		t.Fatalf("file owned destination error = %v", err)
	}
}

func TestWtLifeCovPrepareOperationRootClassifiesUnsafeHome(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	called := false
	root, err := prepareOperationRoot(home, "task-one", func() { called = true })
	if err != nil {
		t.Fatalf("prepareOperationRoot: %v", err)
	}
	defer root.close()
	if !called {
		t.Fatal("beforeHomeOpen callback was not invoked")
	}
	if root.Path != filepath.Join(home, "worktrees", "task-one") {
		t.Fatalf("prepared operation path = %q", root.Path)
	}
	if info, err := os.Stat(root.Path); err != nil || !info.IsDir() {
		t.Fatalf("prepared operation directory = %v, %v", info, err)
	}

	fileHome := filepath.Join(t.TempDir(), "home-file")
	if err := os.WriteFile(fileHome, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareOperationRoot(fileHome, "task-one", nil); err == nil {
		t.Fatal("prepareOperationRoot accepted a file as WB home")
	}

	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, "worktrees"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareOperationRoot(blocked, "task-one", nil); err == nil ||
		!strings.Contains(err.Error(), "worktrees") {
		t.Fatalf("blocked worktrees root error = %v", err)
	}
}

func TestWtLifeCovPrepareOperationRootAtClassifiesUnsafeRoot(t *testing.T) {
	t.Parallel()
	worktreesRoot := t.TempDir()
	root, err := prepareOperationRootAt(worktreesRoot, "task-one")
	if err != nil {
		t.Fatalf("prepareOperationRootAt: %v", err)
	}
	defer root.close()
	if root.Path != filepath.Join(worktreesRoot, "task-one") {
		t.Fatalf("prepared operation path = %q", root.Path)
	}

	fileRoot := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareOperationRootAt(fileRoot, "task-one"); err == nil {
		t.Fatal("prepareOperationRootAt accepted a file as worktrees root")
	}

	blocked := filepath.Join(worktreesRoot, "task-two")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareOperationRootAt(worktreesRoot, "task-two"); err == nil {
		t.Fatal("prepareOperationRootAt accepted a file as the operation directory")
	}
}

func TestWtLifeCovDirectoryExistsNoFollowClassifiesDestinations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if exists, err := directoryExistsNoFollow(filepath.Join(root, "absent")); err != nil || exists {
		t.Fatalf("absent destination = %t, %v", exists, err)
	}
	if err := os.Mkdir(filepath.Join(root, "present"), 0o755); err != nil {
		t.Fatal(err)
	}
	if exists, err := directoryExistsNoFollow(filepath.Join(root, "present")); err != nil || !exists {
		t.Fatalf("present destination = %t, %v", exists, err)
	}
	if err := os.Symlink(filepath.Join(root, "present"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := directoryExistsNoFollow(filepath.Join(root, "link")); err == nil ||
		!strings.Contains(err.Error(), "refusing symlinked worktree destination") {
		t.Fatalf("symlinked destination error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := directoryExistsNoFollow(filepath.Join(root, "file")); err == nil ||
		!strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("file destination error = %v", err)
	}
	if _, err := directoryExistsNoFollow(filepath.Join(root, "file", "child")); err == nil ||
		!strings.Contains(err.Error(), "inspect worktree destination") {
		t.Fatalf("uninspectable destination error = %v", err)
	}
}

func TestWtLifeCovRequireAbsentNoFollowChildClassifiesEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := wtLifeCovOpenDirectory(t, root)
	if err := requireAbsentNoFollowChild(int(directory.Fd()), "absent"); err != nil {
		t.Fatalf("absent child error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "present"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := requireAbsentNoFollowChild(int(directory.Fd()), "present"); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("present child error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "regular"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	regular, err := os.Open(filepath.Join(root, "regular"))
	if err != nil {
		t.Fatal(err)
	}
	if err := requireAbsentNoFollowChild(int(regular.Fd()), "child"); err == nil ||
		!strings.Contains(err.Error(), "inspect secure worktree destination") {
		t.Fatalf("uninspectable child error = %v", err)
	}
	_ = regular.Close()
}

func TestWtLifeCovDuplicateDirectoryDescriptorRejectsUnusableInput(t *testing.T) {
	t.Parallel()
	if _, err := duplicateDirectoryDescriptor(nil, "wb-test"); err == nil ||
		!strings.Contains(err.Error(), "directory descriptor is unavailable") {
		t.Fatalf("nil directory error = %v", err)
	}
	closed, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := duplicateDirectoryDescriptor(closed, "wb-test"); err == nil {
		t.Fatal("closed directory was duplicated")
	}

	directory := wtLifeCovOpenDirectory(t, t.TempDir())
	duplicate, err := duplicateDirectoryDescriptor(directory, "wb-test")
	if err != nil {
		t.Fatalf("duplicate descriptor: %v", err)
	}
	defer func() { _ = duplicate.Close() }()
	if !directoryStillMatches(directory.Name(), directory) {
		t.Fatal("original directory stopped matching")
	}
	if !directoryStillMatches(directory.Name(), duplicate) {
		t.Fatal("duplicate does not describe the same directory")
	}
}

func TestWtLifeCovOpenAbsoluteDirectoryNoFollowClassifiesPaths(t *testing.T) {
	t.Parallel()
	if _, err := openAbsoluteDirectoryNoFollow("relative/path", false); err == nil ||
		!strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("relative path error = %v", err)
	}

	filesystemRoot, err := openAbsoluteDirectoryNoFollow(string(filepath.Separator), false)
	if err != nil {
		t.Fatalf("filesystem root: %v", err)
	}
	defer func() { _ = filesystemRoot.Close() }()

	realRoot := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "link-root")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := openAbsoluteDirectoryNoFollow(filepath.Join(linkRoot, "child"), false); err == nil ||
		!strings.Contains(err.Error(), "refusing symlinked secure worktree directory") {
		t.Fatalf("symlinked ancestor error = %v", err)
	}
	if _, err := openAbsoluteDirectoryNoFollow(filepath.Join(linkRoot, "child"), true); err == nil {
		t.Fatal("symlinked ancestor was followed while creating")
	}

	created, err := openAbsoluteDirectoryNoFollow(filepath.Join(realRoot, "created", "nested"), true)
	if err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	defer func() { _ = created.Close() }()
	if !directoryStillMatches(filepath.Join(realRoot, "created", "nested"), created) {
		t.Fatal("created directory does not match its path")
	}
}
