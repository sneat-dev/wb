package unix

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestResolveDirectoryEntryPathRejectsUnregisteredHandle covers the fix for
// windows.go's Unlinkat: an empty registered path — an fd that was never
// opened, or one that was already closed and dropped from the path table —
// must be a hard error, never a name resolved relative to the process's
// current directory.
func TestResolveDirectoryEntryPathRejectsUnregisteredHandle(t *testing.T) {
	if _, err := resolveDirectoryEntryPath("", "victim"); !errors.Is(err, errUnknownDirectoryHandle) {
		t.Fatalf("resolveDirectoryEntryPath(\"\", ...) error = %v, want errUnknownDirectoryHandle", err)
	}
}

func TestResolveDirectoryEntryPathJoinsRegisteredHandle(t *testing.T) {
	path, err := resolveDirectoryEntryPath("/tmp/task", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/tmp/task", "owner"); path != want {
		t.Fatalf("resolveDirectoryEntryPath = %q, want %q", path, want)
	}
}

// TestValidateUnlinkatTargetHonoursAtRemovedir covers the other half of the
// same fix: AT_REMOVEDIR must refuse a file (never delete it as if it were
// the empty directory the caller asked to remove), and its absence must
// refuse a directory (matching POSIX unlink's EISDIR) rather than silently
// removing it.
func TestValidateUnlinkatTargetHonoursAtRemovedir(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "file.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirPath := filepath.Join(root, "dir")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Lstat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Lstat(dirPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := validateUnlinkatTarget(fileInfo, AT_REMOVEDIR); err == nil {
		t.Fatal("validateUnlinkatTarget(file, AT_REMOVEDIR) = nil, want a refusal")
	}
	if err := validateUnlinkatTarget(fileInfo, 0); err != nil {
		t.Fatalf("validateUnlinkatTarget(file, 0) = %v, want nil", err)
	}
	if err := validateUnlinkatTarget(dirInfo, 0); err == nil {
		t.Fatal("validateUnlinkatTarget(dir, 0) = nil, want a refusal")
	}
	if err := validateUnlinkatTarget(dirInfo, AT_REMOVEDIR); err != nil {
		t.Fatalf("validateUnlinkatTarget(dir, AT_REMOVEDIR) = %v, want nil", err)
	}
}

// TestAtRemovedirIsNonzero guards the constant itself: a zero value would
// make every flags&AT_REMOVEDIR test above vacuously false, which is exactly
// the pre-fix Windows adapter bug (it ignored AT_REMOVEDIR entirely).
func TestAtRemovedirIsNonzero(t *testing.T) {
	if AT_REMOVEDIR == 0 {
		t.Fatal("AT_REMOVEDIR == 0; flags&AT_REMOVEDIR checks would always be false")
	}
}
