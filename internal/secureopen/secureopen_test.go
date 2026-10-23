package secureopen

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	unix "github.com/sneat-dev/wb/internal/unixcompat"

	"github.com/sneat-dev/wb/internal/testsweep"
)

var real = Real{}

func closeFD(t testing.TB, fd int) {
	t.Helper()
	if fd >= 0 {
		_ = unix.Close(fd)
	}
}

func TestRealOpenRootOpensAnExistingDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fd, err := real.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot(%s) = %v, want nil", dir, err)
	}
	t.Cleanup(func() { closeFD(t, fd) })
	if fd < 0 {
		t.Fatalf("OpenRoot(%s) returned fd %d, want >= 0", dir, fd)
	}
}

func TestRealOpenRootReportsAMissingDirectory(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent")
	if _, err := real.OpenRoot(missing); !errors.Is(err, unix.ENOENT) {
		t.Fatalf("OpenRoot(%s) = %v, want ENOENT", missing, err)
	}
}

func TestRealMkdirCreatesThenReportsEEXISTOnRetry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	parentFD, err := real.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { closeFD(t, parentFD) })

	if err := real.Mkdir(parentFD, "child"); err != nil {
		t.Fatalf("first Mkdir = %v, want nil", err)
	}
	if err := real.Mkdir(parentFD, "child"); !errors.Is(err, unix.EEXIST) {
		t.Fatalf("second Mkdir = %v, want EEXIST", err)
	}
}

func TestRealOpenDirOpensAnExistingChildAndRejectsAMissingOne(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	parentFD, err := real.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { closeFD(t, parentFD) })
	if err := real.Mkdir(parentFD, "child"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	fd, err := real.OpenDir(parentFD, "child")
	if err != nil {
		t.Fatalf("OpenDir(child) = %v, want nil", err)
	}
	closeFD(t, fd)

	if _, err := real.OpenDir(parentFD, "absent"); !errors.Is(err, unix.ENOENT) {
		t.Fatalf("OpenDir(absent) = %v, want ENOENT", err)
	}
}

func TestRealOpenDirRefusesARegularFileWithoutFollowingIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "regular"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	parentFD, err := real.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { closeFD(t, parentFD) })

	if _, err := real.OpenDir(parentFD, "regular"); err == nil {
		t.Fatalf("OpenDir(regular file) = nil, want an error (O_DIRECTORY must refuse it)")
	}
}

func TestRealIsSymlinkDistinguishesASymlinkFromARegularDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	parentFD, err := real.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { closeFD(t, parentFD) })

	if !real.IsSymlink(parentFD, "link") {
		t.Fatalf("IsSymlink(link) = false, want true")
	}
	if real.IsSymlink(parentFD, "real") {
		t.Fatalf("IsSymlink(real) = true, want false")
	}
}

func TestRealIsSymlinkReportsFalseWhenTheProbeItselfFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	parentFD, err := real.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { closeFD(t, parentFD) })

	if real.IsSymlink(parentFD, "absent") {
		t.Fatalf("IsSymlink(absent) = true, want false (a failed probe is reported as not a symlink)")
	}
}

// TestFakeDelegatesToRealUntilFailCallIsArmed proves a Fake with no
// FailCall/Hook armed behaves exactly like Real: successful calls hand back
// real, usable descriptors.
func TestFakeDelegatesToRealUntilFailCallIsArmed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fake := NewFake()

	rootFD, err := fake.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { closeFD(t, rootFD) })
	if err := fake.Mkdir(rootFD, "child"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	childFD, err := fake.OpenDir(rootFD, "child")
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	closeFD(t, childFD)
	if fake.IsSymlink(rootFD, "child") {
		t.Fatalf("IsSymlink(child) = true, want false")
	}
	if got := fake.CallCount(); got != 4 {
		t.Fatalf("CallCount() = %d, want 4", got)
	}
}

// TestFakeFailCallFailsExactlyTheTargetedCall proves FailCall's own
// contract standalone, the same way internal/runner/runnertest's tests do,
// before testsweep.Sweep is trusted to drive it across a whole sequence.
func TestFakeFailCallFailsExactlyTheTargetedCall(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	boom := errors.New("boom")
	fake := NewFake()
	fake.FailCall(2, boom)

	rootFD, err := fake.OpenRoot(dir)
	if err != nil {
		t.Fatalf("call 1 OpenRoot = %v, want nil (FailCall targets call 2)", err)
	}
	t.Cleanup(func() { closeFD(t, rootFD) })

	if err := fake.Mkdir(rootFD, "child"); !errors.Is(err, boom) {
		t.Fatalf("call 2 Mkdir = %v, want boom", err)
	}

	// The directory was never actually created, since Mkdir's real syscall
	// never ran for the failed call -- a third call proceeds against real
	// state exactly as if the second had truly failed early.
	if err := fake.Mkdir(rootFD, "child"); err != nil {
		t.Fatalf("call 3 Mkdir = %v, want nil (the failed call 2 must not have created the directory)", err)
	}
}

// TestFakeIsSymlinkReportsFalseWhenFailCallTargetsIt proves a failed
// IsSymlink call reports false rather than propagating FailCall's error --
// IsSymlink has no error return, so its documented behaviour for "the
// probe itself did not work" is the only outcome available.
func TestFakeIsSymlinkReportsFalseWhenFailCallTargetsIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Symlink(dir, filepath.Join(dir, "self-link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	fake := NewFake()
	rootFD, err := fake.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { closeFD(t, rootFD) })

	fake.FailCall(2, errors.New("boom"))
	if fake.IsSymlink(rootFD, "self-link") {
		t.Fatalf("IsSymlink(self-link) = true although FailCall(2) targeted this call, want false")
	}
}

// TestFakeHookRunsBeforeItsTargetCallsRealSyscall proves Hook creates a
// genuine race: code that runs between an earlier check and this call's
// real syscall, so the syscall observes what Hook actually did on disk --
// here, swapping a plain directory for a symlink between an OpenDir that
// would have succeeded and the OpenDir that actually runs.
func TestFakeHookRunsBeforeItsTargetCallsRealSyscall(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	fake := NewFake()
	rootFD, err := fake.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { closeFD(t, rootFD) })

	// Call 2 is the OpenDir below. Arm its Hook to swap "target" for a
	// symlink immediately before that real Openat runs.
	fake.Hook(2, func() {
		if err := os.Remove(target); err != nil {
			t.Fatalf("Remove(target): %v", err)
		}
		if err := os.Symlink(elsewhere, target); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
	})

	if _, err := fake.OpenDir(rootFD, "target"); err == nil {
		t.Fatalf("OpenDir(target) = nil after Hook swapped it for a symlink, want an error (O_NOFOLLOW must refuse it)")
	}
	if !fake.IsSymlink(rootFD, "target") {
		t.Fatalf("IsSymlink(target) = false after the swap, want true")
	}
}

// TestFakeImplementsTestsweepFailer is a compile-time-flavoured assertion,
// kept as a running test so an accidental signature drift fails loudly
// instead of only failing to compile some other package.
func TestFakeImplementsTestsweepFailer(t *testing.T) {
	t.Parallel()
	var _ testsweep.Failer = NewFake()
}

// TestSweepCoversEveryCallOfAThreeCallOpenerSequence exercises
// testsweep.Sweep against this package's own Fake, proving the pairing the
// package doc promises: a happy-path body using Opener can be swept for
// every call's failure without one hand-written test per call.
func TestSweepCoversEveryCallOfAThreeCallOpenerSequence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "child"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	body := func(fake *Fake) error {
		rootFD, err := fake.OpenRoot(dir)
		if err != nil {
			return err
		}
		t.Cleanup(func() { closeFD(t, rootFD) })
		if err := fake.Mkdir(rootFD, "child"); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
		childFD, err := fake.OpenDir(rootFD, "child")
		if err != nil {
			return err
		}
		closeFD(t, childFD)
		return nil
	}

	boom := errors.New("boom")
	testsweep.Sweep(t, NewFake, boom, body, func(t testing.TB, callNum, total int, err error) {
		if !errors.Is(err, boom) {
			t.Fatalf("call %d/%d: err = %v, want it to wrap boom", callNum, total, err)
		}
	})
}
