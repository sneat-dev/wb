package fleet

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// cloneFixture lays out the parts of a canonical clone the fingerprint reads,
// with no Git: the fingerprint is file metadata only.
type cloneFixture struct {
	t   *testing.T
	dir string
}

func newCloneFixture(t *testing.T) *cloneFixture {
	t.Helper()
	f := &cloneFixture{t: t, dir: t.TempDir()}
	f.write(".git/HEAD", "ref: refs/heads/main\n")
	f.write(".git/index", "index")
	f.write(".git/refs/heads/main", "1111\n")
	f.write(".git/refs/remotes/origin/main", "1111\n")
	f.write(".git/refs/remotes/origin/HEAD", "ref: refs/remotes/origin/main\n")
	return f
}

func (f *cloneFixture) write(name, content string) {
	f.t.Helper()
	path := filepath.Join(f.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *cloneFixture) touch(name string, to time.Time) {
	f.t.Helper()
	if err := os.Chtimes(filepath.Join(f.dir, name), to, to); err != nil {
		f.t.Fatal(err)
	}
}

func (f *cloneFixture) fingerprint() string {
	f.t.Helper()
	value, err := Fingerprint(f.dir)
	if err != nil {
		f.t.Fatal(err)
	}
	return value
}

// TestFingerprintIsStableUntilARefTheIndexOrTheWorktreeListMoves proves each
// documented signal moves it and an untouched clone does not.
func TestFingerprintIsStableUntilARefTheIndexOrTheWorktreeListMoves(t *testing.T) {
	t.Parallel()
	f := newCloneFixture(t)
	base := f.fingerprint()
	if f.fingerprint() != base {
		t.Fatal("the fingerprint of an untouched clone changed")
	}
	later := time.Now().Add(time.Hour)
	for name, change := range map[string]func(){
		"HEAD moves":                  func() { f.touch(".git/HEAD", later) },
		"the index is rewritten":      func() { f.write(".git/index", "a longer index") },
		"a loose ref moves":           func() { f.touch(".git/refs/heads/main", later) },
		"a branch is created":         func() { f.write(".git/refs/heads/feature/x", "2222\n") },
		"refs are packed":             func() { f.write(".git/packed-refs", "# pack-refs\n") },
		"a linked worktree appears":   func() { f.write(".git/worktrees/t1/HEAD", "ref: refs/heads/t1\n") },
		"a linked worktree's index":   func() { f.write(".git/worktrees/t1/index", "x") },
		"a local worktree directory":  func() { f.write(".worktrees/t1/file", "x") },
		"a remote-tracking ref moves": func() { f.touch(".git/refs/remotes/origin/main", later.Add(time.Hour)) },
		"the configuration changes":   func() { f.write(".git/config", "[remote \"origin\"]\n\turl = x\n") },
		"a reftable table list":       func() { f.write(".git/reftable/tables.list", "0x1.ref\n") },
		"a reftable table is added":   func() { f.write(".git/reftable/0x1.ref", "table") },
	} {
		before := f.fingerprint()
		change()
		if after := f.fingerprint(); after == before {
			t.Errorf("%s did not move the fingerprint", name)
		}
	}
}

// TestFingerprintIgnoresWBsPrivateRefs requires the refs a branch read writes
// to refs/wb not to move the fingerprint that read was keyed on.
func TestFingerprintIgnoresWBsPrivateRefs(t *testing.T) {
	t.Parallel()
	f := newCloneFixture(t)
	before := f.fingerprint()
	f.write(".git/refs/wb/fetch/main", "3333\n")
	if f.fingerprint() != before {
		t.Error("a WB private ref moved the fingerprint")
	}
	f.write(".git/refs/heads/wb", "4444\n")
	if f.fingerprint() == before {
		t.Error("a branch named wb did not move the fingerprint")
	}
}

// TestFingerprintErrors covers a missing clone, a .git that is not a
// directory, an unreadable refs directory and an unreadable worktrees
// directory.
func TestFingerprintErrors(t *testing.T) {
	t.Parallel()
	if _, err := Fingerprint(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("a missing clone has a fingerprint")
	}
	linked := t.TempDir()
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: elsewhere"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Fingerprint(linked); err == nil {
		t.Error("a linked worktree has a clone fingerprint")
	}
	for _, unreadable := range []string{".git", ".git/refs/heads", ".git/worktrees", ".worktrees", ".git/worktrees/t1"} {
		f := newCloneFixture(t)
		f.write(".git/worktrees/t1/HEAD", "x")
		f.write(".worktrees/t1/file", "x")
		path := filepath.Join(f.dir, unreadable)
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := os.ReadDir(path); err == nil {
			_ = os.Chmod(path, 0o755)
			t.Skip("running with privileges that read a mode 000 directory")
		}
		if _, err := Fingerprint(f.dir); err == nil {
			t.Errorf("an unreadable %s did not fail the fingerprint", unreadable)
		}
		_ = os.Chmod(path, 0o755)
	}
}

// TestFingerprintRefusesAnUnstatableSignalFile requires an error other than
// "absent" from a signal file to fail rather than read as absent.
func TestFingerprintRefusesAnUnstatableSignalFile(t *testing.T) {
	t.Parallel()
	f := newCloneFixture(t)
	f.write(".git/worktrees/t1/HEAD", "x")
	if err := os.Chmod(filepath.Join(f.dir, ".git/worktrees/t1"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(f.dir, ".git/worktrees/t1"), 0o755) })
	if _, err := os.Lstat(filepath.Join(f.dir, ".git/worktrees/t1/HEAD")); err == nil {
		t.Skip("running with privileges that stat through a mode 600 directory")
	}
	if _, err := Fingerprint(f.dir); err == nil {
		t.Error("a signal file that cannot be stat'ed fingerprinted as absent")
	}
}

// TestFingerprintRefusesAnUnlistableReftableDirectory makes .git/reftable
// statable but not listable, so its table list reads and its directory does
// not.
func TestFingerprintRefusesAnUnlistableReftableDirectory(t *testing.T) {
	t.Parallel()
	f := newCloneFixture(t)
	f.write(".git/reftable/tables.list", "0x1.ref\n")
	directory := filepath.Join(f.dir, ".git", "reftable")
	if err := os.Chmod(directory, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o755) })
	if _, err := os.ReadDir(directory); err == nil {
		t.Skip("running with privileges that list a directory without read permission")
	}
	if _, err := Fingerprint(f.dir); err == nil {
		t.Error("an unlistable reftable directory fingerprinted cleanly")
	}
}
