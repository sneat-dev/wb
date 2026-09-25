// Package execfile writes fake executable files the way a parallel test
// binary must: without ever leaving a writable file descriptor open on the
// content's inode while another goroutine in the same process may
// fork/exec elsewhere (golang/go#22315; task-21, #739).
//
// This lives in its own leaf package, not internal/testenv, so that
// foundational packages internal/testenv itself depends on (internal/envguard)
// can use WriteExecutableFile in their own tests without an import cycle.
// internal/testenv re-exports WriteExecutableFile for callers that already
// import it there.
package execfile

import (
	"os"
	"path/filepath"
	"syscall"
)

// tempExecutableFile is the subset of *os.File that WriteExecutableFile
// needs. Production always gets *os.File; tests substitute a fake to force
// each error branch deterministically (a real fd cannot be made to fail
// Write, Chmod or Close on demand).
type tempExecutableFile interface {
	Write([]byte) (int, error)
	Chmod(os.FileMode) error
	Close() error
	Name() string
}

// createTempExecutableFile and renameExecutableFile are seams so
// WriteExecutableFile's own tests can force every error branch (a failing
// CreateTemp or Rename) without depending on real filesystem-full or
// permission conditions.
var (
	createTempExecutableFile = func(dir string) (tempExecutableFile, error) {
		return os.CreateTemp(dir, ".wexec-*")
	}
	renameExecutableFile = os.Rename
)

// WriteExecutableFile writes content to path as an executable file (mode
// perm) without ever leaving a writable file descriptor open on that
// content's inode while another goroutine in this process may fork/exec
// elsewhere.
//
// os.WriteFile is not enough for a fake executable a test is about to exec:
// it opens path directly, so any fork the Go runtime performs anywhere in
// the process between that Open and the matching Close inherits the open,
// writable file descriptor onto the child before exec replaces its image.
// A completely unrelated parallel test that execs its own fake binary by
// path can then fail with "text file busy" purely because this write
// happened to still be in flight (golang/go#22315; task-21, #739).
//
// Writing to a temporary sibling file and renaming it into place (so the
// rename is same-filesystem and atomic) is necessary but not sufficient on
// its own: a fork that races in while the temp file is still open for
// writing inherits that write-mode descriptor regardless of which path the
// temp file had at open time, and once this function's own rename gives
// that same inode the final path, a forked child that raced in earlier and
// has not yet reached its own execve is holding a write-mode descriptor on
// exactly the inode it (or a sibling exec of the same path) is about to
// exec -- ETXTBSY again, just relocated. WriteExecutableFile closes that
// window completely by holding syscall.ForkLock for reading across the
// temp file's entire open lifetime: os/exec always takes ForkLock for
// writing around its own fork+exec (see the syscall.ForkLock doc comment),
// so no fork anywhere in this process can even start while this function
// holds the read side, and no forked child can ever inherit this
// descriptor at all.
func WriteExecutableFile(path string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	syscall.ForkLock.RLock()
	tmp, err := createTempExecutableFile(dir)
	if err != nil {
		syscall.ForkLock.RUnlock()
		return err
	}
	tmpName := tmp.Name()
	writeErr := writeAndChmodTempExecutable(tmp, content, perm)
	closeErr := tmp.Close()
	syscall.ForkLock.RUnlock()

	if writeErr != nil {
		_ = os.Remove(tmpName)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpName)
		return closeErr
	}
	if renameErr := renameExecutableFile(tmpName, path); renameErr != nil {
		_ = os.Remove(tmpName)
		return renameErr
	}
	return nil
}

func writeAndChmodTempExecutable(tmp tempExecutableFile, content []byte, perm os.FileMode) error {
	if _, err := tmp.Write(content); err != nil {
		return err
	}
	return tmp.Chmod(perm)
}
