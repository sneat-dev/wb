package checkoutmarker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureExcludeRefusesSymlinkedInfoWithoutOutsideWrite(t *testing.T) {
	t.Parallel()
	canonical := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(canonical, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(canonical, ".git", "info")); err != nil {
		t.Fatal(err)
	}
	written, err := EnsureExclude(filepath.Join(canonical, ".git", "info", "exclude"))
	if err == nil || written {
		t.Fatalf("symlinked info: written=%t err=%v", written, err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "exclude")); !os.IsNotExist(err) {
		t.Fatalf("symlinked info wrote outside canonical: %v", err)
	}
}

func TestEnsureExcludeReportsPathResolutionFailure(t *testing.T) {
	t.Parallel()
	written, err := ensureExcludeWithAbs("info/exclude", func(string) (string, error) {
		return "", errors.New("cannot resolve working directory")
	})
	if err == nil || written || !strings.Contains(err.Error(), "resolve exclude path") {
		t.Fatalf("absolute path failure: written=%t err=%v", written, err)
	}
}

func TestEnsureExcludeForGitDirRefusesSymlinkedInfoWithoutOutsideWrite(t *testing.T) {
	t.Parallel()
	canonical := t.TempDir()
	outside := t.TempDir()
	gitPath := filepath.Join(canonical, ".git")
	if err := os.Mkdir(gitPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(gitPath, "info")); err != nil {
		t.Fatal(err)
	}
	gitDirectory, err := os.Open(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gitDirectory.Close() })
	written, err := EnsureExcludeForGitDir(gitDirectory)
	if err == nil || written {
		t.Fatalf("symlinked held info: written=%t err=%v", written, err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "exclude")); !os.IsNotExist(err) {
		t.Fatalf("symlinked held info wrote outside canonical: %v", err)
	}
}

func TestEnsureExcludeRefusesSymlinkedExcludeFile(t *testing.T) {
	t.Parallel()
	for _, held := range []bool{false, true} {
		t.Run(map[bool]string{false: "public path", true: "held Git directory"}[held], func(t *testing.T) {
			t.Parallel()
			gitPath := filepath.Join(t.TempDir(), ".git")
			infoPath := filepath.Join(gitPath, "info")
			if err := os.MkdirAll(infoPath, 0o700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "personal-exclude")
			if err := os.WriteFile(outside, []byte("personal/\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(infoPath, "exclude")); err != nil {
				t.Fatal(err)
			}
			var written bool
			var err error
			if held {
				gitDirectory, openErr := os.Open(gitPath)
				if openErr != nil {
					t.Fatal(openErr)
				}
				defer func() { _ = gitDirectory.Close() }()
				written, err = EnsureExcludeForGitDir(gitDirectory)
			} else {
				written, err = EnsureExclude(filepath.Join(infoPath, "exclude"))
			}
			if err == nil || written {
				t.Fatalf("symlinked exclude: written=%t err=%v", written, err)
			}
			content, err := os.ReadFile(outside)
			if err != nil || string(content) != "personal/\n" {
				t.Fatalf("outside exclude changed: %q, %v", content, err)
			}
		})
	}
}

func TestEnsureExcludeForGitDirCreatesInfoPreservesRulesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	gitPath := filepath.Join(t.TempDir(), ".git")
	if err := os.Mkdir(gitPath, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDirectory, err := os.Open(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gitDirectory.Close() })
	written, err := EnsureExcludeForGitDir(gitDirectory)
	if err != nil || !written {
		t.Fatalf("create held exclude: written=%t err=%v", written, err)
	}
	path := filepath.Join(gitPath, "info", "exclude")
	content, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(content), ExcludePattern) || !strings.Contains(string(content), WorktreesExcludePattern) {
		t.Fatalf("held exclude content=%q err=%v", content, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("held exclude mode=%v err=%v", info.Mode(), err)
	}
	if err := os.WriteFile(path, []byte("personal/"), 0o644); err != nil {
		t.Fatal(err)
	}
	written, err = EnsureExcludeForGitDir(gitDirectory)
	if err != nil || !written {
		t.Fatalf("append held exclude: written=%t err=%v", written, err)
	}
	content, err = os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(content), "personal/\n") || !strings.Contains(string(content), WorktreesExcludePattern) {
		t.Fatalf("additive held exclude content=%q err=%v", content, err)
	}
	written, err = EnsureExcludeForGitDir(gitDirectory)
	if err != nil || written {
		t.Fatalf("idempotent held exclude: written=%t err=%v", written, err)
	}
}

func TestEnsureExcludeAcceptsRelativePathAndRefusesInvalidDescriptors(t *testing.T) {
	t.Parallel()
	absolute := filepath.Join(t.TempDir(), "info", "exclude")
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(working, absolute)
	if err != nil {
		t.Fatal(err)
	}
	written, err := EnsureExclude(relative)
	if err != nil || !written {
		t.Fatalf("relative exclude: written=%t err=%v", written, err)
	}
	if _, err := os.Stat(absolute); err != nil {
		t.Fatalf("relative exclude not created: %v", err)
	}
	custom := filepath.Join(filepath.Dir(absolute), "custom-ignore-file")
	if written, err := EnsureExclude(custom); err != nil || !written {
		t.Fatalf("custom filename: written=%t err=%v", written, err)
	}
	if content, err := os.ReadFile(custom); err != nil || !strings.Contains(string(content), ExcludePattern) {
		t.Fatalf("custom filename content=%q err=%v", content, err)
	}
	if written, err := EnsureExclude(string(filepath.Separator)); err == nil || written {
		t.Fatalf("filesystem root accepted as exclude file: written=%t err=%v", written, err)
	}
	if written, err := EnsureExcludeForGitDir(nil); err == nil || written {
		t.Fatalf("nil Git descriptor accepted: written=%t err=%v", written, err)
	}
	closed, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if written, err := EnsureExcludeForGitDir(closed); err == nil || written {
		t.Fatalf("closed Git descriptor accepted: written=%t err=%v", written, err)
	}
}

func TestEnsureExcludeAtReportsAtomicWriteFailure(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	path := filepath.Join(parent, "removed-info")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	written, err := ensureExcludeAt(directory, "exclude", path+"/exclude")
	if err == nil || written || !strings.Contains(err.Error(), "replace ") {
		t.Fatalf("removed held directory: written=%t err=%v", written, err)
	}
}
