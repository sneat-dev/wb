//go:build unix

package main

import (
	"errors"
	"io/fs"
	"os"
	"testing"
)

// TestAuditReportsPropagatesAbsError covers cmd/wb/ci.go:339: auditReports
// must return filepath.Abs's error rather than continue to ciaudit.Audit.
// filepath.Abs only fails when the path is relative and os.Getwd fails; this
// test reproduces that by t.Chdir-ing into a temp directory and then
// removing it out from under the process, so Getwd can no longer resolve
// the current directory. Windows cannot remove a process's current working
// directory, so this lives in a unix-only file, and it cannot run in
// parallel because it changes the process-wide working directory.
//
// The assertion is deliberately specific to os.Getwd's own error shape
// rather than "err == nil -> Fatal": a bare nil check is also satisfied
// when :339's return is deleted, because execution then falls through to
// ciaudit.Audit with an empty path, which fails independently and returns a
// different error via :343 (a sibling line already covered by
// TestRunCIAuditPropagatesAuditError). Requiring the getwd-shaped error is
// what actually distinguishes the two, so mutating away :339 turns this red.
// Verified against this module's go1.27.1 toolchain on linux/amd64 (the
// platform CI measures coverage on): a Getwd failure surfaces as
// *os.SyscallError{Syscall: "getwd"} wrapping syscall.ENOENT, not the
// *fs.PathError shape some other stdlib versions use, so the type assertion
// below targets *os.SyscallError specifically.
//
// On darwin, getcwd(3) still resolves a removed cwd, so filepath.Abs cannot
// be made to fail this way there (confirmed: the same steps leave :339
// uncovered and :343 covered instead on macOS). Probe with a direct
// os.Getwd() call before asserting, and skip if it still succeeds.
//
// compareAgainstTarget is passed as nil: target is "" here, so ci.go:345's
// `if target != ""` guard means it is never invoked by correct code. If a
// future change to that guard made it reachable, this call would panic on
// the nil dereference instead of passing silently, which is more useful
// than an unreachable "was it called" flag would be.
func TestAuditReportsPropagatesAbsError(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Remove(dir); err != nil {
		t.Fatalf("os.Remove(%q): %v", dir, err)
	}
	if _, err := os.Getwd(); err == nil {
		t.Skip("os.Getwd still resolves a removed cwd on this platform; filepath.Abs cannot be made to fail this way here")
	}

	reports, err := auditReports([]string{"relative-repo-path"}, "", nil)
	if err == nil {
		t.Fatal("auditReports err = nil, want an error from filepath.Abs")
	}
	if reports != nil {
		t.Fatalf("reports = %+v, want nil on error", reports)
	}

	var syscallErr *os.SyscallError
	if !errors.As(err, &syscallErr) {
		t.Fatalf("err = %v (%T), want *os.SyscallError from os.Getwd", err, err)
	}
	if syscallErr.Syscall != "getwd" {
		t.Fatalf("syscallErr.Syscall = %q, want %q", syscallErr.Syscall, "getwd")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want errors.Is(err, fs.ErrNotExist)", err)
	}
}
