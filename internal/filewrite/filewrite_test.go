package filewrite

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

func openTestDir(t *testing.T) *os.File {
	t.Helper()
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	return dir
}

var errBoom = errors.New("boom")

// --- Injector matching semantics ---

func TestInjectorRunDoesNothingOnNilInjector(t *testing.T) {
	t.Parallel()
	var inj *Injector
	if err := inj.run(StepSync, "x"); err != nil {
		t.Fatalf("nil Injector.run = %v, want nil", err)
	}
}

func TestInjectorRunIgnoresAnUnmatchedStep(t *testing.T) {
	t.Parallel()
	inj := &Injector{Step: StepSync, Name: "a", Err: errBoom}
	if err := inj.run(StepChmod, "a"); err != nil {
		t.Fatalf("run at wrong step = %v, want nil", err)
	}
}

func TestInjectorRunIgnoresAnUnmatchedName(t *testing.T) {
	t.Parallel()
	inj := &Injector{Step: StepSync, Name: "a", Err: errBoom}
	if err := inj.run(StepSync, "b"); err != nil {
		t.Fatalf("run at wrong name = %v, want nil", err)
	}
}

func TestInjectorRunMatchesEveryNameWhenNameIsEmpty(t *testing.T) {
	t.Parallel()
	inj := &Injector{Step: StepSync, Err: errBoom}
	if err := inj.run(StepSync, "anything"); !errors.Is(err, errBoom) {
		t.Fatalf("run with empty Name = %v, want errBoom", err)
	}
}

func TestInjectorRunFiresOnEveryOccurrenceAtOrAfterSkip(t *testing.T) {
	t.Parallel()
	inj := &Injector{Step: StepSync, Name: "a", Err: errBoom}
	if err := inj.run(StepSync, "a"); !errors.Is(err, errBoom) {
		t.Fatalf("first run = %v, want errBoom", err)
	}
	if err := inj.run(StepSync, "a"); !errors.Is(err, errBoom) {
		t.Fatalf("second run = %v, want errBoom (no one-shot without Hook)", err)
	}
}

func TestInjectorRunAppliesFailAfterHookOnlyFromTheOccurrenceAfterHookFires(t *testing.T) {
	t.Parallel()
	calls := 0
	inj := &Injector{Step: StepOpenOrCreate, Name: "a", Hook: func() { calls++ }, FailAfterHook: true, Err: errBoom}
	if err := inj.run(StepOpenOrCreate, "a"); err != nil {
		t.Fatalf("occurrence where Hook fires = %v, want nil (FailAfterHook defers the error)", err)
	}
	if calls != 1 {
		t.Fatalf("Hook called %d times, want 1", calls)
	}
	if err := inj.run(StepOpenOrCreate, "a"); !errors.Is(err, errBoom) {
		t.Fatalf("occurrence after Hook fired = %v, want errBoom", err)
	}
	if calls != 1 {
		t.Fatalf("Hook called %d times after its occurrence, want still 1 (fires once)", calls)
	}
}

func TestInjectorRunSkipsTheStatedNumberOfOccurrencesFirst(t *testing.T) {
	t.Parallel()
	inj := &Injector{Step: StepOpenOrCreate, Name: "a", Skip: 2, Err: errBoom}
	if err := inj.run(StepOpenOrCreate, "a"); err != nil {
		t.Fatalf("occurrence 1 = %v, want nil (skipped)", err)
	}
	if err := inj.run(StepOpenOrCreate, "a"); err != nil {
		t.Fatalf("occurrence 2 = %v, want nil (skipped)", err)
	}
	if err := inj.run(StepOpenOrCreate, "a"); !errors.Is(err, errBoom) {
		t.Fatalf("occurrence 3 = %v, want errBoom", err)
	}
}

func TestInjectorRunReturnsNilWhenErrIsUnset(t *testing.T) {
	t.Parallel()
	// A matching Injector with no Err (typically paired with Hook) never
	// fails the step; this is what the open-or-create race seam relies
	// on to let its own occurrence succeed for real.
	inj := &Injector{Step: StepSync, Name: "a"}
	if err := inj.run(StepSync, "a"); err != nil {
		t.Fatalf("run with Err unset = %v, want nil", err)
	}
}

func TestInjectorRunInvokesHookBeforeReturningTheError(t *testing.T) {
	t.Parallel()
	called := false
	inj := &Injector{Step: StepOpenOrCreate, Name: "a", Hook: func() { called = true }, Err: errBoom}
	if err := inj.run(StepOpenOrCreate, "a"); !errors.Is(err, errBoom) {
		t.Fatalf("run = %v, want errBoom", err)
	}
	if !called {
		t.Fatal("Hook was not invoked")
	}
}

func TestInjectorShortWriteReportsNoInjectionWhenUnmatched(t *testing.T) {
	t.Parallel()
	inj := &Injector{Step: StepSync, Name: "a"}
	if n, ok := inj.shortWrite("a"); ok || n != 0 {
		t.Fatalf("shortWrite = (%d, %v), want (0, false)", n, ok)
	}
}

func TestInjectorShortWriteReportsNoInjectionOnAnUnmatchedName(t *testing.T) {
	t.Parallel()
	inj := &Injector{Step: StepShortWrite, Name: "a", ShortBytes: 3}
	if n, ok := inj.shortWrite("b"); ok || n != 0 {
		t.Fatalf("shortWrite with wrong name = (%d, %v), want (0, false)", n, ok)
	}
}

func TestInjectorShortWriteSkipsTheStatedNumberOfOccurrencesFirst(t *testing.T) {
	t.Parallel()
	inj := &Injector{Step: StepShortWrite, Name: "a", Skip: 1, ShortBytes: 3}
	if n, ok := inj.shortWrite("a"); ok || n != 0 {
		t.Fatalf("occurrence 1 = (%d, %v), want (0, false) (skipped)", n, ok)
	}
	if n, ok := inj.shortWrite("a"); !ok || n != 3 {
		t.Fatalf("occurrence 2 = (%d, %v), want (3, true)", n, ok)
	}
}

func TestInjectorShortWriteInvokesHookAndReportsShortBytes(t *testing.T) {
	t.Parallel()
	called := false
	inj := &Injector{Step: StepShortWrite, Name: "a", ShortBytes: 3, Hook: func() { called = true }}
	n, ok := inj.shortWrite("a")
	if !ok || n != 3 {
		t.Fatalf("shortWrite = (%d, %v), want (3, true)", n, ok)
	}
	if !called {
		t.Fatal("Hook was not invoked")
	}
}

func TestShortWriteErrorMessageNamesFileAndCounts(t *testing.T) {
	t.Parallel()
	err := &ShortWriteError{Name: "f", Wrote: 2, Want: 5}
	want := `filewrite: short write to "f": wrote 2 of 5 bytes`
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

// --- CreateExclusive / OpenReadOnly ---

func TestCreateExclusiveCreatesANewFile(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	fd, err := CreateExclusive(int(dir.Fd()), "f", 0o600, nil)
	if err != nil {
		t.Fatalf("CreateExclusive: %v", err)
	}
	_ = unix.Close(fd)
	if _, err := os.Stat(filepath.Join(dir.Name(), "f")); err != nil {
		t.Fatalf("created file missing: %v", err)
	}
}

func TestCreateExclusiveReportsEEXISTOnAnExistingName(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	fd, err := CreateExclusive(int(dir.Fd()), "f", 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	if _, err := CreateExclusive(int(dir.Fd()), "f", 0o600, nil); !errors.Is(err, unix.EEXIST) {
		t.Fatalf("second CreateExclusive = %v, want EEXIST", err)
	}
}

func TestCreateExclusiveHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	inj := &Injector{Step: StepOpenOrCreate, Name: "f", Err: errBoom}
	if _, err := CreateExclusive(int(dir.Fd()), "f", 0o600, inj); !errors.Is(err, errBoom) {
		t.Fatalf("CreateExclusive with injected failure = %v, want errBoom", err)
	}
	if _, err := os.Stat(filepath.Join(dir.Name(), "f")); err == nil {
		t.Fatal("injected failure still created the file")
	}
}

func TestOpenReadOnlyOpensAnExistingFile(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	fd, err := CreateExclusive(int(dir.Fd()), "f", 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	fd, err = OpenReadOnly(int(dir.Fd()), "f", nil)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	_ = unix.Close(fd)
}

func TestOpenReadOnlyReportsENOENTOnAMissingName(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	if _, err := OpenReadOnly(int(dir.Fd()), "missing", nil); !errors.Is(err, unix.ENOENT) {
		t.Fatalf("OpenReadOnly(missing) = %v, want ENOENT", err)
	}
}

func TestOpenReadOnlyHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	inj := &Injector{Step: StepOpen, Name: "f", Err: errBoom}
	if _, err := OpenReadOnly(int(dir.Fd()), "f", inj); !errors.Is(err, errBoom) {
		t.Fatalf("OpenReadOnly with injected failure = %v, want errBoom", err)
	}
}

// TestOpenReadOnlyIgnoresAStepOpenOrCreateInjector proves StepOpen and
// StepOpenOrCreate are genuinely distinct steps (N2 from the task-9 PR-1
// review): an Injector armed for "the create" must never fire on "the
// reopen", even when both calls share a Name.
func TestOpenReadOnlyIgnoresAStepOpenOrCreateInjector(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	fd, err := CreateExclusive(int(dir.Fd()), "f", 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	inj := &Injector{Step: StepOpenOrCreate, Name: "f", Err: errBoom}
	fd, err = OpenReadOnly(int(dir.Fd()), "f", inj)
	if err != nil {
		t.Fatalf("OpenReadOnly with a StepOpenOrCreate injector = %v, want nil (StepOpen and StepOpenOrCreate are distinct)", err)
	}
	_ = unix.Close(fd)
}

// --- Chmod ---

func TestChmodChangesTheModeOfAnOpenFile(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	fd, err := CreateExclusive(int(dir.Fd()), "f", 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if err := Chmod(fd, 0o400, "f", nil); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir.Name(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("mode = %v, want 0400", info.Mode().Perm())
	}
}

func TestChmodReportsARealFailureOnABadDescriptor(t *testing.T) {
	t.Parallel()
	if err := Chmod(-1, 0o600, "f", nil); err == nil {
		t.Fatal("Chmod(-1) = nil, want an error")
	}
}

func TestChmodHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	fd, err := CreateExclusive(int(dir.Fd()), "f", 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	inj := &Injector{Step: StepChmod, Name: "f", Err: errBoom}
	if err := Chmod(fd, 0o400, "f", inj); !errors.Is(err, errBoom) {
		t.Fatalf("Chmod with injected failure = %v, want errBoom", err)
	}
}

func TestChmodFileChangesTheModeOfAnOpenFile(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	fd, err := CreateExclusive(int(dir.Fd()), "f", 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), "f"))
	t.Cleanup(func() { _ = file.Close() })
	if err := ChmodFile(file, 0o400, "f", nil); err != nil {
		t.Fatalf("ChmodFile: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir.Name(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("mode = %v, want 0400", info.Mode().Perm())
	}
}

func TestChmodFileHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	fd, err := CreateExclusive(int(dir.Fd()), "f", 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), "f"))
	t.Cleanup(func() { _ = file.Close() })
	inj := &Injector{Step: StepChmod, Name: "f", Err: errBoom}
	if err := ChmodFile(file, 0o400, "f", inj); !errors.Is(err, errBoom) {
		t.Fatalf("ChmodFile with injected failure = %v, want errBoom", err)
	}
	info, err := os.Stat(filepath.Join(dir.Name(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed despite the injected failure: %v", info.Mode().Perm())
	}
}

// TestChmodFileReportsARealFailureAsAPathError asserts ChmodFile keeps
// file.Chmod's own *fs.PathError wrapping (path plus errno) rather than
// the bare errno Chmod's Fchmod returns -- the exact drift the task-9
// PR-2 review (B1) flagged: callers at daemon.go, fleet_default_branch.go
// and daemon_process_darwin.go return this error unwrapped, and peers.go
// wraps it with %w, so both need the "chmod <path>: <errno>" text and
// errors.As(*fs.PathError) to keep working.
func TestChmodFileReportsARealFailureAsAPathError(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	fd, err := CreateExclusive(int(dir.Fd()), "f", 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), "f"))
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	err = ChmodFile(file, 0o400, "f", nil)
	if err == nil {
		t.Fatal("ChmodFile on a closed file = nil, want an error")
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("ChmodFile error = %v (%T), want a *fs.PathError", err, err)
	}
	if pathErr.Op != "chmod" {
		t.Fatalf("PathError.Op = %q, want %q", pathErr.Op, "chmod")
	}
}

// --- Write ---

func openWritableFile(t *testing.T, dir *os.File, name string) *os.File {
	t.Helper()
	fd, err := CreateExclusive(int(dir.Fd()), name, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), name)
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestWriteWritesTheFullPayload(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	file := openWritableFile(t, dir, "f")
	if err := Write(file, []byte("hello"), "f", nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir.Name(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q, want %q", got, "hello")
	}
}

func TestWriteReportsARealFailureOnAClosedFile(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	file := openWritableFile(t, dir, "f")
	_ = file.Close()
	if err := Write(file, []byte("x"), "f", nil); err == nil {
		t.Fatal("Write on a closed file = nil, want an error")
	}
}

func TestWriteHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	file := openWritableFile(t, dir, "f")
	inj := &Injector{Step: StepWrite, Name: "f", Err: errBoom}
	if err := Write(file, []byte("x"), "f", inj); !errors.Is(err, errBoom) {
		t.Fatalf("Write with injected failure = %v, want errBoom", err)
	}
}

func TestWriteReportsAShortWriteFromAnInjectedShortWrite(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	file := openWritableFile(t, dir, "f")
	inj := &Injector{Step: StepShortWrite, Name: "f", ShortBytes: 2}
	err := Write(file, []byte("hello"), "f", inj)
	var short *ShortWriteError
	if !errors.As(err, &short) {
		t.Fatalf("Write with injected short write = %v, want *ShortWriteError", err)
	}
	if short.Wrote != 2 || short.Want != 5 {
		t.Fatalf("short write = %+v, want Wrote=2 Want=5", short)
	}
	got, err := os.ReadFile(filepath.Join(dir.Name(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "he" {
		t.Fatalf("content = %q, want %q (the real, truncated write)", got, "he")
	}
}

func TestWriteClampsAnOutOfRangeShortWriteToZeroBytes(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	file := openWritableFile(t, dir, "f")
	inj := &Injector{Step: StepShortWrite, Name: "f", ShortBytes: -1}
	err := Write(file, []byte("hello"), "f", inj)
	var short *ShortWriteError
	if !errors.As(err, &short) || short.Wrote != 0 {
		t.Fatalf("Write with out-of-range ShortBytes = %v, want a ShortWriteError with Wrote=0", err)
	}
}

// --- Writer ---

func TestWriterWritesTheExactChunksItIsGiven(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	file := openWritableFile(t, dir, "f")
	w := Writer(file, "f", nil)
	if n, err := w.Write([]byte("hel")); n != 3 || err != nil {
		t.Fatalf("Write(%q) = %d, %v, want 3, nil", "hel", n, err)
	}
	if n, err := w.Write([]byte("lo")); n != 2 || err != nil {
		t.Fatalf("Write(%q) = %d, %v, want 2, nil", "lo", n, err)
	}
	got, err := os.ReadFile(filepath.Join(dir.Name(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q, want %q", got, "hello")
	}
}

func TestWriterReportsARealFailureOnAClosedFile(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	file := openWritableFile(t, dir, "f")
	_ = file.Close()
	w := Writer(file, "f", nil)
	if n, err := w.Write([]byte("x")); err == nil {
		t.Fatalf("Write(closed file) = %d, nil, want a real error", n)
	}
}

func TestWriterHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	file := openWritableFile(t, dir, "f")
	inj := &Injector{Step: StepWrite, Name: "f", Err: errBoom}
	w := Writer(file, "f", inj)
	n, err := w.Write([]byte("x"))
	if n != 0 || !errors.Is(err, errBoom) {
		t.Fatalf("Write with injected failure = %d, %v, want 0, errBoom", n, err)
	}
	if got, statErr := os.ReadFile(filepath.Join(dir.Name(), "f")); statErr != nil || len(got) != 0 {
		t.Fatalf("file content after injected failure = %q, %v, want empty (the real write must not run)", got, statErr)
	}
}

// --- Sync / Close / SyncDir ---

func TestSyncFsyncsARegularFile(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	file := openWritableFile(t, dir, "f")
	if err := Sync(file, "f", nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
}

func TestSyncReportsARealFailureOnAClosedFile(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	file := openWritableFile(t, dir, "f")
	_ = file.Close()
	if err := Sync(file, "f", nil); err == nil {
		t.Fatal("Sync on a closed file = nil, want an error")
	}
}

func TestSyncHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	file := openWritableFile(t, dir, "f")
	inj := &Injector{Step: StepSync, Name: "f", Err: errBoom}
	if err := Sync(file, "f", inj); !errors.Is(err, errBoom) {
		t.Fatalf("Sync with injected failure = %v, want errBoom", err)
	}
}

// Close is exported starting with task-9 PR-2 (see filewrite.go's doc
// comment); TestCreateExclusiveWriteSyncReportsCreatedTrueEvenWhenCloseFails
// below covers the same StepClose branch through CreateExclusiveWriteSync,
// and these three tests cover Close's own real-failure and
// leak-prevention behaviour directly.
func TestCloseClosesTheFile(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	fd, err := CreateExclusive(int(dir.Fd()), "f", 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), "f")
	if err := Close(file, "f", nil); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := file.Close(); err == nil {
		t.Fatal("file was not actually closed")
	}
}

func TestCloseReportsARealFailureOnADoubleClose(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	file := openWritableFile(t, dir, "f")
	_ = file.Close()
	if err := Close(file, "f", nil); err == nil {
		t.Fatal("Close on an already-closed file = nil, want an error")
	}
}

func TestCloseHonoursAnInjectedFailureAndStillClosesTheDescriptor(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	file := openWritableFile(t, dir, "f")
	inj := &Injector{Step: StepClose, Name: "f", Err: errBoom}
	if err := Close(file, "f", inj); !errors.Is(err, errBoom) {
		t.Fatalf("Close with injected failure = %v, want errBoom", err)
	}
	if err := file.Close(); err == nil {
		t.Fatal("descriptor was leaked: a second Close still succeeded")
	}
}

func TestSyncDirFsyncsTheDirectory(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	if err := SyncDir(dir, nil); err != nil {
		t.Fatalf("SyncDir: %v", err)
	}
}

func TestSyncDirHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	inj := &Injector{Step: StepDirSync, Err: errBoom}
	if err := SyncDir(dir, inj); !errors.Is(err, errBoom) {
		t.Fatalf("SyncDir with injected failure = %v, want errBoom", err)
	}
}

func TestSyncDirReportsARealFailureOnAClosedDirectory(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	_ = dir.Close()
	if err := SyncDir(dir, nil); err == nil {
		t.Fatal("SyncDir on a closed directory = nil, want an error")
	}
}

// --- LinkNoReplace ---

func TestLinkNoReplaceCreatesASecondNameForTheSameContent(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	fd, err := CreateExclusive(int(dir.Fd()), "old", 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	if err := LinkNoReplace(int(dir.Fd()), "old", "new", nil); err != nil {
		t.Fatalf("LinkNoReplace: %v", err)
	}
	var oldStat, newStat unix.Stat_t
	if err := unix.Lstat(filepath.Join(dir.Name(), "old"), &oldStat); err != nil {
		t.Fatal(err)
	}
	if err := unix.Lstat(filepath.Join(dir.Name(), "new"), &newStat); err != nil {
		t.Fatal(err)
	}
	if oldStat.Ino != newStat.Ino {
		t.Fatal("LinkNoReplace did not create a link to the same inode")
	}
}

func TestLinkNoReplaceReportsEEXISTOnAnExistingDestination(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	for _, name := range []string{"old", "new"} {
		fd, err := CreateExclusive(int(dir.Fd()), name, 0o600, nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = unix.Close(fd)
	}
	if err := LinkNoReplace(int(dir.Fd()), "old", "new", nil); !errors.Is(err, unix.EEXIST) {
		t.Fatalf("LinkNoReplace(existing destination) = %v, want EEXIST", err)
	}
}

func TestLinkNoReplaceHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	fd, err := CreateExclusive(int(dir.Fd()), "old", 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	inj := &Injector{Step: StepLink, Name: "new", Err: errBoom}
	if err := LinkNoReplace(int(dir.Fd()), "old", "new", inj); !errors.Is(err, errBoom) {
		t.Fatalf("LinkNoReplace with injected failure = %v, want errBoom", err)
	}
	if _, err := os.Stat(filepath.Join(dir.Name(), "new")); err == nil {
		t.Fatal("injected failure still created the link")
	}
}

// --- LinkPath (task-9 PR-4) ---

func TestLinkPathCreatesASecondNameForTheSameContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old")
	if err := os.WriteFile(oldPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(dir, "new")
	if err := LinkPath(oldPath, newPath, nil); err != nil {
		t.Fatalf("LinkPath: %v", err)
	}
	oldInfo, err := os.Stat(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	newInfo, err := os.Stat(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(oldInfo, newInfo) {
		t.Fatal("LinkPath did not create a link to the same inode")
	}
}

func TestLinkPathReportsAnErrorOnAnExistingDestination(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old")
	newPath := filepath.Join(dir, "new")
	for _, path := range []string{oldPath, newPath} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := LinkPath(oldPath, newPath, nil); !errors.Is(err, os.ErrExist) {
		t.Fatalf("LinkPath(existing destination) = %v, want a wrapped os.ErrExist", err)
	}
}

func TestLinkPathHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old")
	if err := os.WriteFile(oldPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(dir, "new")
	inj := &Injector{Step: StepLink, Name: newPath, Err: errBoom}
	if err := LinkPath(oldPath, newPath, inj); !errors.Is(err, errBoom) {
		t.Fatalf("LinkPath with injected failure = %v, want errBoom", err)
	}
	if _, err := os.Stat(newPath); err == nil {
		t.Fatal("injected failure still created the link")
	}
}

// TestLinkPathHookCreatesARealRaceBeforeTheLink asserts Injector.Hook lets
// a test build a genuine collision at LinkPath's destination -- exactly
// the pattern the 6 internal/orchestrate acknowledgement-persist call
// sites this primitive replaces need: a competing writer publishes its
// own content to newpath first, and the real os.Link call that follows
// then observes a real EEXIST, not a simulated one.
func TestLinkPathHookCreatesARealRaceBeforeTheLink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old")
	if err := os.WriteFile(oldPath, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(dir, "new")
	hookRan := false
	inj := &Injector{
		Step: StepLink,
		Name: newPath,
		Hook: func() {
			hookRan = true
			if err := os.WriteFile(newPath, []byte("competing"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	err := LinkPath(oldPath, newPath, inj)
	if !hookRan {
		t.Fatal("Hook did not run")
	}
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("LinkPath after a Hook-created collision = %v, want a wrapped os.ErrExist", err)
	}
	contents, readErr := os.ReadFile(newPath)
	if readErr != nil || string(contents) != "competing" {
		t.Fatalf("newPath contents = %q, err = %v, want the competing writer's content preserved", contents, readErr)
	}
}

// --- CreateExclusiveWriteSync ---

func TestCreateExclusiveWriteSyncWritesSyncsAndClosesANewFile(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	created, err := CreateExclusiveWriteSync(dir, "f", []byte("payload"), 0o600, nil)
	if err != nil || !created {
		t.Fatalf("CreateExclusiveWriteSync = (%v, %v), want (true, nil)", created, err)
	}
	got, err := os.ReadFile(filepath.Join(dir.Name(), "f"))
	if err != nil || string(got) != "payload" {
		t.Fatalf("content = %q, %v, want %q, nil", got, err, "payload")
	}
}

func TestCreateExclusiveWriteSyncReportsNotCreatedOnAnExistingName(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	if _, err := CreateExclusiveWriteSync(dir, "f", []byte("first"), 0o600, nil); err != nil {
		t.Fatal(err)
	}
	created, err := CreateExclusiveWriteSync(dir, "f", []byte("second"), 0o600, nil)
	if err != nil || created {
		t.Fatalf("CreateExclusiveWriteSync(existing) = (%v, %v), want (false, nil)", created, err)
	}
	got, err := os.ReadFile(filepath.Join(dir.Name(), "f"))
	if err != nil || string(got) != "first" {
		t.Fatalf("content changed after a losing write: %q, %v", got, err)
	}
}

func TestCreateExclusiveWriteSyncFailsOnAnInjectedOpenFailure(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	inj := &Injector{Step: StepOpenOrCreate, Name: "f", Err: errBoom}
	if _, err := CreateExclusiveWriteSync(dir, "f", []byte("x"), 0o600, inj); !errors.Is(err, errBoom) {
		t.Fatalf("CreateExclusiveWriteSync with injected open failure = %v, want errBoom", err)
	}
}

func TestCreateExclusiveWriteSyncClosesAndReportsAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	inj := &Injector{Step: StepWrite, Name: "f", Err: errBoom}
	created, err := CreateExclusiveWriteSync(dir, "f", []byte("x"), 0o600, inj)
	if created || !errors.Is(err, errBoom) {
		t.Fatalf("CreateExclusiveWriteSync with injected write failure = (%v, %v), want (false, errBoom)", created, err)
	}
	// The file descriptor must have been closed, not leaked: a fresh
	// CreateExclusive of the same name must succeed (EEXIST would mean
	// the aborted attempt's file is still there and open is irrelevant
	// either way, but a leaked fd would eventually exhaust the process,
	// not fail this assertion directly -- what this does prove is the
	// partially written file was left in place for inspection, matching
	// the original writeImmutableAt's behaviour of never unlinking on a
	// failed write).
	if _, err := os.Stat(filepath.Join(dir.Name(), "f")); err != nil {
		t.Fatalf("partially written file missing: %v", err)
	}
}

func TestCreateExclusiveWriteSyncReportsAnInjectedSyncFailure(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	inj := &Injector{Step: StepSync, Name: "f", Err: errBoom}
	created, err := CreateExclusiveWriteSync(dir, "f", []byte("x"), 0o600, inj)
	if created || !errors.Is(err, errBoom) {
		t.Fatalf("CreateExclusiveWriteSync with injected sync failure = (%v, %v), want (false, errBoom)", created, err)
	}
}

func TestCreateExclusiveWriteSyncReportsCreatedTrueEvenWhenCloseFails(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	inj := &Injector{Step: StepClose, Name: "f", Err: errBoom}
	created, err := CreateExclusiveWriteSync(dir, "f", []byte("x"), 0o600, inj)
	if !created || !errors.Is(err, errBoom) {
		t.Fatalf("CreateExclusiveWriteSync with injected close failure = (%v, %v), want (true, errBoom)", created, err)
	}
}

// --- OpenOrCreateRegular ---

func TestOpenOrCreateRegularCreatesAndChmodsAnAbsentFile(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	fd, err := OpenOrCreateRegular(int(dir.Fd()), "f", 0o600, nil)
	if err != nil {
		t.Fatalf("OpenOrCreateRegular: %v", err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	info, err := os.Stat(filepath.Join(dir.Name(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestOpenOrCreateRegularOpensAnExistingFileDirectly(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	if _, err := CreateExclusiveWriteSync(dir, "f", []byte("payload"), 0o644, nil); err != nil {
		t.Fatal(err)
	}
	fd, err := OpenOrCreateRegular(int(dir.Fd()), "f", 0o600, nil)
	if err != nil {
		t.Fatalf("OpenOrCreateRegular(existing): %v", err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	got, err := os.ReadFile(filepath.Join(dir.Name(), "f"))
	if err != nil || string(got) != "payload" {
		t.Fatalf("existing content lost: %q, %v", got, err)
	}
	// mode is re-asserted even on the existing-file fast path.
	info, err := os.Stat(filepath.Join(dir.Name(), "f"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v, want 0600, nil", info, err)
	}
}

func TestOpenOrCreateRegularFallsBackToOpenAfterACompetingCreate(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)

	var competingFD int
	inj := &Injector{
		Step: StepOpenOrCreate,
		Name: "contested",
		Skip: 1, // let the first (absent-check) Openat through untouched
		Hook: func() {
			fd, err := unix.Openat(int(dir.Fd()), "contested",
				unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
			if err != nil {
				t.Fatalf("competing creator inside hook: %v", err)
			}
			competingFD = fd
		}, // Hook fires exactly once (see Injector's doc comment), so it
		// cannot race its own competing file a second time even though
		// OpenOrCreateRegular's fallback-open occurrence also matches.
	}
	t.Cleanup(func() {
		if competingFD != 0 {
			_ = unix.Close(competingFD)
		}
	})

	fd, err := OpenOrCreateRegular(int(dir.Fd()), "contested", 0o600, inj)
	if err != nil {
		t.Fatalf("OpenOrCreateRegular with a competing creator = %v, want it to fall back to opening the winner's file", err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })

	var got, want unix.Stat_t
	if err := unix.Fstat(fd, &got); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fstat(competingFD, &want); err != nil {
		t.Fatal(err)
	}
	if got.Dev != want.Dev || got.Ino != want.Ino {
		t.Fatalf("OpenOrCreateRegular fallback opened a different inode: got dev=%d ino=%d, want dev=%d ino=%d", got.Dev, got.Ino, want.Dev, want.Ino)
	}

	entries, err := os.ReadDir(dir.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "contested" {
		t.Fatalf("directory entries = %v, want only contested", entries)
	}
}

func TestOpenOrCreateRegularRejectsAnExistingNameWithMoreThanOneLink(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	fd, err := CreateExclusive(int(dir.Fd()), "f", 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	if err := LinkNoReplace(int(dir.Fd()), "f", "f2", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOrCreateRegular(int(dir.Fd()), "f", 0o600, nil); err == nil {
		t.Fatal("OpenOrCreateRegular on a multiply-linked file = nil, want an error")
	}
}

func TestOpenOrCreateRegularHonoursAnInjectedFailureOnTheInitialOpen(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	inj := &Injector{Step: StepOpenOrCreate, Name: "f", Err: errBoom}
	if _, err := OpenOrCreateRegular(int(dir.Fd()), "f", 0o600, inj); !errors.Is(err, errBoom) {
		t.Fatalf("OpenOrCreateRegular with injected initial-open failure = %v, want errBoom", err)
	}
}

func TestOpenOrCreateRegularHonoursAnInjectedFailureOnTheCreateAttempt(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	inj := &Injector{Step: StepOpenOrCreate, Name: "f", Skip: 1, Err: errBoom}
	if _, err := OpenOrCreateRegular(int(dir.Fd()), "f", 0o600, inj); !errors.Is(err, errBoom) {
		t.Fatalf("OpenOrCreateRegular with injected create failure = %v, want errBoom", err)
	}
}

func TestOpenOrCreateRegularHonoursAnInjectedFailureOnTheFallbackOpenAfterEEXIST(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	inj := &Injector{
		Step: StepOpenOrCreate,
		Name: "contested",
		Skip: 1, // let the absent-check open through untouched
		Hook: func() {
			// Runs right before the O_CREAT|O_EXCL attempt, making it
			// lose to a real EEXIST -- FailAfterHook then defers this
			// Injector's Err to the fallback-open occurrence that
			// follows, instead of failing the create attempt itself.
			fd, err := unix.Openat(int(dir.Fd()), "contested",
				unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
			if err != nil {
				t.Fatalf("competing creator inside hook: %v", err)
			}
			_ = unix.Close(fd)
		},
		FailAfterHook: true,
		Err:           errBoom,
	}
	if _, err := OpenOrCreateRegular(int(dir.Fd()), "contested", 0o600, inj); !errors.Is(err, errBoom) {
		t.Fatalf("OpenOrCreateRegular with injected fallback-open failure = %v, want errBoom", err)
	}
}

func TestOpenOrCreateRegularReportsARealNonENOENTFailureOnTheInitialOpen(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	// A dangling symlink at name makes the initial O_NOFOLLOW open fail
	// with ELOOP, not ENOENT, so the function must return that error
	// directly instead of treating it as absent and trying to create it.
	if err := os.Symlink("nowhere", filepath.Join(dir.Name(), "link")); err != nil {
		t.Fatal(err)
	}
	_, err := OpenOrCreateRegular(int(dir.Fd()), "link", 0o600, nil)
	if err == nil || errors.Is(err, unix.ENOENT) {
		t.Fatalf("OpenOrCreateRegular(symlink) = %v, want a non-ENOENT error", err)
	}
}

func TestOpenOrCreateRegularHonoursAnInjectedFailureOnTheChmod(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	inj := &Injector{Step: StepChmod, Name: "f", Err: errBoom}
	if _, err := OpenOrCreateRegular(int(dir.Fd()), "f", 0o600, inj); !errors.Is(err, errBoom) {
		t.Fatalf("OpenOrCreateRegular with injected chmod failure = %v, want errBoom", err)
	}
	if _, err := os.Stat(filepath.Join(dir.Name(), "f")); err != nil {
		t.Fatalf("file created before the injected chmod failure is missing: %v", err)
	}
}

// --- CreateTemp (task-9 PR-2) ---

func TestCreateTempCreatesAUniquelyNamedFileUnderThePattern(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file, err := CreateTemp(dir, "prefix-*.tmp", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	name := filepath.Base(file.Name())
	if !strings.HasPrefix(name, "prefix-") || !strings.HasSuffix(name, ".tmp") {
		t.Fatalf("CreateTemp name = %q, want prefix-*.tmp", name)
	}
}

func TestCreateTempHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inj := &Injector{Step: StepOpenOrCreate, Err: errBoom}
	if _, err := CreateTemp(dir, "prefix-*.tmp", inj); !errors.Is(err, errBoom) {
		t.Fatalf("CreateTemp with injected failure = %v, want errBoom", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("CreateTemp created a file despite the injected failure: %v", entries)
	}
}

func TestCreateTempReportsARealFailure(t *testing.T) {
	t.Parallel()
	if _, err := CreateTemp(filepath.Join(t.TempDir(), "missing"), "prefix-*.tmp", nil); err == nil {
		t.Fatal("CreateTemp under a missing directory = nil, want an error")
	}
}

// --- CreateExclusivePath (task-9 PR-2) ---

func TestCreateExclusivePathCreatesTheFileWithExactlyTheGivenFlags(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	file, err := CreateExclusivePath(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestCreateExclusivePathFailsWhenTheFileAlreadyExists(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateExclusivePath(path, 0o600, nil); !errors.Is(err, os.ErrExist) {
		t.Fatalf("CreateExclusivePath over an existing file = %v, want ErrExist", err)
	}
}

func TestCreateExclusivePathHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	inj := &Injector{Step: StepOpenOrCreate, Name: path, Err: errBoom}
	if _, err := CreateExclusivePath(path, 0o600, inj); !errors.Is(err, errBoom) {
		t.Fatalf("CreateExclusivePath with injected failure = %v, want errBoom", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("CreateExclusivePath created a file despite the injected failure")
	}
}

// --- ChmodPath (task-9 PR-2) ---

func TestChmodPathChangesTheModeOfAnExistingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ChmodPath(path, 0o600, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestChmodPathHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	inj := &Injector{Step: StepChmod, Name: path, Err: errBoom}
	if err := ChmodPath(path, 0o600, inj); !errors.Is(err, errBoom) {
		t.Fatalf("ChmodPath with injected failure = %v, want errBoom", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode changed despite the injected failure: %v", info.Mode().Perm())
	}
}

func TestChmodPathReportsARealFailure(t *testing.T) {
	t.Parallel()
	if err := ChmodPath(filepath.Join(t.TempDir(), "missing"), 0o600, nil); err == nil {
		t.Fatal("ChmodPath on a missing file = nil, want an error")
	}
}

// --- Rename (task-9 PR-2) ---

func TestRenamePublishesOldpathToNewpath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	oldpath := filepath.Join(dir, "old")
	newpath := filepath.Join(dir, "new")
	if err := os.WriteFile(oldpath, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Rename(oldpath, newpath, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(newpath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "content" {
		t.Fatalf("content = %q, want %q", got, "content")
	}
	if _, err := os.Stat(oldpath); err == nil {
		t.Fatal("oldpath still exists after Rename")
	}
}

func TestRenameHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	oldpath := filepath.Join(dir, "old")
	newpath := filepath.Join(dir, "new")
	if err := os.WriteFile(oldpath, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	inj := &Injector{Step: StepRename, Name: newpath, Err: errBoom}
	if err := Rename(oldpath, newpath, inj); !errors.Is(err, errBoom) {
		t.Fatalf("Rename with injected failure = %v, want errBoom", err)
	}
	if _, err := os.Stat(oldpath); err != nil {
		t.Fatal("oldpath was renamed away despite the injected failure")
	}
	if _, err := os.Stat(newpath); err == nil {
		t.Fatal("newpath exists despite the injected failure")
	}
}

func TestRenameReportsARealFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := Rename(filepath.Join(dir, "missing"), filepath.Join(dir, "new"), nil); err == nil {
		t.Fatal("Rename of a missing oldpath = nil, want an error")
	}
}

// --- RenameAt (task-9 PR-3) ---

func TestRenameAtPublishesFromNameToToName(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	if err := os.WriteFile(filepath.Join(dir.Name(), "old"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RenameAt(int(dir.Fd()), "old", int(dir.Fd()), "new", nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir.Name(), "new"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "content" {
		t.Fatalf("content = %q, want %q", got, "content")
	}
	if _, err := os.Stat(filepath.Join(dir.Name(), "old")); err == nil {
		t.Fatal("fromName still exists after RenameAt")
	}
}

func TestRenameAtReplacesAnExistingToName(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	if err := os.WriteFile(filepath.Join(dir.Name(), "old"), []byte("new-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir.Name(), "new"), []byte("stale-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RenameAt(int(dir.Fd()), "old", int(dir.Fd()), "new", nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir.Name(), "new"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-content" {
		t.Fatalf("content = %q, want the replacing content", got)
	}
}

func TestRenameAtHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	if err := os.WriteFile(filepath.Join(dir.Name(), "old"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	inj := &Injector{Step: StepRename, Name: "new", Err: errBoom}
	if err := RenameAt(int(dir.Fd()), "old", int(dir.Fd()), "new", inj); !errors.Is(err, errBoom) {
		t.Fatalf("RenameAt with injected failure = %v, want errBoom", err)
	}
	if _, err := os.Stat(filepath.Join(dir.Name(), "old")); err != nil {
		t.Fatal("fromName was renamed away despite the injected failure")
	}
}

func TestRenameAtReportsARealFailure(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	if err := RenameAt(int(dir.Fd()), "missing", int(dir.Fd()), "new", nil); err == nil {
		t.Fatal("RenameAt of a missing fromName = nil, want an error")
	}
}

// --- RenameNoReplace (task-9 PR-3; performs its own syscall since
// review-756 N1, rather than running a caller-supplied closure) ---

func TestRenameNoReplaceMovesFromNameToANewToName(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	if err := os.WriteFile(filepath.Join(dir.Name(), "old"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoReplace(int(dir.Fd()), "old", int(dir.Fd()), "new", nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir.Name(), "new"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "content" {
		t.Fatalf("content = %q, want %q", got, "content")
	}
	if _, err := os.Stat(filepath.Join(dir.Name(), "old")); err == nil {
		t.Fatal("fromName still exists after RenameNoReplace")
	}
}

func TestRenameNoReplaceReportsARealFailureWhenToNameAlreadyExists(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	if err := os.WriteFile(filepath.Join(dir.Name(), "old"), []byte("new-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir.Name(), "new"), []byte("existing-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoReplace(int(dir.Fd()), "old", int(dir.Fd()), "new", nil); err == nil {
		t.Fatal("RenameNoReplace onto an existing toName = nil, want an error")
	}
	got, err := os.ReadFile(filepath.Join(dir.Name(), "new"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "existing-content" {
		t.Fatalf("toName content = %q, want the original existing content unreplaced", got)
	}
	if _, err := os.Stat(filepath.Join(dir.Name(), "old")); err != nil {
		t.Fatal("fromName was renamed away despite the no-replace refusal")
	}
}

func TestRenameNoReplaceHonoursAnInjectedFailureWithoutRunningRename(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	if err := os.WriteFile(filepath.Join(dir.Name(), "old"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	inj := &Injector{Step: StepRenameNoReplace, Name: "new", Err: errBoom}
	if err := RenameNoReplace(int(dir.Fd()), "old", int(dir.Fd()), "new", inj); !errors.Is(err, errBoom) {
		t.Fatalf("RenameNoReplace with injected failure = %v, want errBoom", err)
	}
	if _, err := os.Stat(filepath.Join(dir.Name(), "old")); err != nil {
		t.Fatal("fromName was renamed away despite the injected failure")
	}
}

func TestRenameNoReplaceReportsARealFailureFromAMissingFromName(t *testing.T) {
	t.Parallel()
	dir := openTestDir(t)
	if err := RenameNoReplace(int(dir.Fd()), "missing", int(dir.Fd()), "new", nil); err == nil {
		t.Fatal("RenameNoReplace of a missing fromName = nil, want an error")
	}
}

// --- CreateOrTruncatePath (task-9 PR-3) ---

func TestCreateOrTruncatePathCreatesAMissingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	file, err := CreateOrTruncatePath(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("created file missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("created file mode = %o, want 0600", perm)
	}
}

func TestCreateOrTruncatePathTruncatesAnExistingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("stale content"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := CreateOrTruncatePath(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("size = %d, want 0 after truncation", info.Size())
	}
}

func TestCreateOrTruncatePathHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	inj := &Injector{Step: StepOpenOrCreate, Name: path, Err: errBoom}
	if _, err := CreateOrTruncatePath(path, 0o600, inj); !errors.Is(err, errBoom) {
		t.Fatalf("CreateOrTruncatePath with injected failure = %v, want errBoom", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("injected failure still created the file")
	}
}

func TestCreateOrTruncatePathReportsARealFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing-dir", "f")
	if _, err := CreateOrTruncatePath(path, 0o600, nil); err == nil {
		t.Fatal("CreateOrTruncatePath under a missing directory = nil, want an error")
	}
}

// --- WriteFile (task-9 PR-3) ---

func TestWriteFileWritesTheGivenBytes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	if err := WriteFile(path, []byte("content"), 0o600, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "content" {
		t.Fatalf("content = %q, want %q", got, "content")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("written file mode = %o, want 0600", perm)
	}
}

func TestWriteFileHonoursAnInjectedFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	inj := &Injector{Step: StepWrite, Name: path, Err: errBoom}
	if err := WriteFile(path, []byte("content"), 0o600, inj); !errors.Is(err, errBoom) {
		t.Fatalf("WriteFile with injected failure = %v, want errBoom", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("injected failure still created the file")
	}
}

func TestWriteFileReportsARealFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing-dir", "f")
	if err := WriteFile(path, []byte("content"), 0o600, nil); err == nil {
		t.Fatal("WriteFile under a missing directory = nil, want an error")
	}
}
