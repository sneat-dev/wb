//go:build e2e && windows

package session

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/sneat-dev/wb/internal/filewrite"
	"golang.org/x/sys/windows"
)

func windowsLifecycleFixture(t *testing.T) (*os.File, string, string, Record) {
	t.Helper()
	root, caller, _ := sharedScopeFixture(t)
	parent, err := openLifecycle(root, caller.Scope, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	return parent, filepath.Join(root, scopeIndexName, caller.Scope, "lifecycle"), root, caller
}
func readWindowsHeldMarker(t *testing.T, parent *os.File, name string) []byte {
	t.Helper()
	file, err := openRegistryLifecycleFile(parent, name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := checkRegistryLifecyclePrivacy(file, false); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func assertNoWindowsRegistryStages(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".registry-stage-") {
			t.Fatalf("uncommitted native stage survived: %s", entry.Name())
		}
	}
}

func TestWindowsRegistryLifecyclePublisherFaultsAndImmutableWinner(t *testing.T) {
	for _, fault := range []string{"none", "create", "write", "sync", "rename", "close", "parent-sync"} {
		t.Run(fault, func(t *testing.T) {
			parent, path, _, _ := windowsLifecycleFixture(t)
			ops := nativeRegistryPublisherOps()
			failure := errors.New("native publication interrupted")
			switch fault {
			case "create":
				ops.create = func(*os.File, string) (*os.File, error) { return nil, failure }
			case "write":
				ops.write = func(*os.File, []byte) error { return failure }
			case "sync":
				ops.sync = func(*os.File) error { return failure }
			case "rename":
				ops.rename = func(*os.File, *os.File, string) error { return failure }
			case "close":
				ops.close = func(file *os.File) error {
					if err := file.Close(); err != nil {
						return err
					}
					return failure
				}
			case "parent-sync":
				ops.parentSync = func(*os.File) error { return failure }
			}
			raw := []byte(`{"owned":"native marker"}`)
			err := publishRegistryLifecycleWithOps(parent, "marker.json", raw, ops)
			if fault == "none" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("fault evidence lost: %v", err)
			}
			committed := fault == "none" || fault == "close" || fault == "parent-sync"
			if committed {
				if got := readWindowsHeldMarker(t, parent, "marker.json"); !bytes.Equal(got, raw) {
					t.Fatalf("published winner changed: %q", got)
				}
				if err := publishRegistryLifecycle(parent, "marker.json", []byte("replacement"), nil); err == nil {
					t.Fatal("immutable winner overwritten")
				}
				if got := readWindowsHeldMarker(t, parent, "marker.json"); !bytes.Equal(got, raw) {
					t.Fatal("collision replaced winner bytes")
				}
			} else {
				file, err := openRegistryLifecycleFile(parent, "marker.json")
				if file != nil {
					_ = file.Close()
				}
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed publication exposed marker: %v", err)
				}
				if err := publishRegistryLifecycle(parent, "marker.json", raw, nil); err != nil {
					t.Fatalf("native retry after cleanup failed: %v", err)
				}
			}
			assertNoWindowsRegistryStages(t, path)
		})
	}
	parent, path, root, caller := windowsLifecycleFixture(t)
	if _, err := markScopedParked(root, caller, "park-native-exact", nil, time.Now); err != nil {
		t.Fatal(err)
	}
	name, _ := scopedLifecycleName(caller, "parked")
	before := readWindowsHeldMarker(t, parent, name)
	if _, err := markScopedParked(root, caller, "park-native-exact", nil, time.Now); err != nil {
		t.Fatal(err)
	}
	if got := readWindowsHeldMarker(t, parent, name); !bytes.Equal(before, got) {
		t.Fatal("same-authority retry rewrote marker")
	}
	if _, err := markScopedParked(root, caller, "park-other-winner", nil, time.Now); err == nil {
		t.Fatal("different logical winner accepted")
	}
	if err := publishRegistryLifecycle(parent, "injected.json", []byte("x"), &filewrite.Injector{}); err == nil {
		t.Fatal("Unix-only injector silently ignored")
	}
	assertNoWindowsRegistryStages(t, path)
}

func TestWindowsRegistryLifecyclePublisherRetainsParentAndPrivateCreation(t *testing.T) {
	parent, path, _, _ := windowsLifecycleFixture(t)
	ops := nativeRegistryPublisherOps()
	originalWrite, originalRename := ops.write, ops.rename
	ops.write = func(file *os.File, raw []byte) error {
		if err := checkRegistryLifecyclePrivacy(file, false); err != nil {
			return err
		}
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if info.Size() != 0 {
			t.Fatal("privacy was not established before first data write")
		}
		return originalWrite(file, raw)
	}
	moved := path + "-held-original"
	renameDenied := false
	ops.rename = func(file, held *os.File, name string) error {
		if err := os.Rename(path, moved); err != nil {
			if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				return err
			}
			renameDenied = true
			t.Log("kernel denied parent rename while original HANDLE remained held; retained-parent redirection attempt refused")
			return originalRename(file, held, name)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(path, name), []byte("foreign-path-sentinel"), 0600); err != nil {
			return err
		}
		return originalRename(file, held, name)
	}
	if err := publishRegistryLifecycleWithOps(parent, "marker.json", []byte("original-held-parent"), ops); err != nil {
		t.Fatal(err)
	}
	if got := readWindowsHeldMarker(t, parent, "marker.json"); string(got) != "original-held-parent" {
		t.Fatalf("held-parent publication redirected: %q", got)
	}
	if !renameDenied {
		got, err := os.ReadFile(filepath.Join(path, "marker.json"))
		if err != nil || string(got) != "foreign-path-sentinel" {
			t.Fatalf("replacement parent was mutated: %q %v", got, err)
		}
		assertNoWindowsRegistryStages(t, moved)
	} else {
		assertNoWindowsRegistryStages(t, path)
	}
}

func TestWindowsRegistryLifecyclePublisherConcurrentCollisionAndNativeLayout(t *testing.T) {
	parent, path, _, _ := windowsLifecycleFixture(t)
	var header registryRenameInfo
	if unsafe.Sizeof(windows.Handle(0)) == 8 {
		if unsafe.Offsetof(header.Root) != 8 || unsafe.Offsetof(header.Name) != 20 {
			t.Fatal("native amd64/arm64 rename layout differs from SDK")
		}
	} else if unsafe.Offsetof(header.Root) != 4 || unsafe.Offsetof(header.Name) != 12 {
		t.Fatal("native 32-bit rename layout differs from SDK")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, raw := range [][]byte{[]byte("winner-a"), []byte("winner-b")} {
		wg.Add(1)
		go func(raw []byte) {
			defer wg.Done()
			results <- publishRegistryLifecycle(parent, "winner.json", raw, nil)
		}(raw)
	}
	wg.Wait()
	close(results)
	wins, losses := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else {
			losses++
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatalf("native no-replace winners=%d losses=%d", wins, losses)
	}
	raw := readWindowsHeldMarker(t, parent, "winner.json")
	if string(raw) != "winner-a" && string(raw) != "winner-b" {
		t.Fatalf("collision corrupted winner: %q", raw)
	}
	// Non-ASCII names verify native UTF-16 byte lengths, independent of ABI width.
	if err := publishRegistryLifecycle(parent, "owned-🐥.json", []byte("utf16"), nil); err != nil {
		t.Fatal(err)
	}
	if string(readWindowsHeldMarker(t, parent, "owned-🐥.json")) != "utf16" {
		t.Fatal("UTF-16 filename changed")
	}
	for _, name := range []string{"", ".", "..", "../escape", `sub\file`, "marker:ads", "bad\x00name", "bad\nname", "trailing.", " space", "CON.json", strings.Repeat("x", 256)} {
		if err := publishRegistryLifecycle(parent, name, []byte("invalid"), nil); err == nil {
			t.Fatalf("unsafe native basename accepted: %q", name)
		}
	}
	for _, raw := range [][]byte{nil, make([]byte, (64<<10)+1)} {
		if err := publishRegistryLifecycle(parent, "bounded.json", raw, nil); err == nil {
			t.Fatal("unbounded marker accepted")
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside-sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(path, "linked.json")); err != nil {
		t.Fatalf("native reparse proof needs owned symlink creation: %v", err)
	}
	file, err := openRegistryLifecycleFile(parent, "linked.json")
	if err == nil {
		err = checkRegistryLifecyclePrivacy(file, false)
		_ = file.Close()
	}
	if err == nil {
		t.Fatal("native reparse marker followed")
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "outside-sentinel" {
		t.Fatal("reparse target mutated")
	}
	assertNoWindowsRegistryStages(t, path)
}
