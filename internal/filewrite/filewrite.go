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
// as of the PR that adds them; a primitive with no caller yet (a plain
// Rename, a RenameNoReplace, Mkdirat, or a "create, chmod, write, sync,
// but do not close" composite) is added in the PR that first needs it,
// not spuriously ahead of time.
package filewrite

import (
	"errors"
	"fmt"
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
// O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC, the flag set every
// write-once-immutable call site in this repository used. The returned
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

// Sync fsyncs a regular file.
func Sync(file *os.File, name string, inj *Injector) error {
	if err := inj.run(StepSync, name); err != nil {
		return err
	}
	return file.Sync()
}

// closeFile closes a regular file. On an injected or real failure the
// descriptor is still closed for real first, so injecting a close
// failure in a test never leaks the fd. It is unexported: every current
// production sequence that needs an injectable close reaches it through
// CreateExclusiveWriteSync; a caller that needs to inject a close failure
// on a sequence of its own is the PR that re-exports it (see this
// package's doc comment).
func closeFile(file *os.File, name string, inj *Injector) error {
	if err := inj.run(StepClose, name); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// SyncDir fsyncs a directory file descriptor -- the step that makes a
// preceding rename or link durable, separate from StepSync so a test can
// fail the directory fsync without also failing a regular file's fsync
// earlier in the same sequence.
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
	return true, closeFile(file, name, inj)
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
