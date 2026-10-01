package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestMakeUniqueStageDirectoryAtPreservesCollisionAndErrorPolicy(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	parentFD := int(parent.Fd())
	const prefix = ".wb-stage-"
	fresh, err := makeUniqueStageDirectoryAt(parentFD, prefix, "exhausted stages", func(size int) string {
		if size != 16 {
			t.Fatalf("token size = %d, want 16", size)
		}
		return "fresh"
	})
	if err != nil || fresh != prefix+"fresh" {
		t.Fatalf("fresh stage: name=%q err=%v", fresh, err)
	}
	info, err := os.Stat(filepath.Join(root, fresh))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("fresh stage mode: info=%v err=%v", info, err)
	}
	if err := os.Mkdir(filepath.Join(root, prefix+"occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	afterCollision, err := makeUniqueStageDirectoryAt(parentFD, prefix, "exhausted stages", func(int) string {
		calls++
		if calls == 1 {
			return "occupied"
		}
		return "after-collision"
	})
	if err != nil || calls != 2 || afterCollision != prefix+"after-collision" {
		t.Fatalf("collision retry: name=%q calls=%d err=%v", afterCollision, calls, err)
	}
	calls = 0
	name, err := makeUniqueStageDirectoryAt(parentFD, prefix, "exhausted stages", func(int) string {
		calls++
		return "occupied"
	})
	if name != "" || err == nil || err.Error() != "exhausted stages" || calls != 16 {
		t.Fatalf("collision exhaustion: name=%q calls=%d err=%v", name, calls, err)
	}
	name, err = makeUniqueStageDirectoryAt(-1, prefix, "exhausted stages", func(int) string { return "unreachable" })
	if name != "" || !errors.Is(err, syscall.EBADF) {
		t.Fatalf("invalid held parent: name=%q err=%v", name, err)
	}
	if _, err := os.Lstat(filepath.Join(root, prefix+"unreachable")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid parent created stage: %v", err)
	}
	local, err := makeTaskBoundLocalStageDirectory(parent, "task")
	if err != nil || !strings.HasPrefix(local, taskBoundLocalStagePrefix("task")) {
		t.Fatalf("fresh task-bound stage: name=%q err=%v", local, err)
	}
	if info, err := os.Stat(filepath.Join(root, local)); err != nil || !info.IsDir() {
		t.Fatalf("fresh task-bound stage missing: info=%v err=%v", info, err)
	}
}

func TestRetireEmptyLocalStageAtPreservesReplacedAndOccupiedEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	prefix := taskBoundLocalStagePrefix("task")
	occupiedName := prefix + "occupied"
	if err := os.Mkdir(filepath.Join(root, occupiedName), 0o700); err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(root, occupiedName, "protected")
	if err := os.WriteFile(protected, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	retireEmptyLocalStageAt(parent, occupiedName)
	if data, err := os.ReadFile(protected); err != nil || string(data) != "keep" {
		t.Fatalf("occupied stage changed: data=%q err=%v", data, err)
	}
	outside := t.TempDir()
	outsideMarker := filepath.Join(outside, "protected")
	if err := os.WriteFile(outsideMarker, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedName := prefix + "linked"
	if err := os.Symlink(outside, filepath.Join(root, linkedName)); err != nil {
		t.Fatal(err)
	}
	retireEmptyLocalStageAt(parent, linkedName)
	if info, err := os.Lstat(filepath.Join(root, linkedName)); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("linked stage was changed: info=%v err=%v", info, err)
	}
	if data, err := os.ReadFile(outsideMarker); err != nil || string(data) != "outside" {
		t.Fatalf("outside stage changed: data=%q err=%v", data, err)
	}
	emptyName := prefix + strings.Repeat("a", 32)
	if err := os.Mkdir(filepath.Join(root, emptyName), 0o700); err != nil {
		t.Fatal(err)
	}
	retireEmptyLocalStageAt(parent, emptyName)
	if _, err := os.Lstat(filepath.Join(root, emptyName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty active stage remained: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	retired := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), taskBoundLocalRetiredStagePrefix("task")) && entry.IsDir() {
			retired++
		}
	}
	if retired != 1 {
		t.Fatalf("retired empty stage count = %d, want 1; entries=%v", retired, entries)
	}
}
