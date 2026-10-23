package lifecyclehooks

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHkCovValidateTrustedConfigRejectsNonRegularFiles(t *testing.T) {
	t.Parallel()
	if _, err := validateTrustedConfig(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("expected missing config to fail")
	}

	directory := t.TempDir()
	if _, err := validateTrustedConfig(directory); err == nil || !strings.Contains(err.Error(), "regular non-symlink") {
		t.Fatalf("directory error=%v", err)
	}

	file := hkCovWriteFile(t, filepath.Join(t.TempDir(), "wb.yaml"), "hooks: {}\n", 0o600)
	link := filepath.Join(t.TempDir(), "link.yaml")
	if err := os.Symlink(file, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := validateTrustedConfig(link); err == nil || !strings.Contains(err.Error(), "regular non-symlink") {
		t.Fatalf("symlink error=%v", err)
	}
}

func TestHkCovValidateControlPathsResolveFailures(t *testing.T) {
	t.Parallel()
	checkout := t.TempDir()

	resolveCheckout := Dispatcher{ConfigPath: "/tmp/wb.yaml", StateDir: "/tmp/state", ReceiptPath: "/tmp/receipts.jsonl"}
	resolveCheckout.EvalSymlinks = func(string) (string, error) { return "", errors.New("cannot resolve") }
	if err := resolveCheckout.validateControlPaths([]Event{{Checkout: checkout}}); err == nil || !strings.Contains(err.Error(), "resolve checkout") {
		t.Fatalf("checkout resolve error=%v", err)
	}

	resolveControl := Dispatcher{ConfigPath: "/tmp/wb.yaml", StateDir: "/tmp/state", ReceiptPath: "/tmp/receipts.jsonl"}
	resolveControl.EvalSymlinks = func(path string) (string, error) {
		if filepath.Clean(path) == filepath.Clean(checkout) {
			return checkout, nil
		}
		return "", errors.New("control path unreadable")
	}
	if err := resolveControl.validateControlPaths([]Event{{Checkout: checkout}}); err == nil || !strings.Contains(err.Error(), "resolve lifecycle hook configuration") {
		t.Fatalf("control resolve error=%v", err)
	}
}

func TestHkCovResolveWithMissingTailStopsAtFilesystemRoot(t *testing.T) {
	t.Parallel()
	missing := func(string) (string, error) { return "", fs.ErrNotExist }
	resolved, err := resolveWithMissingTail("/a/b/c", missing)
	if err != nil || resolved != filepath.Clean("/a/b/c") {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
}

func TestHkCovVerifyCheckoutFailures(t *testing.T) {
	t.Parallel()
	if _, _, err := verifyCheckout(Event{Checkout: filepath.Join(t.TempDir(), "absent")}); err == nil || !strings.Contains(err.Error(), "resolve checkout") {
		t.Fatalf("missing checkout error=%v", err)
	}

	file := hkCovWriteFile(t, filepath.Join(t.TempDir(), "checkout"), "not a directory", 0o600)
	if _, _, err := verifyCheckout(Event{Checkout: file}); err == nil || !strings.Contains(err.Error(), "must resolve to a directory") {
		t.Fatalf("file checkout error=%v", err)
	}

	plain := t.TempDir()
	if _, _, err := verifyCheckout(Event{Checkout: plain, Repository: "github.com/acme/app"}); err == nil || !strings.Contains(err.Error(), "identify checkout repository") {
		t.Fatalf("plain directory error=%v", err)
	}

	unborn := hkCovGitRepository(t, "https://github.com/acme/app.git")
	if _, _, err := verifyCheckout(Event{Checkout: unborn, Repository: "github.com/acme/app"}); err == nil || !strings.Contains(err.Error(), "read checkout HEAD") {
		t.Fatalf("unborn repository error=%v", err)
	}
}

func TestHkCovValidatePrivateDirectoryRejectsUnusablePaths(t *testing.T) {
	t.Parallel()
	if err := validatePrivateDirectory(filepath.Join(t.TempDir(), "absent"), "purpose"); err == nil {
		t.Fatal("expected missing directory to fail")
	}

	file := hkCovWriteFile(t, filepath.Join(t.TempDir(), "file"), "x", 0o600)
	if err := validatePrivateDirectory(file, "purpose"); err == nil || !strings.Contains(err.Error(), "non-symlink directory") {
		t.Fatalf("file error=%v", err)
	}

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := validatePrivateDirectory(link, "purpose"); err == nil || !strings.Contains(err.Error(), "non-symlink directory") {
		t.Fatalf("symlink error=%v", err)
	}
}

func TestHkCovEnsureTrustedParentRejectsFileParent(t *testing.T) {
	t.Parallel()
	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	if err := ensureTrustedParent(filepath.Join(blocker, "child", "file.json"), "purpose"); err == nil {
		t.Fatal("expected parent creation failure")
	}
}

func TestHkCovValidateTrustedDataFileRejectsUntrustedPaths(t *testing.T) {
	t.Parallel()
	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	if _, _, err := validateTrustedDataFile(filepath.Join(blocker, "child"), "purpose"); err == nil {
		t.Fatal("expected non-directory Lstat failure")
	}

	file := hkCovWriteFile(t, filepath.Join(t.TempDir(), "data.jsonl"), "", 0o600)
	link := filepath.Join(t.TempDir(), "link.jsonl")
	if err := os.Symlink(file, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := validateTrustedDataFile(link, "purpose"); err == nil || !strings.Contains(err.Error(), "regular non-symlink") {
		t.Fatalf("symlink error=%v", err)
	}

	groupWritable := hkCovWriteFile(t, filepath.Join(t.TempDir(), "shared.jsonl"), "", 0o600)
	if err := os.Chmod(groupWritable, 0o620); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateTrustedDataFile(groupWritable, "purpose"); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("group-writable error=%v", err)
	}

	info, exists, err := validateTrustedDataFile(file, "purpose")
	if err != nil || !exists || info == nil {
		t.Fatalf("info=%v exists=%t err=%v", info, exists, err)
	}
	if _, exists, err := validateTrustedDataFile(filepath.Join(t.TempDir(), "absent.jsonl"), "purpose"); err != nil || exists {
		t.Fatalf("exists=%t err=%v", exists, err)
	}
}

func TestHkCovValidateControlPathsAcceptsOutsideCheckout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	checkout := hkCovCheckout(t, root)
	dispatcher := Dispatcher{
		ConfigPath: filepath.Join(root, "wb.yaml"), StateDir: filepath.Join(root, "state"),
		ReceiptPath: filepath.Join(root, "receipts.jsonl"), EvalSymlinks: filepath.EvalSymlinks,
	}
	if err := dispatcher.validateControlPaths([]Event{{Checkout: checkout}}); err != nil {
		t.Fatalf("outside checkout rejected: %v", err)
	}
}
