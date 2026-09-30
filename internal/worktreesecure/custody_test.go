package worktreesecure

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/secureopen"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

type tempRootOpener struct {
	fake *secureopen.Fake
	root string
}

func newTempRootOpener(root string) *tempRootOpener {
	return &tempRootOpener{fake: secureopen.NewFake(), root: root}
}
func (o *tempRootOpener) OpenRoot(string) (int, error)          { return o.fake.OpenRoot(o.root) }
func (o *tempRootOpener) Mkdir(parentFD int, name string) error { return o.fake.Mkdir(parentFD, name) }
func (o *tempRootOpener) OpenDir(parentFD int, name string) (int, error) {
	return o.fake.OpenDir(parentFD, name)
}
func (o *tempRootOpener) IsSymlink(parentFD int, name string) bool {
	return o.fake.IsSymlink(parentFD, name)
}
func (o *tempRootOpener) FailCall(call int, err error) { o.fake.FailCall(call, err) }

type invalidFDOpener struct{}

func (invalidFDOpener) OpenRoot(string) (int, error)     { return -1, errors.New("not used") }
func (invalidFDOpener) Mkdir(int, string) error          { return nil }
func (invalidFDOpener) OpenDir(int, string) (int, error) { return -1, nil }
func (invalidFDOpener) IsSymlink(int, string) bool       { return false }

type negativeRootOpener struct{}

func (negativeRootOpener) OpenRoot(string) (int, error)     { return -1, nil }
func (negativeRootOpener) Mkdir(int, string) error          { return nil }
func (negativeRootOpener) OpenDir(int, string) (int, error) { return -1, nil }
func (negativeRootOpener) IsSymlink(int, string) bool       { return false }

type negativeChildOpener struct{ root string }

func (o negativeChildOpener) OpenRoot(string) (int, error)   { return secureopen.Real{}.OpenRoot(o.root) }
func (negativeChildOpener) Mkdir(int, string) error          { return nil }
func (negativeChildOpener) OpenDir(int, string) (int, error) { return -1, nil }
func (negativeChildOpener) IsSymlink(int, string) bool       { return false }

func openTestDirectory(t *testing.T, path string) *os.File {
	t.Helper()
	directory, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	return directory
}
func physicalTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func validSegment(s string) bool { return s != "" && s != "." && s != ".." && filepath.Base(s) == s }

func TestCustodyBasicClassification(t *testing.T) {
	root := physicalTempDir(t)
	if CloseIncompleteFiles(nil) != true || CloseIncompleteFiles() != false {
		t.Fatal("incomplete group classification")
	}
	partial := openTestDirectory(t, root)
	if !CloseIncompleteFiles(partial, nil) {
		t.Fatal("partial descriptor group accepted")
	}
	if _, err := partial.Stat(); err == nil {
		t.Fatal("partial descriptor stayed open")
	}
	if PathWithin(root, filepath.Join(root, "child")) == false || PathWithin(root, filepath.Dir(root)) {
		t.Fatal("path containment")
	}
	if exists, err := DirectoryExistsNoFollow(filepath.Join(root, "missing")); err != nil || exists {
		t.Fatalf("missing = %v, %v", exists, err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if exists, err := DirectoryExistsNoFollow(filepath.Join(root, "directory")); err != nil || !exists {
		t.Fatalf("directory = %v, %v", exists, err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DirectoryExistsNoFollow(filepath.Join(root, "file")); err == nil {
		t.Fatal("regular file accepted")
	}
	if _, err := DirectoryExistsNoFollow(filepath.Join(root, "file", "child")); err == nil {
		t.Fatal("uninspectable path accepted")
	}
	if err := os.Symlink(filepath.Join(root, "directory"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := DirectoryExistsNoFollow(filepath.Join(root, "link")); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := DuplicateDirectoryDescriptor(nil, "nil"); err == nil {
		t.Fatal("nil descriptor accepted")
	}
	directory := openTestDirectory(t, root)
	duplicate, err := DuplicateDirectoryDescriptor(directory, "duplicate")
	if err != nil {
		t.Fatal(err)
	}
	duplicateClosed := false
	t.Cleanup(func() {
		if !duplicateClosed {
			if err := duplicate.Close(); err != nil {
				t.Errorf("close duplicate directory: %v", err)
			}
		}
	})
	if !DirectoryStillMatches(root, duplicate) {
		t.Fatal("duplicate identity mismatch")
	}
	if err := duplicate.Close(); err != nil {
		t.Fatal(err)
	}
	duplicateClosed = true
	if _, err := DuplicateDirectoryDescriptor(duplicate, "closed"); err == nil {
		t.Fatal("closed descriptor duplicated")
	}
}

func TestCustodyDirectoryOpenAndIdentity(t *testing.T) {
	root := physicalTempDir(t)
	parent := openTestDirectory(t, root)
	if _, err := OpenAbsoluteDirectoryNoFollow("relative", false); err == nil {
		t.Fatal("relative path accepted")
	}
	rootShortcut, err := OpenAbsoluteDirectoryNoFollowWith(newTempRootOpener(root), string(filepath.Separator), false)
	if err != nil {
		t.Fatal(err)
	}
	_ = rootShortcut.Close()
	if err := os.MkdirAll(filepath.Join(root, "existing", "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenAbsoluteDirectoryNoFollowWith(newTempRootOpener(root), "/existing/child", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = opened.Close()
	if _, err := OpenAbsoluteDirectoryNoFollowWith(newTempRootOpener(root), "/missing", false); err == nil {
		t.Fatal("missing segment accepted")
	}
	if err := os.Symlink(filepath.Join(root, "existing"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAbsoluteDirectoryNoFollowWith(newTempRootOpener(root), "/link", false); err == nil {
		t.Fatal("symlink segment accepted")
	}
	created, err := OpenAbsoluteDirectoryNoFollow(filepath.Join(root, "nested", "child"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := created.Close(); err != nil {
			t.Errorf("close created directory: %v", err)
		}
	})
	if !DirectoryStillMatches(filepath.Join(root, "nested", "child"), created) {
		t.Fatal("created identity mismatch")
	}
	child, err := OpenDirectoryAtNoFollow(int(parent.Fd()), "nested", "child", "open child")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := child.Close(); err != nil {
			t.Errorf("close child directory: %v", err)
		}
	})
	fd, err := OpenOrCreateNoFollowDirectory(int(parent.Fd()), "other")
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("mkdir")
	failing := secureopen.NewFake()
	failing.FailCall(1, boom)
	if _, err := OpenOrCreateNoFollowDirectoryWith(failing, int(parent.Fd()), "failure"); !errors.Is(err, boom) {
		t.Fatalf("mkdir failure = %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "other"), filepath.Join(root, "link-child")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOrCreateNoFollowDirectoryWith(secureopen.Real{}, int(parent.Fd()), "link-child"); err == nil {
		t.Fatal("symlink child accepted")
	}
	openFailure := secureopen.NewFake()
	openFailure.FailCall(2, boom)
	if _, err := OpenOrCreateNoFollowDirectoryWith(openFailure, int(parent.Fd()), "other"); !errors.Is(err, boom) {
		t.Fatalf("open failure = %v", err)
	}
	_ = unix.Close(fd)
	empty, err := OpenDirectoryAtNoFollow(int(parent.Fd()), "other", "other", "open other")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := empty.Close(); err != nil {
			t.Errorf("close empty directory: %v", err)
		}
	})
	if ok, err := DirectoryEmpty(empty); err != nil || !ok {
		t.Fatalf("empty = %v, %v", ok, err)
	}
	if err := os.WriteFile(filepath.Join(root, "other", "entry"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := DirectoryEmpty(empty); err != nil || ok {
		t.Fatalf("nonempty = %v, %v", ok, err)
	}
	regular, err := os.Open(filepath.Join(root, "other", "entry"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DirectoryEmpty(regular); err == nil {
		t.Fatal("regular file accepted as directory")
	}
	_ = regular.Close()
	closed, err := os.Open(filepath.Join(root, "other"))
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	if _, err := DirectoryEmpty(closed); err == nil {
		t.Fatal("closed directory accepted")
	}
	identity, err := IdentityAt(int(parent.Fd()), "nested")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Device() == 0 || identity.Inode() == 0 {
		t.Fatal("zero identity")
	}
	if _, err := IdentityAt(int(parent.Fd()), "missing"); err == nil {
		t.Fatal("missing identity accepted")
	}
	if _, err := IdentityAt(int(parent.Fd()), "other/entry"); err == nil {
		t.Fatal("file identity accepted")
	}
	if !DirectoryEntryStillMatches(parent, "nested", child) {
		t.Fatal("entry mismatch")
	}
	if err := os.Symlink(filepath.Join(root, "nested"), filepath.Join(root, "entry-link")); err != nil {
		t.Fatal(err)
	}
	if DirectoryEntryStillMatches(parent, "entry-link", child) {
		t.Fatal("symlink entry matched")
	}
	if DirectoryStillMatches(filepath.Join(root, "entry-link"), child) {
		t.Fatal("symlink path matched")
	}
	if DirectoryStillMatches(filepath.Join(root, "other", "entry"), child) {
		t.Fatal("file path matched")
	}
	if DirectoryEntryStillMatches(nil, "nested", child) || DirectoryEntryStillMatches(parent, "missing", child) {
		t.Fatal("invalid entry matched")
	}
	if err := RequireAbsentNoFollowChild(int(parent.Fd()), "missing"); err != nil {
		t.Fatal(err)
	}
	if err := RequireAbsentNoFollowChild(int(parent.Fd()), "nested"); err == nil {
		t.Fatal("existing child accepted")
	}
	regularParent, err := os.Open(filepath.Join(root, "other", "entry"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := regularParent.Close(); err != nil {
			t.Errorf("close regular parent: %v", err)
		}
	})
	if err := RequireAbsentNoFollowChild(int(regularParent.Fd()), "missing"); err == nil {
		t.Fatal("regular parent accepted")
	}
	if absent, err := NoFollowChildAbsent(int(parent.Fd()), "missing"); err != nil || !absent {
		t.Fatalf("missing = %v, %v", absent, err)
	}
	if absent, err := NoFollowChildAbsent(int(parent.Fd()), "nested"); err != nil || absent {
		t.Fatalf("existing = %v, %v", absent, err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NoFollowChildAbsent(int(parent.Fd()), "missing"); err == nil {
		t.Fatal("closed parent accepted")
	}
}

func TestCustodyOpenWithAndPrivateChild(t *testing.T) {
	root := physicalTempDir(t)
	opener := newTempRootOpener(root)
	if _, err := OpenAbsoluteDirectoryNoFollowWith(opener, "/a", true); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAbsoluteDirectoryNoFollowWith(negativeRootOpener{}, string(filepath.Separator), false); err == nil || err.Error() != "wrap secure directory /" {
		t.Fatalf("root nil wrap = %v", err)
	}
	if _, err := OpenAbsoluteDirectoryNoFollowWith(negativeChildOpener{root: root}, "/child", false); err == nil || err.Error() != "wrap secure directory /child" {
		t.Fatalf("final nil wrap = %v", err)
	}
	boom := errors.New("boom")
	opener = newTempRootOpener(root)
	opener.FailCall(1, boom)
	if _, err := OpenAbsoluteDirectoryNoFollowWith(opener, "/a", false); !errors.Is(err, boom) {
		t.Fatalf("root failure = %v", err)
	}
	parent := openTestDirectory(t, root)
	if _, err := OpenPrivateChild(parent, "../bad", true, validSegment); err == nil {
		t.Fatal("unsafe private child accepted")
	}
	child, err := OpenPrivateChild(parent, "private", true, validSegment)
	if err != nil {
		t.Fatal(err)
	}
	_ = child.Close()
	if info, err := os.Stat(filepath.Join(root, "private")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("private mode = %v, %v", info, err)
	}
	if _, err := OpenPrivateChildWith(invalidFDOpener{}, parent, "failed", true, validSegment); err == nil {
		t.Fatal("invalid create descriptor accepted")
	}
	if _, err := OpenPrivateChildWith(invalidFDOpener{}, parent, "read-wrap", false, validSegment); err == nil || err.Error() != "wrap private directory read-wrap" {
		t.Fatalf("private nil wrap = %v", err)
	}
	if _, err := OpenPrivateChildWith(secureopen.Real{}, parent, "missing", false, validSegment); err == nil {
		t.Fatal("missing child accepted")
	}
	private, err := OpenPrivateChild(parent, "records", true, validSegment)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := private.Close(); err != nil {
			t.Errorf("close private directory: %v", err)
		}
	})
	if err := filewrite.WriteJSONImmutableAt(private, "record.json", map[string]string{"value": "ok"}, false, nil); err != nil {
		t.Fatal(err)
	}
	record, err := ReadPrivateRecordAt[map[string]string](parent, "records", "record.json", validSegment)
	if err != nil || record["value"] != "ok" {
		t.Fatalf("private record = %#v, %v", record, err)
	}
	if _, err := ReadPrivateRecordAt[map[string]string](parent, "missing", "record.json", validSegment); err == nil {
		t.Fatal("missing private child accepted")
	}
	if _, err := ReadPrivateRecordAt[map[string]string](parent, "records", "missing.json", validSegment); err == nil {
		t.Fatal("missing private record accepted")
	}
}

func TestCustodyMoveExpectedDirectoryNoReplace(t *testing.T) {
	root := physicalTempDir(t)
	parent := openTestDirectory(t, root)
	if err := os.Mkdir(filepath.Join(root, "source"), 0o700); err != nil {
		t.Fatal(err)
	}
	expected, err := OpenDirectoryAtNoFollow(int(parent.Fd()), "source", "source", "open source")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := expected.Close(); err != nil {
			t.Errorf("close expected directory: %v", err)
		}
	})
	rename := func(a int, b string, c int, d string) error { return filewrite.RenameNoReplace(a, b, c, d, nil) }
	moved, err := MoveExpectedDirectoryNoReplace(parent, "source", parent, "target", expected, rename, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = moved.Close()
	if _, err := MoveExpectedDirectoryNoReplaceAuthorized(parent, "source", parent, "again", expected, rename, nil); err == nil {
		t.Fatal("moved source accepted")
	}
	if err := os.Mkdir(filepath.Join(root, "source2"), 0o700); err != nil {
		t.Fatal(err)
	}
	source2, err := OpenDirectoryAtNoFollow(int(parent.Fd()), "source2", "source2", "open source2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := source2.Close(); err != nil {
			t.Errorf("close source2 directory: %v", err)
		}
	})
	want := errors.New("authorization")
	if _, err := MoveExpectedDirectoryNoReplaceAuthorized(parent, "source2", parent, "target2", source2, rename, func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("authorization = %v", err)
	}
	moveFixture := func(t *testing.T, name string) (string, *os.File, *os.File) {
		t.Helper()
		fixtureRoot := physicalTempDir(t)
		fixtureParent := openTestDirectory(t, fixtureRoot)
		if err := os.Mkdir(filepath.Join(fixtureRoot, name), 0o700); err != nil {
			t.Fatal(err)
		}
		held, err := OpenDirectoryAtNoFollow(int(fixtureParent.Fd()), name, name, "open fixture")
		if err != nil {
			t.Fatal(err)
		}
		return fixtureRoot, fixtureParent, held
	}
	t.Run("after authorization and hook", func(t *testing.T) {
		fixtureRoot, fixtureParent, held := moveFixture(t, "hook")
		called := false
		moved, err := MoveExpectedDirectoryNoReplace(fixtureParent, "hook", fixtureParent, "hook-target", held, rename, func() { called = true }, func() {})
		if err != nil || !called {
			t.Fatalf("hook move = %v, %t", err, called)
		}
		_ = moved.Close()
		_ = held.Close()
		_ = fixtureRoot
	})
	t.Run("destination disappears", func(t *testing.T) {
		fixtureRoot, fixtureParent, held := moveFixture(t, "gone")
		_, err := MoveExpectedDirectoryNoReplaceAuthorized(fixtureParent, "gone", fixtureParent, "gone-target", held, rename, nil, func() { _ = os.Rename(filepath.Join(fixtureRoot, "gone-target"), filepath.Join(fixtureRoot, "away")) })
		if err == nil {
			t.Fatal("disappeared destination accepted")
		}
		_ = held.Close()
	})
	t.Run("source returns", func(t *testing.T) {
		fixtureRoot, fixtureParent, held := moveFixture(t, "again")
		if err := os.WriteFile(filepath.Join(fixtureRoot, "again", "original"), []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
		heldInfo, err := held.Stat()
		if err != nil {
			t.Fatal(err)
		}
		moved, err := MoveExpectedDirectoryNoReplaceAuthorized(fixtureParent, "again", fixtureParent, "again-target", held, rename, nil, func() {
			if err := os.Mkdir(filepath.Join(fixtureRoot, "again"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fixtureRoot, "again", "competitor"), []byte("competitor"), 0o600); err != nil {
				t.Fatal(err)
			}
		})
		if !errors.Is(err, ErrDirectoryMoveIdentityChanged) || moved == nil {
			t.Fatalf("recreated source = %v, %v", moved, err)
		}
		movedInfo, statErr := moved.Stat()
		if statErr != nil || !os.SameFile(heldInfo, movedInfo) {
			t.Fatalf("returned handle lost original identity: %v", statErr)
		}
		if _, statErr := held.Stat(); statErr != nil {
			t.Fatalf("borrowed handle invalid: %v", statErr)
		}
		if got, readErr := os.ReadFile(filepath.Join(fixtureRoot, "again-target", "original")); readErr != nil || string(got) != "original" {
			t.Fatalf("published content = %q, %v", got, readErr)
		}
		if got, readErr := os.ReadFile(filepath.Join(fixtureRoot, "again", "competitor")); readErr != nil || string(got) != "competitor" {
			t.Fatalf("competing source overwritten = %q, %v", got, readErr)
		}
		_ = moved.Close()
		_ = held.Close()
	})
	t.Run("expected closes", func(t *testing.T) {
		_, fixtureParent, held := moveFixture(t, "closed")
		_, err := MoveExpectedDirectoryNoReplaceAuthorized(fixtureParent, "closed", fixtureParent, "closed-target", held, rename, nil, func() { _ = held.Close() })
		if !errors.Is(err, ErrDirectoryMoveIdentityChanged) {
			t.Fatalf("closed expected = %v", err)
		}
	})
	t.Run("source descriptor closes", func(t *testing.T) {
		fromRoot := physicalTempDir(t)
		toRoot := physicalTempDir(t)
		from := openTestDirectory(t, fromRoot)
		to := openTestDirectory(t, toRoot)
		if err := os.Mkdir(filepath.Join(fromRoot, "source"), 0o700); err != nil {
			t.Fatal(err)
		}
		held, err := OpenDirectoryAtNoFollow(int(from.Fd()), "source", "source", "open source")
		if err != nil {
			t.Fatal(err)
		}
		_, err = MoveExpectedDirectoryNoReplaceAuthorized(from, "source", to, "target", held, rename, nil, func() { _ = from.Close() })
		if err == nil {
			t.Fatal("closed source descriptor accepted")
		}
		_ = held.Close()
	})
	t.Run("rename fails", func(t *testing.T) {
		_, fixtureParent, held := moveFixture(t, "rename-failure")
		if _, err := MoveExpectedDirectoryNoReplaceAuthorized(fixtureParent, "rename-failure", fixtureParent, "target", held, func(int, string, int, string) error { return errors.New("rename") }, nil); err == nil {
			t.Fatal("rename failure accepted")
		}
		_ = held.Close()
	})
	t.Run("restore fails", func(t *testing.T) {
		_, fixtureParent, held := moveFixture(t, "restore-failure")
		calls := 0
		restoreFailure := func(a int, b string, c int, d string) error {
			calls++
			if calls == 2 {
				return errors.New("restore")
			}
			return filewrite.RenameNoReplace(a, b, c, d, nil)
		}
		_, err := MoveExpectedDirectoryNoReplaceAuthorized(fixtureParent, "restore-failure", fixtureParent, "target", held, restoreFailure, nil, func() { _ = held.Close() })
		if !errors.Is(err, ErrDirectoryMoveIdentityChanged) {
			t.Fatalf("restore failure = %v", err)
		}
	})
	t.Run("destination substitution restore collision", func(t *testing.T) {
		fixtureRoot, fixtureParent, held := moveFixture(t, "collision")
		if err := os.WriteFile(filepath.Join(fixtureRoot, "collision", "original"), []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
		heldInfo, err := held.Stat()
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		racingRename := func(a int, b string, c int, d string) error {
			calls++
			if calls == 2 {
				if err := os.Mkdir(filepath.Join(fixtureRoot, "collision"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fixtureRoot, "collision", "competitor"), []byte("competitor"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			return filewrite.RenameNoReplace(a, b, c, d, nil)
		}
		_, err = MoveExpectedDirectoryNoReplaceAuthorized(fixtureParent, "collision", fixtureParent, "collision-target", held, racingRename, nil, func() {
			if err := os.Rename(filepath.Join(fixtureRoot, "collision-target"), filepath.Join(fixtureRoot, "original-held")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(fixtureRoot, "collision-target"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fixtureRoot, "collision-target", "substitute"), []byte("substitute"), 0o600); err != nil {
				t.Fatal(err)
			}
		})
		if !errors.Is(err, ErrDirectoryMoveIdentityChanged) {
			t.Fatalf("restore collision = %v", err)
		}
		if info, statErr := held.Stat(); statErr != nil || !os.SameFile(heldInfo, info) {
			t.Fatalf("borrowed identity changed: %v", statErr)
		}
		if got, readErr := os.ReadFile(filepath.Join(fixtureRoot, "collision", "competitor")); readErr != nil || string(got) != "competitor" {
			t.Fatalf("source overwritten = %q, %v", got, readErr)
		}
		if got, readErr := os.ReadFile(filepath.Join(fixtureRoot, "collision-target", "substitute")); readErr != nil || string(got) != "substitute" {
			t.Fatalf("destination overwritten = %q, %v", got, readErr)
		}
		_ = held.Close()
	})
}
