package quality

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNoInlineWriteSequencesOutsideFilewrite guards spec/plans/
// coverage-to-100 task-9's cutover: no non-test function outside
// internal/filewrite (and internal/unixcompat, the syscall shim it and
// everything else is built on) may call a create or publish primitive this
// package bans. A new inline sequence must either be routed through
// internal/filewrite or be added to PendingMigrationExemptions (a real
// site still awaiting migration) or NotAFileWritePublishExemptions (a
// false positive that is not a write-and-publish sequence at all), each
// with a reason -- never silently reintroduced.
func TestNoInlineWriteSequencesOutsideFilewrite(t *testing.T) {
	t.Parallel()
	root, err := ParallelGuardModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	violations, err := FindInlineWriteSequences(root, PendingMigrationExemptions, NotAFileWritePublishExemptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		for _, v := range violations {
			t.Errorf("%s", v)
		}
		t.Fatalf("%d function(s) call a create/write/publish primitive directly outside internal/filewrite; route them through it or add a named, reasoned entry to PendingMigrationExemptions or NotAFileWritePublishExemptions", len(violations))
	}
}

// TestEveryFilewriteBoundaryExemptionMatchesALiveViolation asserts every
// entry in both allow-lists still names a site FindInlineWriteSequences
// currently detects. Without this, an exemption could go stale (its
// function renamed, deleted, or already migrated) or be padded with a
// site the detector never actually flags, and nothing would notice --
// silently defeating the "the allow-list can only shrink" property task-9
// depends on. It reads the package-level maps but only ever reads them
// (this test and TestNoInlineWriteSequencesOutsideFilewrite are the only
// two that do), so it races nothing.
func TestEveryFilewriteBoundaryExemptionMatchesALiveViolation(t *testing.T) {
	t.Parallel()
	root, err := ParallelGuardModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	empty := map[string]string{}
	rawViolations, err := FindInlineWriteSequences(root, empty, empty)
	if err != nil {
		t.Fatal(err)
	}
	live := map[string]bool{}
	for _, v := range rawViolations {
		live[v.File+":"+v.Func] = true
	}
	for key, reason := range PendingMigrationExemptions {
		if !live[key] {
			t.Errorf("PendingMigrationExemptions[%q] = %q no longer matches a live violation; remove it (the site was migrated, renamed, or never matched)", key, reason)
		}
	}
	for key, reason := range NotAFileWritePublishExemptions {
		if !live[key] {
			t.Errorf("NotAFileWritePublishExemptions[%q] = %q no longer matches a live violation; remove it (the site was renamed, deleted, or never matched)", key, reason)
		}
	}
	for key := range PendingMigrationExemptions {
		if _, dup := NotAFileWritePublishExemptions[key]; dup {
			t.Errorf("%q is listed in both allow-lists; a site is either a real pending migration or not a write publish, never both", key)
		}
	}
}

func writeBoundaryFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindInlineWriteSequencesFlagsAnOSRenameCall(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/write.go", `package pkg

import "os"

func writeThenRename(temporary, name string) error {
	return os.Rename(temporary, name)
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Func != "writeThenRename" {
		t.Fatalf("violations = %v, want exactly one for writeThenRename", violations)
	}
}

func TestFindInlineWriteSequencesFlagsAnOSLinkCall(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/link.go", `package pkg

import "os"

func publishByLink(temporary, name string) error {
	return os.Link(temporary, name)
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Func != "publishByLink" {
		t.Fatalf("violations = %v, want exactly one for publishByLink", violations)
	}
}

func TestFindInlineWriteSequencesFlagsASyscallRenameCall(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/syscall_rename.go", `package pkg

import "syscall"

func publishBySyscallRename(temporary, name string) error {
	return syscall.Rename(temporary, name)
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Func != "publishBySyscallRename" {
		t.Fatalf("violations = %v, want exactly one for publishBySyscallRename", violations)
	}
}

func TestFindInlineWriteSequencesFlagsAnFDRelativeRenameatFamilyCall(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/write.go", `package pkg

import "github.com/sneat-dev/wb/internal/unixcompat"

func writeThenRenameat(dirFD int, name string) error {
	return unix.Renameat(dirFD, ".tmp", dirFD, name)
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Func != "writeThenRenameat" {
		t.Fatalf("violations = %v, want exactly one for writeThenRenameat", violations)
	}
}

func TestFindInlineWriteSequencesFlagsALinkatCall(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/link.go", `package pkg

import "github.com/sneat-dev/wb/internal/unixcompat"

func publishByLinkat(dirFD int, name string) error {
	return unix.Linkat(dirFD, ".tmp", dirFD, name, 0)
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Func != "publishByLinkat" {
		t.Fatalf("violations = %v, want exactly one for publishByLinkat", violations)
	}
}

func TestFindInlineWriteSequencesFlagsARenameNoReplaceCall(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/write.go", `package pkg

func publishViaRenameNoReplace(dirFD int, name string) error {
	return renameNoReplace(dirFD, ".tmp", dirFD, name)
}

func renameNoReplace(fromFD int, from string, toFD int, to string) error {
	return nil
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, v := range violations {
		names[v.Func] = true
	}
	if !names["publishViaRenameNoReplace"] {
		t.Fatalf("violations = %v, want publishViaRenameNoReplace flagged", violations)
	}
}

func TestFindInlineWriteSequencesFlagsAPackageLevelOSLinkAlias(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/write.go", `package pkg

import "os"

var linkPublish = os.Link

func publishViaAlias(temporary, name string) error {
	return linkPublish(temporary, name)
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Func != "publishViaAlias" {
		t.Fatalf("violations = %v, want exactly one for publishViaAlias", violations)
	}
}

func TestFindInlineWriteSequencesFlagsAPackageLevelOSRenameAlias(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/write.go", `package pkg

import "os"

var renamePublish = os.Rename

func publishViaRenameAlias(temporary, name string) error {
	return renamePublish(temporary, name)
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Func != "publishViaRenameAlias" {
		t.Fatalf("violations = %v, want exactly one for publishViaRenameAlias", violations)
	}
}

func TestFindInlineWriteSequencesFlagsAnOSCreateTempCallThatWritesContent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/createtemp.go", `package pkg

import "os"

func writeOnce(dir string, content []byte) error {
	f, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		return err
	}
	return f.Close()
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Func != "writeOnce" {
		t.Fatalf("violations = %v, want exactly one for writeOnce", violations)
	}
}

func TestFindInlineWriteSequencesIgnoresAnOSCreateTempCallThatNeverWritesContent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/createtemp_lock.go", `package pkg

import "os"

func createOnly(dir string) error {
	f, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return err
	}
	return f.Close()
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (a create with no content write is a lock-style open, not a write sequence)", violations)
	}
}

func TestFindInlineWriteSequencesFlagsAnIOUtilTempFileCallThatWritesContent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/tempfile.go", `package pkg

import "io/ioutil"

func writeOnce(dir string, content []byte) error {
	f, err := ioutil.TempFile(dir, "tmp-*")
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		return err
	}
	return f.Close()
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Func != "writeOnce" {
		t.Fatalf("violations = %v, want exactly one for writeOnce", violations)
	}
}

func TestFindInlineWriteSequencesFlagsOSOpenFileWithOCreateThatWritesContent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/openfile.go", `package pkg

import "os"

func writeOnce(name string, content []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		return err
	}
	return f.Close()
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Func != "writeOnce" {
		t.Fatalf("violations = %v, want exactly one for writeOnce (os.O_CREATE must match, not just unix.O_CREAT)", violations)
	}
}

func TestFindInlineWriteSequencesIgnoresAnOSOpenFileCreateCallThatNeverWritesContent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/openfile_lock.go", `package pkg

import "os"

func createOnly(name string) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (a create with no content write is a lock-style open, not a write sequence)", violations)
	}
}

func TestFindInlineWriteSequencesIgnoresOSOpenFileWithoutACreateFlag(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/openfile_readonly.go", `package pkg

import "os"

func readOnly(name string) error {
	f, err := os.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	return f.Close()
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (no create flag present)", violations)
	}
}

func TestFindInlineWriteSequencesFlagsUnixOpenatWithOCreatThatWritesContent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/openat.go", `package pkg

import (
	"os"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

func writeOnce(dirFD int, name string, content []byte) error {
	fd, err := unix.Openat(dirFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	if _, err := f.Write(content); err != nil {
		return err
	}
	return f.Close()
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Func != "writeOnce" {
		t.Fatalf("violations = %v, want exactly one for writeOnce", violations)
	}
}

func TestFindInlineWriteSequencesIgnoresUnixOpenatCreateCallThatNeverWritesContent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/openat_lock.go", `package pkg

import "github.com/sneat-dev/wb/internal/unixcompat"

func createOnly(dirFD int, name string) error {
	_, err := unix.Openat(dirFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600)
	return err
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (a create with no content write is a lock-file open, not a write sequence -- see internal/worktrees/journal.go:lockJournalSequence)", violations)
	}
}

func TestFindInlineWriteSequencesFlagsAnIOCopyIntoACreatedFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/copy.go", `package pkg

import (
	"io"
	"os"
)

func copyOnce(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = output.Close() }()
	_, err = io.Copy(output, input)
	return err
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Func != "copyOnce" {
		t.Fatalf("violations = %v, want exactly one for copyOnce (io.Copy into a just-created file is a content write -- see internal/worktrees/branches_cleanup.go:copyFileSHA256)", violations)
	}
}

func TestFindInlineWriteSequencesFlagsAWriteFileCallPairedWithARename(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/writefile.go", `package pkg

import "os"

func writeThenRename(temporary, name string, content []byte) error {
	if err := os.WriteFile(temporary, content, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, name)
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Func != "writeThenRename" {
		t.Fatalf("violations = %v, want exactly one for writeThenRename", violations)
	}
}

func TestFindInlineWriteSequencesIgnoresAStandaloneWriteFileCall(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/writefile_only.go", `package pkg

import "os"

func overwrite(name string, content []byte) error {
	return os.WriteFile(name, content, 0o600)
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (a bare os.WriteFile with no publish call is an ordinary overwrite)", violations)
	}
}

func TestFindInlineWriteSequencesSortsByFileThenByFunc(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "zpkg/write.go", `package zpkg

import "os"

func writeThenRenameZ(temporary, name string) error {
	return os.Rename(temporary, name)
}
`)
	writeBoundaryFixture(t, root, "apkg/write.go", `package apkg

import "os"

func writeThenRenameB(temporary, name string) error {
	return os.Rename(temporary, name)
}

func writeThenRenameA(temporary, name string) error {
	return os.Rename(temporary, name)
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 3 {
		t.Fatalf("violations = %v, want 3", violations)
	}
	if violations[0].File != "apkg/write.go" || violations[0].Func != "writeThenRenameA" {
		t.Fatalf("violations[0] = %+v, want apkg/write.go writeThenRenameA (file then func order)", violations[0])
	}
	if violations[1].File != "apkg/write.go" || violations[1].Func != "writeThenRenameB" {
		t.Fatalf("violations[1] = %+v, want apkg/write.go writeThenRenameB", violations[1])
	}
	if violations[2].File != "zpkg/write.go" {
		t.Fatalf("violations[2] = %+v, want zpkg/write.go last", violations[2])
	}
}

func TestFindInlineWriteSequencesIgnoresACreateWithoutAPublish(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/readonly.go", `package pkg

import "github.com/sneat-dev/wb/internal/unixcompat"

func readOnly(dirFD int, name string) error {
	_, err := unix.Openat(dirFD, name, unix.O_RDONLY, 0)
	return err
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (no create flag, no publish call)", violations)
	}
}

func TestFindInlineWriteSequencesSkipsTestFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/write_test.go", `package pkg

import "os"

func writeThenRenameInTest(temporary, name string) error {
	return os.Rename(temporary, name)
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (_test.go files are not scanned)", violations)
	}
}

func TestFindInlineWriteSequencesSkipsInternalFilewriteItself(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "internal/filewrite/filewrite.go", `package filewrite

import "os"

func createExclusiveWriteSync(temporary, name string) error {
	return os.Rename(temporary, name)
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (internal/filewrite is the seam itself)", violations)
	}
}

func TestFindInlineWriteSequencesSkipsInternalUnixcompatItself(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "internal/unixcompat/windows.go", `package unix

import "os"

func Renameat(olddirfd int, oldname string, newdirfd int, newname string) error {
	return os.Rename(oldname, newname)
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (internal/unixcompat is the syscall shim internal/filewrite is itself built on)", violations)
	}
}

func TestFindInlineWriteSequencesHonoursThePendingMigrationExemptions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/exempt.go", `package pkg

import "os"

func stillInline(temporary, name string) error {
	return os.Rename(temporary, name)
}
`)
	pending := map[string]string{"pkg/exempt.go:stillInline": "test-only exemption"}
	violations, err := FindInlineWriteSequences(root, pending, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (exempted via pendingMigration)", violations)
	}
}

func TestFindInlineWriteSequencesHonoursTheNotAFileWritePublishExemptions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/exempt.go", `package pkg

import "os"

func stillInline(temporary, name string) error {
	return os.Rename(temporary, name)
}
`)
	notWrite := map[string]string{"pkg/exempt.go:stillInline": "test-only exemption"}
	violations, err := FindInlineWriteSequences(root, nil, notWrite)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (exempted via notAFileWritePublish)", violations)
	}
}

func TestFindInlineWriteSequencesReportsAWalkFailure(t *testing.T) {
	t.Parallel()
	if _, err := FindInlineWriteSequences(filepath.Join(t.TempDir(), "missing"), nil, nil); err == nil {
		t.Fatal("FindInlineWriteSequences accepted a root directory that does not exist")
	}
}

func TestFindInlineWriteSequencesReportsAParseFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/broken.go", "package pkg\n\nfunc Broken( {\n")
	if _, err := FindInlineWriteSequences(root, nil, nil); err == nil {
		t.Fatal("FindInlineWriteSequences accepted a file with a syntax error")
	}
}

func TestRelSlashForFallsBackToPathWhenNotRelatable(t *testing.T) {
	t.Parallel()
	got := relSlashFor("relative/root", filepath.FromSlash("/absolute/root/pkg/foo.go"))
	want := filepath.ToSlash(filepath.FromSlash("/absolute/root/pkg/foo.go"))
	if got != want {
		t.Fatalf("relSlashFor fallback = %q, want %q", got, want)
	}
}

func TestInlineWriteSequenceViolationStringNamesFileLineAndFunc(t *testing.T) {
	t.Parallel()
	v := InlineWriteSequenceViolation{File: "pkg/write.go", Func: "writeThenRename", Line: 12}
	got := v.String()
	want := "pkg/write.go:12: func writeThenRename calls a create/write/publish primitive directly, outside internal/filewrite"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestFileRenameLinkAliasesIgnoresANonSingleValueAssignment(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/write.go", `package pkg

import "os"

func a() (int, error) { return 0, nil }

var x, err = a()

func useX(temporary, name string) error {
	_ = x
	_ = err
	return os.Rename(temporary, name)
}
`)
	violations, err := FindInlineWriteSequences(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Func != "useX" {
		t.Fatalf("violations = %v, want exactly one for useX (the multi-value var decl must not confuse alias detection)", violations)
	}
}
