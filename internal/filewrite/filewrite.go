// Package filewrite is the one seam every fd-relative temp-file
// write -> sync -> chmod -> close -> publish (rename or link) sequence in
// this repository goes through. Each exported function wraps exactly one
// syscall (or, for Write, one syscall plus the short-write check no call
// site previously made) with an injectable failure point, and does
// nothing else: callers keep their own control flow, EEXIST/ENOENT
// branching and error-wrapping text unchanged, so behaviour with a nil
// *Injector -- what every production call site passes -- is byte-for-byte
// what it was before this package existed.
//
// Fault injection is never a package-level variable. Each function takes
// an explicit *Injector argument; a test builds its own Injector and
// passes it directly to the call under test, so tests using different
// Injectors race nothing and stay safe under t.Parallel(). An *Injector
// itself is not goroutine-safe (Skip and hookDone mutate in place): one
// Injector belongs to one call chain in one goroutine, never shared
// across concurrently running calls.
//
// This package's exported surface only includes primitives spec/plans/
// coverage-to-100 task-9's PR series has an actual production caller for
// as of the PR that adds them; a primitive with no caller yet (a
// Mkdirat, or a "create, chmod, write, sync, but do not close" composite)
// is added in the PR that first needs it, not spuriously ahead of time.
// CreateTemp, CreateExclusivePath, ChmodPath, Rename, and the exported
// Close were added in task-9 PR-2, the first PR with a path-based
// (rather than fd-relative) call site. ChmodFile was added in the same
// PR's review round, for a call site holding an *os.File rather than a
// bare fd, so it keeps file.Chmod's *fs.PathError wrapping instead of
// Chmod's bare Fchmod errno. RenameAt, RenameNoReplace,
// CreateOrTruncatePath, and WriteFile were added in task-9 PR-3. LinkPath
// was added in task-9 PR-4, replacing 6 internal/orchestrate
// package-level os.Link aliases (var linkXxx = os.Link) that existed only
// as an ad hoc test seam -- exactly what this package's explicit
// no-package-level-variable rule above exists to replace.
package filewrite

import (
	"errors"
	"fmt"
	"io"
	"os"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// Step names one point in a write sequence an Injector can make fail.
type Step string

const (
	// StepOpenOrCreate covers CreateExclusive's create-or-fail Openat call
	// and OpenOrCreateRegular's own create-or-fall-back Openat calls --
	// every occurrence where this package's job is to produce a file
	// descriptor for a name that may not exist yet.
	StepOpenOrCreate Step = "open_or_create"
	// StepOpen covers OpenReadOnly's Openat call: reopening a name this
	// package (or its caller) already knows exists, to read it back. It is
	// a separate Step from StepOpenOrCreate -- rather than the two sharing
	// one name and being distinguished only by the Name field -- so a test
	// can target "the create" or "the reopen" of the same sequence without
	// needing to know each call's exact Name.
	StepOpen Step = "open"
	// StepWrite covers the write itself, before the real syscall runs.
	StepWrite Step = "write"
	// StepShortWrite forces Write's underlying syscall to report fewer
	// bytes written than were requested, without an error -- the branch
	// no call site in this repository checked for before this package.
	StepShortWrite Step = "short_write"
	// StepSync covers a regular file's Sync (fsync).
	StepSync Step = "sync"
	// StepChmod covers Fchmod.
	StepChmod Step = "chmod"
	// StepClose covers a regular file's Close.
	StepClose Step = "close"
	// StepLink covers Linkat.
	StepLink Step = "link"
	// StepDirSync covers a directory file descriptor's Sync (fsync),
	// separate from StepSync so a test can fail the directory fsync
	// without also failing the regular file's own fsync.
	StepDirSync Step = "dir_sync"
	// StepRename covers a path-based os.Rename publish -- the sequence
	// this package's cmd/wb call sites use in place of the fd-relative
	// Linkat every internal/sessionpark/internal/sessionmove call site
	// used (task-9 PR-2: these sites resolve a plain absolute path, with
	// no already-open parent directory descriptor to rename relative to).
	StepRename Step = "rename"
	// StepRenameNoReplace covers a fd-relative no-replace rename publish
	// (RenameNoReplace) -- the sequence task-9 PR-3's
	// internal/worktrees/worklog.go:writeBytesImmutableAt uses in place of
	// LinkNoReplace's Linkat, so two racing publishers of identical
	// content converge on whichever one wins the rename instead of each
	// producing its own inode.
	StepRenameNoReplace Step = "rename_no_replace"
)

// Injector lets a test force one named step to fail, or run a hook
// immediately before it, for one chosen name. The zero value and a nil
// *Injector inject nothing.
//
// Skip counts how many otherwise-matching occurrences to let through
// silently first -- this is how a test reaches, for example, the second
// of three Openat calls the same function makes for the same name. Once
// Skip is exhausted, Hook (if set) runs exactly once, on the next
// occurrence; every occurrence at or after that point returns Err (which
// may be nil, if a test only wants Hook's side effect and no failure).
// FailAfterHook defers Err until the occurrence after Hook's, so a test
// can use Hook to create a real race that the occurrence it fires on
// must survive, and fail only a later occurrence of the same step and
// name -- see OpenOrCreateRegular's tests for both patterns.
type Injector struct {
	// Step is the step to act on. The zero Step ("") matches nothing.
	Step Step
	// Name restricts the injection to one file/directory name; empty
	// matches every name at Step.
	Name string
	// Skip is how many matching occurrences to let through before this
	// Injector starts acting.
	Skip int
	// Err is returned in place of the step's real error once this
	// Injector is acting. Only meaningful for steps other than
	// StepShortWrite, which never returns Err directly (see ShortBytes).
	Err error
	// ShortBytes is the number of leading bytes of the write's payload to
	// actually write once a StepShortWrite Injector is acting; the rest
	// are silently dropped by the (real, not simulated) call to Write,
	// which then reports the short count truthfully.
	ShortBytes int
	// Hook, when set, runs once, immediately before the real syscall for
	// the first occurrence at or after Skip. It lets a test create a
	// genuine race -- for example, creating a competing file between an
	// open-or-create sequence's ENOENT check and its own O_CREAT|O_EXCL
	// attempt -- so the following real syscall observes a real EEXIST
	// instead of the test injecting one.
	Hook func()
	// FailAfterHook defers Err to the occurrence after the one Hook fires
	// on (which then returns nil), instead of applying Err to that same
	// occurrence. Meaningless without Hook set.
	FailAfterHook bool

	hookDone bool
}

// run applies inj at step/name, returning the error to substitute for the
// real syscall's outcome.
func (inj *Injector) run(step Step, name string) error {
	if inj == nil || inj.Step != step {
		return nil
	}
	if inj.Name != "" && inj.Name != name {
		return nil
	}
	if inj.Skip > 0 {
		inj.Skip--
		return nil
	}
	if inj.Hook != nil && !inj.hookDone {
		inj.hookDone = true
		inj.Hook()
		if inj.FailAfterHook {
			return nil
		}
	}
	return inj.Err
}

// shortWrite reports whether inj is acting on a short write at name, and
// if so, how many bytes should actually reach the real Write call.
func (inj *Injector) shortWrite(name string) (int, bool) {
	if inj == nil || inj.Step != StepShortWrite {
		return 0, false
	}
	if inj.Name != "" && inj.Name != name {
		return 0, false
	}
	if inj.Skip > 0 {
		inj.Skip--
		return 0, false
	}
	if inj.Hook != nil && !inj.hookDone {
		inj.hookDone = true
		inj.Hook()
	}
	return inj.ShortBytes, true
}

// ShortWriteError reports that a write returned fewer bytes than
// requested with no error -- possible under the io.Writer contract, if
// never previously observed from any of this package's call sites on a
// regular local file, and never previously checked for by any of them.
type ShortWriteError struct {
	Name  string
	Wrote int
	Want  int
}

func (e *ShortWriteError) Error() string {
	return fmt.Sprintf("filewrite: short write to %q: wrote %d of %d bytes", e.Name, e.Wrote, e.Want)
}

// CreateExclusive opens name under the directory identified by
// directoryFD for writing, creating it and failing if it already exists:
// O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC. Most write-once-immutable
// call sites in this repository already used exactly this flag set before
// migrating here. The two task-9 PR-3 exceptions --
// internal/worktrees/worklog.go's writeBytesImmutableAtInjected and
// writeBytesAtomicAtInjected -- previously opened their temp file with a
// raw unix.Openat that omitted O_CLOEXEC; routing them through
// CreateExclusive is a deliberate behaviour change (review-756 B3), not an
// oversight: it closes a real fd leak, where a child process exec'd while
// the temp file was open (git and friends) used to inherit a writable fd
// on it, and nothing in this repository depends on that leak. The returned
// error is the syscall's own (typically wrapped in nothing further, so
// callers keep their existing errors.Is(err, unix.EEXIST) checks).
func CreateExclusive(directoryFD int, name string, mode uint32, inj *Injector) (int, error) {
	if err := inj.run(StepOpenOrCreate, name); err != nil {
		return -1, err
	}
	return unix.Openat(directoryFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
}

// OpenReadOnly opens an existing name under directoryFD read-only:
// O_RDONLY|O_NOFOLLOW|O_CLOEXEC, the flag set every read-back-to-verify
// call site in this repository used.
func OpenReadOnly(directoryFD int, name string, inj *Injector) (int, error) {
	if err := inj.run(StepOpen, name); err != nil {
		return -1, err
	}
	return unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// Chmod re-asserts mode on the open file descriptor fd (Fchmod), the
// belt-and-braces step call sites that cannot rely solely on O_CREAT's
// mode (because of umask, or because the descriptor was opened without
// O_CREAT) took explicitly.
func Chmod(fd int, mode uint32, name string, inj *Injector) error {
	if err := inj.run(StepChmod, name); err != nil {
		return err
	}
	return unix.Fchmod(fd, mode)
}

// ChmodFile re-asserts mode on an already-open *os.File via file.Chmod,
// the belt-and-braces step a call site holding an *os.File (rather than a
// bare fd from a fd-relative open) took explicitly. Unlike Chmod, which
// wraps the bare unix.Fchmod errno, this preserves file.Chmod's own
// *fs.PathError wrapping (path plus an EINTR retry on some platforms) --
// the exact behaviour task-9 PR-2's daemon.go, fleet_default_branch.go,
// peers.go and daemon_process_darwin.go call sites had before their
// migration and must keep, since callers wrap or match on that error.
func ChmodFile(file *os.File, mode os.FileMode, name string, inj *Injector) error {
	if err := inj.run(StepChmod, name); err != nil {
		return err
	}
	return file.Chmod(mode)
}

// Write writes the full contents of data to file in one call. A short
// write (Write returning n < len(data) with a nil error) is reported as
// *ShortWriteError rather than silently accepted -- new behaviour this
// package adds, unreachable in production (a regular local file's Write
// either completes or errors) and reachable only through a StepShortWrite
// Injector, which is the point.
func Write(file *os.File, data []byte, name string, inj *Injector) error {
	if err := inj.run(StepWrite, name); err != nil {
		return err
	}
	toWrite := data
	if short, ok := inj.shortWrite(name); ok {
		if short < 0 || short > len(data) {
			short = 0
		}
		toWrite = data[:short]
	}
	n, err := file.Write(toWrite)
	if err != nil {
		return err
	}
	if n != len(data) {
		return &ShortWriteError{Name: name, Wrote: n, Want: len(data)}
	}
	return nil
}

// writer adapts an already-open *os.File to io.Writer, routing every Write
// call through the same Injector.Step (StepWrite) and Name key that a
// single-shot Write call above uses. See Writer's doc comment for why this
// exists and what it deliberately does not do.
type writer struct {
	file *os.File
	name string
	inj  *Injector
}

// var _ io.ReaderFrom = (*writer)(nil) pins the io.Copy fast-path contract
// at compile time (review-t9-pr8 N6): if ReadFrom were ever dropped from
// *writer, this line -- not just a test -- would fail to build.
var _ io.ReaderFrom = (*writer)(nil)

// Write makes byte-identical write calls and error text to a direct
// file.Write with a nil Injector: it runs the same injection check as
// Write above, then calls file.Write(p) once and returns its real (n,
// err) unchanged. Unlike Write, it never checks for a short write itself
// -- it is a plain io.Writer, and its caller (bufio.Writer, io.Copy, ...)
// already treats n < len(p) with a nil error as its own short-write
// condition, exactly as it would writing to file directly.
func (w *writer) Write(p []byte) (int, error) {
	if err := w.inj.run(StepWrite, w.name); err != nil {
		return 0, err
	}
	return w.file.Write(p)
}

// Writer wraps file as an io.Writer whose every Write call is injectable
// at StepWrite/name -- the seam for a call site that hands its temp file
// to a streaming or buffered writer (bufio.Writer, io.Copy, ...) instead
// of assembling one []byte and calling Write once. With a nil Injector
// this issues exactly the same file.Write calls, in the same chunks its
// caller already made, and passes the same error up unwrapped: a
// bufio.Writer or io.Copy already writes and errors byte-identically
// whether its io.Writer happens to be *os.File directly or this thin
// wrapper around it -- including an io.Copy whose *source* is itself an
// *os.File or a socket (task-9 PR-6 review note N3): the returned value
// also implements io.ReaderFrom (see ReadFrom below), so io.Copy still
// takes its copy_file_range/splice/sendfile fast path exactly as it would
// writing to file directly. On that fast path, StepWrite injection fires
// once for the whole io.Copy, not once per chunk as it would for the
// plain Write path (review-t9-pr8 N7).
func Writer(file *os.File, name string, inj *Injector) io.Writer {
	return &writer{file: file, name: name, inj: inj}
}

// ReadFrom implements io.ReaderFrom, restoring io.Copy's
// copy_file_range/splice/sendfile fast path for a call site whose source
// is itself an *os.File or a socket (review-t9-pr6 N3): io.Copy only takes
// that fast path when its destination implements io.ReaderFrom, and *os.File
// itself does (see os.File's own ReadFrom, added for exactly this reason).
// This runs the same injection check as Write above once, then delegates
// to file.ReadFrom(r) and returns its real (n, err) unchanged -- with a nil
// Injector this is byte-identical to io.Copy writing to file directly,
// including which fast path the runtime picks.
func (w *writer) ReadFrom(r io.Reader) (int64, error) {
	if err := w.inj.run(StepWrite, w.name); err != nil {
		return 0, err
	}
	return w.file.ReadFrom(r)
}

// Sync fsyncs a regular file via file.Sync(). Every inline call site this
// package replaces already called file.Sync() on the same *os.File
// before this package existed, so this call's behaviour -- including on
// Darwin, where Go's os.File.Sync implementation issues
// fcntl(F_FULLFSYNC) rather than plain fsync(2) (see Go's
// internal/poll/fd_fsync_darwin.go) -- is unchanged. See SyncDir's doc
// comment for the one real durability change this package does
// introduce: a directory sync that used to go through unix.Fsync (plain
// fsync(2) on every platform) now goes through this same file.Sync()
// path.
func Sync(file *os.File, name string, inj *Injector) error {
	if err := inj.run(StepSync, name); err != nil {
		return err
	}
	return file.Sync()
}

// Close closes a regular file (or a directory file descriptor opened as
// an *os.File, e.g. for SyncDir's caller). On an injected or real failure
// the descriptor is still closed for real first, so injecting a close
// failure in a test never leaks the fd. Exported starting with task-9
// PR-2: CreateExclusiveWriteSync was its only caller through PR-1, which
// needed no exported access; PR-2's path-based write-then-rename sites
// each close their own *os.File directly (not through a single shared
// composite), so they need this call directly.
func Close(file *os.File, name string, inj *Injector) error {
	if err := inj.run(StepClose, name); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// SyncDir fsyncs a directory file descriptor via directory.Sync() -- the
// step that makes a preceding rename or link durable, separate from
// StepSync so a test can fail the directory fsync without also failing a
// regular file's fsync earlier in the same sequence.
//
// This is the one real durability change this package introduces: every
// inline directory sync it replaces (e.g. internal/sessionmove/
// store.go:publishImmutableAt, before this package existed) called
// unix.Fsync(fd), plain fsync(2) on every platform including Darwin.
// directory.Sync() calls Go's os.File.Sync, which on Darwin issues
// fcntl(F_FULLFSYNC) instead (see Go's internal/poll/fd_fsync_darwin.go)
// -- a stronger, slower durability guarantee than plain fsync(2). On
// every other platform os.File.Sync is fsync(2), the same as unix.Fsync
// was, so this is a Darwin-only behaviour change, and strictly a
// strengthening (never a weakening) of what the directory sync
// guarantees.
func SyncDir(directory *os.File, inj *Injector) error {
	if err := inj.run(StepDirSync, directory.Name()); err != nil {
		return err
	}
	return directory.Sync()
}

// LinkNoReplace hard-links oldName to newName, both fd-relative, failing
// instead of replacing an existing newName -- the publication step
// content-addressed immutable writes use in place of a rename, so two
// racing publishers of identical content converge on one shared inode
// instead of each producing their own.
func LinkNoReplace(directoryFD int, oldName, newName string, inj *Injector) error {
	if err := inj.run(StepLink, newName); err != nil {
		return err
	}
	return unix.Linkat(directoryFD, oldName, directoryFD, newName, 0)
}

// LinkPath hard-links oldpath to newpath, both resolved paths, failing
// instead of replacing an existing newpath (os.Link's own contract) --
// the path-based twin of LinkNoReplace for a task-9 PR-4
// internal/orchestrate call site that already has a resolved absolute
// destination path and no parent directory descriptor to link relative
// to. It shares StepLink with LinkNoReplace: a test targets "the link"
// step regardless of which of the two publishes it. An Injector's Hook
// can create a real race here exactly as documented on Injector.Hook --
// for example writing a competing acknowledgement to newpath and then
// returning os.ErrExist, so the following assertion observes a real
// collision instead of a simulated one.
func LinkPath(oldpath, newpath string, inj *Injector) error {
	if err := inj.run(StepLink, newpath); err != nil {
		return err
	}
	return os.Link(oldpath, newpath)
}

// CreateExclusiveWriteSync creates name under directory (O_WRONLY|
// O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC, mode), writes raw, fsyncs and
// closes -- the write-once-immutable sequence with no separate Fchmod
// step. It reports (false, nil) when name already exists (EEXIST), the
// existing idempotent-write contract every caller of this sequence
// relied on, and (true, err) once content has been durably written even
// if the final Close then fails, matching every existing caller's own
// "return true, file.Close()" tail exactly.
func CreateExclusiveWriteSync(directory *os.File, name string, raw []byte, mode os.FileMode, inj *Injector) (bool, error) {
	fd, err := CreateExclusive(int(directory.Fd()), name, uint32(mode), inj)
	if errors.Is(err, unix.EEXIST) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(fd), name)
	if err := Write(file, raw, name, inj); err != nil {
		_ = file.Close()
		return false, err
	}
	if err := Sync(file, name, inj); err != nil {
		_ = file.Close()
		return false, err
	}
	return true, Close(file, name, inj)
}

// OpenOrCreateRegular opens name under directoryFD for read-write,
// creating it with mode if absent, and reports an error if a competing
// creator wins the race and what results is not one regular,
// single-link file: O_RDWR|O_NOFOLLOW|O_CLOEXEC, escalating through
// O_CREAT|O_EXCL on ENOENT and falling back to a plain open on a losing
// EEXIST, then Fchmod to re-assert mode regardless of which path
// produced the descriptor (a competing creator's mode is not trusted).
func OpenOrCreateRegular(directoryFD int, name string, mode uint32, inj *Injector) (int, error) {
	const flags = unix.O_RDWR | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if err := inj.run(StepOpenOrCreate, name); err != nil {
		return -1, err
	}
	fd, err := unix.Openat(directoryFD, name, flags, 0)
	if errors.Is(err, unix.ENOENT) {
		if err := inj.run(StepOpenOrCreate, name); err != nil {
			return -1, err
		}
		fd, err = unix.Openat(directoryFD, name, flags|unix.O_CREAT|unix.O_EXCL, mode)
		if errors.Is(err, unix.EEXIST) {
			if err := inj.run(StepOpenOrCreate, name); err != nil {
				return -1, err
			}
			fd, err = unix.Openat(directoryFD, name, flags, 0)
		}
	}
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("%q is not one regular file", name)
	}
	if err := Chmod(fd, mode, name, inj); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// CreateTemp creates a new temporary file in directory whose name begins
// with pattern (an os.CreateTemp "*"-pattern), returning the open file --
// the path-based twin of CreateExclusive, for the cmd/wb call sites
// (task-9 PR-2) that resolve a plain directory path rather than holding
// an already-open parent directory descriptor. It shares StepOpenOrCreate
// with CreateExclusive and OpenOrCreateRegular: all three produce a file
// descriptor for a name that may not exist yet.
//
// The injector's Name key for this step is pattern, not the resolved
// unique temp file name -- os.CreateTemp only picks the actual name once
// it succeeds, so there is nothing else to key on before the call runs.
// A later Step (Chmod, Write, Sync, Close, Rename, ...) on the same call
// chain keys on the resolved name instead (temporary.Name()); a test that
// wants to target both CreateTemp's own failure and a later step's
// failure with one Injector.Name cannot -- every existing test targets one
// step per Injector, so this has not mattered in practice.
func CreateTemp(directory, pattern string, inj *Injector) (*os.File, error) {
	if err := inj.run(StepOpenOrCreate, pattern); err != nil {
		return nil, err
	}
	return os.CreateTemp(directory, pattern)
}

// CreateExclusivePath opens path for writing, creating it and failing if
// it already exists: O_WRONLY|O_CREAT|O_EXCL, mode -- the path-based twin
// of CreateExclusive for a cmd/wb call site (task-9 PR-2) that already has
// a resolved absolute path and no parent directory descriptor to open
// relative to. Unlike CreateExclusive's fd-relative Openat, this does not
// add O_NOFOLLOW or O_CLOEXEC: every call site this replaces used plain
// os.OpenFile with exactly this flag set, and preserving that exactly is
// this migration's contract (adding either flag would be a behaviour
// change, not a refactor).
func CreateExclusivePath(path string, mode os.FileMode, inj *Injector) (*os.File, error) {
	if err := inj.run(StepOpenOrCreate, path); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
}

// ChmodPath re-asserts mode on path (os.Chmod), the path-based twin of
// Chmod's Fchmod for a cmd/wb call site (task-9 PR-2) that re-asserts a
// temporary or existing file's mode after its descriptor is already
// closed, or that never opened one at all.
func ChmodPath(path string, mode os.FileMode, inj *Injector) error {
	if err := inj.run(StepChmod, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// Rename publishes oldpath to newpath (os.Rename) -- the path-based twin
// of LinkNoReplace's Linkat for the cmd/wb call sites (task-9 PR-2) that
// publish by replacing rather than by content-addressed hard link.
func Rename(oldpath, newpath string, inj *Injector) error {
	if err := inj.run(StepRename, newpath); err != nil {
		return err
	}
	return os.Rename(oldpath, newpath)
}

// RenameAt publishes a fd-relative rename (unix.Renameat), replacing any
// existing toName -- the fd-relative twin of Rename for a task-9 PR-3
// internal/worktrees call site that already holds an open directory
// descriptor and has no reason to resolve a path through it.
func RenameAt(fromDirectoryFD int, fromName string, toDirectoryFD int, toName string, inj *Injector) error {
	if err := inj.run(StepRename, toName); err != nil {
		return err
	}
	return unix.Renameat(fromDirectoryFD, fromName, toDirectoryFD, toName)
}

// RenameNoReplace publishes a fd-relative, no-replace rename
// (renameat2's RENAME_NOREPLACE on Linux, renameatx_np's RENAME_EXCL on
// Darwin, an explicit unsupported error elsewhere) -- the fd-relative,
// content-addressed-publish twin of RenameAt, used where two racing
// publishers of identical content must converge on whichever one wins
// the rename instead of each producing its own inode.
//
// This package owns the platform mechanics itself (task-9 PR-3
// review-756 N1): earlier, a caller built the already-resolved rename
// itself and handed RenameNoReplace a closure to run, which meant this
// package's 100% coverage said nothing about the renameat2/renameatx_np
// mechanics. internal/worktrees' own renameNoReplace, which is also
// called from unrelated, non-write-sequence call sites this task does
// not touch, is now a thin delegate to this function instead of an
// independent implementation.
func RenameNoReplace(fromDirectoryFD int, fromName string, toDirectoryFD int, toName string, inj *Injector) error {
	if err := inj.run(StepRenameNoReplace, toName); err != nil {
		return err
	}
	return renameNoReplaceSyscall(fromDirectoryFD, fromName, toDirectoryFD, toName)
}

// CreateOrTruncatePath opens path for writing, creating it if it does
// not exist and truncating it to empty if it does: O_WRONLY|O_CREAT|
// O_TRUNC, mode -- the shape a task-9 PR-3 call site uses for a fixed
// (not uniquely-named) temporary path it always fully overwrites, unlike
// CreateExclusivePath's refuse-if-present contract.
func CreateOrTruncatePath(path string, mode os.FileMode, inj *Injector) (*os.File, error) {
	if err := inj.run(StepOpenOrCreate, path); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
}

// OpenAppend opens path for appending, creating it with mode if it does
// not exist: O_APPEND|O_CREATE|O_WRONLY -- the shape a task-9 PR-8 call
// site uses for a small, not-durability-critical sidecar file (a git
// exclude file, not an append-only durable log) that is opened, appended
// to once, and closed, with no separate publish/rename step at all.
func OpenAppend(path string, mode os.FileMode, inj *Injector) (*os.File, error) {
	if err := inj.run(StepOpenOrCreate, path); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, mode)
}

// WriteFile writes data to path in one call (os.WriteFile: open-create-
// truncate, write, close, no fsync) -- the shape task-9 PR-3's
// WriteFile-to-temp+Rename call sites use for their temporary half, where
// the original inline code never called Sync either.
func WriteFile(path string, data []byte, mode os.FileMode, inj *Injector) error {
	if err := inj.run(StepWrite, path); err != nil {
		return err
	}
	return os.WriteFile(path, data, mode)
}

// CreateScratch creates a uniquely-named temporary file in directory whose
// name begins with pattern (an os.CreateTemp "*"-pattern), optionally
// re-asserts mode on it, optionally writes content, and closes it --
// task-9 PR-9's shared shape for a scratch reservation this repository
// never publishes by rename or link: some callers write nothing through
// this call at all (a coverage-profile placeholder another `go test
// -coverprofile` invocation fills in later, a git-index or writability-probe
// name reserved and then freed, never durably read back); others write
// content once, for a single subprocess call to consume as its input, and
// remove the reservation once that call returns.
//
// mode == 0 skips the ChmodFile step entirely (os.CreateTemp's own file
// already carries mode 0600, so most callers have nothing to re-assert);
// content == nil skips the Write step entirely, distinct from writing zero
// bytes, which still makes one Write call. Passing both zero values reduces
// this to a bare CreateTemp+Close, task-9 PR-9's Category D scratch shape.
//
// path is returned non-empty exactly when CreateTemp itself succeeded, even
// if a later ChmodFile, Write, or Close then fails: a caller whose original
// inline sequence removed the reservation on any such later failure needs
// the name to do that; a caller whose original sequence left the
// reservation in place (or silently ignored a Close failure) can tell the
// two situations apart the same way, by checking path == "" -- did
// CreateTemp itself produce a name, or not. Like every other single-step
// primitive in this package (CreateTemp, Close, ...), CreateScratch never
// removes the reservation itself on any failure: each call site keeps
// deciding its own cleanup, exactly as its original inline sequence did, so
// migrating to this composite changes no site's on-disk-failure behaviour.
func CreateScratch(directory, pattern string, mode os.FileMode, content []byte, inj *Injector) (path string, err error) {
	file, err := CreateTemp(directory, pattern, inj)
	if err != nil {
		return "", err
	}
	path = file.Name()
	if mode != 0 {
		if err := ChmodFile(file, mode, path, inj); err != nil {
			_ = Close(file, path, inj)
			return path, err
		}
	}
	if content != nil {
		if err := Write(file, content, path, inj); err != nil {
			_ = Close(file, path, inj)
			return path, err
		}
	}
	if err := Close(file, path, inj); err != nil {
		return path, err
	}
	return path, nil
}
