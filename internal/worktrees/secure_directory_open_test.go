package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/secureopen"
)

// tempRootOpener wraps a secureopen.Fake but redirects every OpenRoot call
// to a chosen real temp directory regardless of the root argument
// production code passes (openAbsoluteDirectoryNoFollow always asks for the
// real filesystem root). That lets a test walk a real, disposable directory
// tree through the same fd-relative code path the real filesystem root
// would take, and still arm FailCall/Hook on any call in the sequence.
type tempRootOpener struct {
	fake *secureopen.Fake
	root string
}

func newTempRootOpener(root string) *tempRootOpener {
	return &tempRootOpener{fake: secureopen.NewFake(), root: root}
}

func (o *tempRootOpener) FailCall(callNum int, err error) { o.fake.FailCall(callNum, err) }
func (o *tempRootOpener) Hook(callNum int, fn func())     { o.fake.Hook(callNum, fn) }
func (o *tempRootOpener) CallCount() int                  { return o.fake.CallCount() }

func (o *tempRootOpener) OpenRoot(string) (int, error) { return o.fake.OpenRoot(o.root) }
func (o *tempRootOpener) Mkdir(parentFD int, name string) error {
	return o.fake.Mkdir(parentFD, name)
}
func (o *tempRootOpener) OpenDir(parentFD int, name string) (int, error) {
	return o.fake.OpenDir(parentFD, name)
}
func (o *tempRootOpener) IsSymlink(parentFD int, name string) bool {
	return o.fake.IsSymlink(parentFD, name)
}

var _ secureopen.Opener = (*tempRootOpener)(nil)

func TestOpenAbsoluteDirectoryNoFollowRejectsARelativePath(t *testing.T) {
	t.Parallel()
	if _, err := openAbsoluteDirectoryNoFollowWith(secureopen.Real{}, "relative/path", false); err == nil {
		t.Fatalf("openAbsoluteDirectoryNoFollowWith(relative) = nil, want an error")
	}
}

func TestOpenAbsoluteDirectoryNoFollowOpensTheRootShortcut(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	marker := filepath.Join(root, "marker")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	directory, err := openAbsoluteDirectoryNoFollowWith(newTempRootOpener(root), string(filepath.Separator), false)
	if err != nil {
		t.Fatalf("openAbsoluteDirectoryNoFollowWith(/) = %v, want nil", err)
	}
	t.Cleanup(func() { _ = directory.Close() })

	entries, err := directory.ReadDir(-1)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "marker" {
		t.Fatalf("ReadDir() = %v, want exactly [marker]", entries)
	}
}

func TestOpenAbsoluteDirectoryNoFollowWalksExistingSegments(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	directory, err := openAbsoluteDirectoryNoFollowWith(newTempRootOpener(root), "/a/b", false)
	if err != nil {
		t.Fatalf("openAbsoluteDirectoryNoFollowWith(/a/b) = %v, want nil", err)
	}
	t.Cleanup(func() { _ = directory.Close() })

	want, err := os.Stat(filepath.Join(root, "a", "b"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	got, err := directory.Stat()
	if err != nil {
		t.Fatalf("directory.Stat: %v", err)
	}
	if !os.SameFile(want, got) {
		t.Fatalf("openAbsoluteDirectoryNoFollowWith(/a/b) opened a different directory than %s/a/b", root)
	}
}

func TestOpenAbsoluteDirectoryNoFollowReportsAMissingSegmentOnRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	_, err := openAbsoluteDirectoryNoFollowWith(newTempRootOpener(root), "/absent", false)
	if err == nil {
		t.Fatalf("openAbsoluteDirectoryNoFollowWith(/absent, create=false) = nil, want an error")
	}
	if want := "refusing symlinked secure worktree directory absent"; err.Error() == want {
		t.Fatalf("openAbsoluteDirectoryNoFollowWith(/absent) reported a symlink refusal for a merely-missing segment: %v", err)
	}
}

func TestOpenAbsoluteDirectoryNoFollowRefusesASymlinkSegmentOnRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(root, "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, err := openAbsoluteDirectoryNoFollowWith(newTempRootOpener(root), "/link", false)
	if err == nil {
		t.Fatalf("openAbsoluteDirectoryNoFollowWith(/link) = nil, want a symlink-refusal error")
	}
	if want := "refusing symlinked secure worktree directory link"; err.Error() != want {
		t.Fatalf("openAbsoluteDirectoryNoFollowWith(/link) error = %q, want %q", err.Error(), want)
	}
}

func TestOpenAbsoluteDirectoryNoFollowReportsRootOpenFailure(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "absent")

	if _, err := openAbsoluteDirectoryNoFollowWith(newTempRootOpener(root), "/anything", false); err == nil {
		t.Fatalf("openAbsoluteDirectoryNoFollowWith with a missing root = nil, want an error")
	}
}

func TestOpenAbsoluteDirectoryNoFollowCreatesMissingSegments(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	directory, err := openAbsoluteDirectoryNoFollowWith(newTempRootOpener(root), "/a/b", true)
	if err != nil {
		t.Fatalf("openAbsoluteDirectoryNoFollowWith(/a/b, create=true) = %v, want nil", err)
	}
	t.Cleanup(func() { _ = directory.Close() })

	if info, err := os.Stat(filepath.Join(root, "a", "b")); err != nil || !info.IsDir() {
		t.Fatalf("Stat(root/a/b) = (%v, %v), want an existing directory", info, err)
	}
}

func TestOpenAbsoluteDirectoryNoFollowToleratesAnExistingSegmentOnCreate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	directory, err := openAbsoluteDirectoryNoFollowWith(newTempRootOpener(root), "/a", true)
	if err != nil {
		t.Fatalf("openAbsoluteDirectoryNoFollowWith(/a, create=true) over an existing directory = %v, want nil", err)
	}
	_ = directory.Close()
}

func TestOpenAbsoluteDirectoryNoFollowReportsAMkdirFailureOtherThanEEXIST(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	opener := newTempRootOpener(root)
	boom := errors.New("mkdir boom")
	// Call 1 is OpenRoot, call 2 is the Mkdir for segment "a".
	opener.FailCall(2, boom)

	_, err := openAbsoluteDirectoryNoFollowWith(opener, "/a", true)
	if !errors.Is(err, boom) {
		t.Fatalf("openAbsoluteDirectoryNoFollowWith(/a, create=true) = %v, want it to wrap boom", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "a")); statErr == nil {
		t.Fatalf("segment %q was created on disk despite the injected Mkdir failure", "a")
	}
}

func TestOpenAbsoluteDirectoryNoFollowReportsAnOpenFailureAfterASuccessfulCreate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	opener := newTempRootOpener(root)
	boom := errors.New("open boom")
	// Call 1 is OpenRoot, call 2 is Mkdir("a") (succeeds for real), call 3
	// is the reopen -- fail that one. Call 4 (IsSymlink's real probe) then
	// observes the real, freshly created plain directory and reports false.
	opener.FailCall(3, boom)

	_, err := openAbsoluteDirectoryNoFollowWith(opener, "/a", true)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap boom", err)
	}
	if want := "refusing symlinked secure worktree directory a"; err.Error() == want {
		t.Fatalf("err = %v, want a generic open failure, not a symlink refusal", err)
	}
	if info, statErr := os.Stat(filepath.Join(root, "a")); statErr != nil || !info.IsDir() {
		t.Fatalf("Mkdir's real side effect should have created root/a as a plain directory: (%v, %v)", info, statErr)
	}
}

// TestOpenAbsoluteDirectoryNoFollowRefusesASymlinkSwappedInAfterCreateSucceeds
// reproduces the genuine TOCTOU race this seam exists to make testable: a
// segment is a real directory when Mkdir tolerates its EEXIST, but has been
// replaced with a symlink by the time the reopen actually runs. Hook runs
// the swap for real, immediately before that real Openat -- the following
// Openat and the IsSymlink probe both observe the real, swapped-in symlink.
func TestOpenAbsoluteDirectoryNoFollowRefusesASymlinkSwappedInAfterCreateSucceeds(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	opener := newTempRootOpener(root)
	// Call 1 OpenRoot, call 2 Mkdir("a") (tolerates real EEXIST), call 3 is
	// the reopen this Hook races against.
	opener.Hook(3, func() {
		if err := os.Remove(filepath.Join(root, "a")); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(root, "a")); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
	})

	_, err := openAbsoluteDirectoryNoFollowWith(opener, "/a", true)
	if err == nil {
		t.Fatalf("openAbsoluteDirectoryNoFollowWith(/a, create=true) = nil after a symlink swap, want an error")
	}
	if want := "refusing symlinked secure worktree directory a"; err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
}

func TestDirectoryStillMatchesAcceptsTheSameDirectoryAndRejectsEverythingElse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	other := filepath.Join(root, "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	regular := filepath.Join(root, "regular")
	if err := os.WriteFile(regular, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	held, err := os.Open(target)
	if err != nil {
		t.Fatalf("Open(target): %v", err)
	}
	t.Cleanup(func() { _ = held.Close() })

	if !directoryStillMatches(target, held) {
		t.Fatalf("directoryStillMatches(target, held-open target) = false, want true")
	}
	if directoryStillMatches(other, held) {
		t.Fatalf("directoryStillMatches(other, held-open target) = true, want false")
	}
	if directoryStillMatches(regular, held) {
		t.Fatalf("directoryStillMatches(regular file, ...) = true, want false")
	}
	if directoryStillMatches(link, held) {
		t.Fatalf("directoryStillMatches(symlink, ...) = true, want false (a symlink must never be treated as a match)")
	}
	if directoryStillMatches(filepath.Join(root, "absent"), held) {
		t.Fatalf("directoryStillMatches(missing path, ...) = true, want false")
	}

	closed, err := os.Open(other)
	if err != nil {
		t.Fatalf("Open(other): %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if directoryStillMatches(other, closed) {
		t.Fatalf("directoryStillMatches(other, closed handle) = true, want false (Stat on a closed handle must fail closed)")
	}
}
