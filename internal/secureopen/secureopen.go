// Package secureopen is the seam every fd-relative, symlink-refusing
// directory open in this repository goes through: opening the filesystem
// root, creating-or-opening one already-validated path segment under an
// already-open parent directory descriptor, and the Fstatat probe that
// turns a plain "open failed" into a specific "refusing symlinked
// directory" error. Real, the production implementation, is exactly the
// three syscalls (Open, Mkdirat, Openat, Fstatat) internal/worktrees'
// openAbsoluteDirectoryNoFollow and openPrivateChild used inline before
// this package existed: same flags (O_NOFOLLOW|O_DIRECTORY), same
// create-tolerates-EEXIST handling, same symlink probe. Callers keep their
// own loop structure, create-vs-must-exist branching and error-wrapping
// text unchanged -- behaviour with Real is byte-for-byte what it was
// before this package existed.
//
// Fault injection is never a package-level variable: every helper takes an
// explicit Opener argument, defaulted to Real{} by the one production call
// site in each caller, so tests using different Fakes race nothing and
// stay safe under t.Parallel(). Fake implements internal/testsweep.Failer
// (CallCount/FailCall), so internal/testsweep.Sweep can drive a happy-path
// body once per call it makes to cover every error return a multi-call
// sequence reaches, matching internal/runner/runnertest.Fake and
// internal/filewrite's Injector, the two existing seams this one follows.
// Fake.Hook goes one step further than a plain injected error: it runs
// arbitrary code immediately before one chosen call's real syscall, so a
// test can create a genuine TOCTOU race -- swap a directory for a
// symlink, or replace it with a different directory, between an earlier
// check and this call -- and the real syscall that follows observes the
// real condition instead of a simulated one.
package secureopen

import (
	"sync"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// Opener is the fd-relative directory-open primitive. Real is the
// production implementation; a test builds a Fake instead.
type Opener interface {
	// OpenRoot opens root read-only, O_DIRECTORY|O_NOFOLLOW.
	OpenRoot(root string) (fd int, err error)
	// Mkdir creates name under parentFD, mode 0o755. It returns the
	// syscall's own error, including unix.EEXIST -- callers decide
	// whether EEXIST is tolerated.
	Mkdir(parentFD int, name string) error
	// OpenDir opens name under parentFD, requiring it to already exist:
	// O_RDONLY|O_DIRECTORY|O_NOFOLLOW.
	OpenDir(parentFD int, name string) (fd int, err error)
	// IsSymlink reports whether name under parentFD is a symlink. Callers
	// use it only after OpenDir has already failed, to turn a generic
	// open error into a specific "refusing symlinked directory" one; a
	// probe failure (the entry is gone, or worse) reports false, so the
	// caller falls back to its generic error, exactly as before this
	// package existed.
	IsSymlink(parentFD int, name string) bool
}

// Real is the production Opener: exactly today's syscalls, nothing else.
type Real struct{}

var _ Opener = Real{}

// OpenRoot implements Opener.
func (Real) OpenRoot(root string) (int, error) {
	return unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
}

// Mkdir implements Opener.
func (Real) Mkdir(parentFD int, name string) error {
	return unix.Mkdirat(parentFD, name, 0o755)
}

// OpenDir implements Opener.
func (Real) OpenDir(parentFD int, name string) (int, error) {
	return unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
}

// IsSymlink implements Opener.
func (Real) IsSymlink(parentFD int, name string) bool {
	var info unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &info, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false
	}
	return info.Mode&unix.S_IFMT == unix.S_IFLNK
}

// Fake is a scriptable Opener that otherwise delegates to Real, so a
// successful call still performs the real syscall and hands back a real,
// usable descriptor. Build one with NewFake, arrange FailCall and/or Hook,
// then pass it where production code takes an Opener.
//
// Fake counts OpenRoot, Mkdir, OpenDir and IsSymlink together, in the
// order it answers them, the same "every call this type answers, combined"
// convention internal/runner/runnertest.Fake uses for Run/Start/Detach/
// Interactive.
type Fake struct {
	mu      sync.Mutex
	calls   int
	failAt  int // 1-indexed call number FailCall targets; 0 disables it.
	failErr error
	hookAt  int // 1-indexed call number Hook targets; 0 disables it.
	hook    func()
}

var _ Opener = (*Fake)(nil)

// NewFake returns a Fake that delegates every call to Real until FailCall
// or Hook is armed.
func NewFake() *Fake { return &Fake{} }

// CallCount reports how many calls the Fake has answered so far, across
// OpenRoot, Mkdir, OpenDir and IsSymlink combined. It implements
// internal/testsweep.Failer.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// FailCall arranges for the callNum'th call (1-indexed, counting OpenRoot,
// Mkdir, OpenDir and IsSymlink together in the order the Fake answers
// them) to fail with err instead of performing its real syscall; every
// other call keeps delegating to Real. IsSymlink has no error to return,
// so a failed IsSymlink call reports false -- its documented behaviour for
// "the probe itself did not work". A callNum of 0 disables the override.
// It implements internal/testsweep.Failer.
func (f *Fake) FailCall(callNum int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAt = callNum
	f.failErr = err
}

// Hook arranges for fn to run once, immediately before the callNum'th call
// performs its real syscall -- letting a test create a genuine race (swap
// a directory for a symlink, replace it with a different directory,
// remove it) that the real syscall which follows then observes for real,
// instead of a simulated failure. It works standalone or alongside
// FailCall, which may target the same or a different call number; when
// both target the same call, Hook runs first and the call still fails
// with FailCall's error rather than reaching the real syscall.
func (f *Fake) Hook(callNum int, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hookAt = callNum
	f.hook = fn
}

// next records one call, runs any Hook armed for it, and reports the error
// (if any) FailCall armed for it.
func (f *Fake) next() error {
	f.mu.Lock()
	f.calls++
	n := f.calls
	var fn func()
	if f.hookAt == n {
		fn = f.hook
		f.hook = nil
	}
	var failErr error
	armed := f.failAt == n
	if armed {
		failErr = f.failErr
	}
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
	if armed {
		return failErr
	}
	return nil
}

// OpenRoot implements Opener.
func (f *Fake) OpenRoot(root string) (int, error) {
	if err := f.next(); err != nil {
		return -1, err
	}
	return Real{}.OpenRoot(root)
}

// Mkdir implements Opener.
func (f *Fake) Mkdir(parentFD int, name string) error {
	if err := f.next(); err != nil {
		return err
	}
	return Real{}.Mkdir(parentFD, name)
}

// OpenDir implements Opener.
func (f *Fake) OpenDir(parentFD int, name string) (int, error) {
	if err := f.next(); err != nil {
		return -1, err
	}
	return Real{}.OpenDir(parentFD, name)
}

// IsSymlink implements Opener.
func (f *Fake) IsSymlink(parentFD int, name string) bool {
	if err := f.next(); err != nil {
		return false
	}
	return Real{}.IsSymlink(parentFD, name)
}
