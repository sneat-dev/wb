package quality

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNoInlineWriteSequencesOutsideFilewrite guards spec/plans/
// coverage-to-100 task-9's cutover: no non-test, non-exempt Go function
// may implement its own O_CREAT-open-then-rename-or-link publish
// sequence outside internal/filewrite. A new inline sequence must either
// be routed through internal/filewrite or be added to
// InlineWriteSequenceExemptions with a reason naming the task that will
// migrate it -- never silently reintroduced.
func TestNoInlineWriteSequencesOutsideFilewrite(t *testing.T) {
	t.Parallel()
	root, err := ParallelGuardModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	violations, err := FindInlineWriteSequences(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		for _, v := range violations {
			t.Errorf("%s", v)
		}
		t.Fatalf("%d function(s) implement an inline create/write/publish sequence outside internal/filewrite; route them through it or add a named, reasoned entry to InlineWriteSequenceExemptions", len(violations))
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

func TestFindInlineWriteSequencesFlagsACreateAndRenamePair(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/write.go", `package pkg

import "golang.org/x/sys/unix"

func writeThenRename(dirFD int, name string) error {
	fd, err := unix.Openat(dirFD, ".tmp", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_ = fd
	return unix.Renameat(dirFD, ".tmp", dirFD, name)
}
`)
	violations, err := FindInlineWriteSequences(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Func != "writeThenRename" {
		t.Fatalf("violations = %v, want exactly one for writeThenRename", violations)
	}
}

func TestFindInlineWriteSequencesSortsByFileThenByFunc(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "zpkg/write.go", `package zpkg

import "golang.org/x/sys/unix"

func writeThenRenameZ(dirFD int, name string) error {
	if _, err := unix.Openat(dirFD, ".tmp", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600); err != nil {
		return err
	}
	return unix.Renameat(dirFD, ".tmp", dirFD, name)
}
`)
	writeBoundaryFixture(t, root, "apkg/write.go", `package apkg

import "golang.org/x/sys/unix"

func writeThenRenameB(dirFD int, name string) error {
	if _, err := unix.Openat(dirFD, ".tmp", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600); err != nil {
		return err
	}
	return unix.Renameat(dirFD, ".tmp", dirFD, name)
}

func writeThenRenameA(dirFD int, name string) error {
	if _, err := unix.Openat(dirFD, ".tmp2", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600); err != nil {
		return err
	}
	return unix.Renameat(dirFD, ".tmp2", dirFD, name)
}
`)
	violations, err := FindInlineWriteSequences(root)
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
	writeBoundaryFixture(t, root, "pkg/create_only.go", `package pkg

import "golang.org/x/sys/unix"

func createOnly(dirFD int) error {
	_, err := unix.Openat(dirFD, "name", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600)
	return err
}
`)
	violations, err := FindInlineWriteSequences(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (no publish call present)", violations)
	}
}

func TestFindInlineWriteSequencesIgnoresAPublishWithoutOCreat(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/rename_only.go", `package pkg

import "golang.org/x/sys/unix"

func renameOnly(dirFD int, from, to string) error {
	_, _ = unix.Openat(dirFD, from, unix.O_RDONLY, 0)
	return unix.Renameat(dirFD, from, dirFD, to)
}
`)
	violations, err := FindInlineWriteSequences(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (open has no O_CREAT)", violations)
	}
}

func TestFindInlineWriteSequencesSkipsTestFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/write_test.go", `package pkg

import "golang.org/x/sys/unix"

func writeThenRenameInTest(dirFD int, name string) error {
	_, err := unix.Openat(dirFD, ".tmp", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	return unix.Renameat(dirFD, ".tmp", dirFD, name)
}
`)
	violations, err := FindInlineWriteSequences(root)
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

import "golang.org/x/sys/unix"

func createExclusiveWriteSync(dirFD int, name string) error {
	_, err := unix.Openat(dirFD, ".tmp", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	return unix.Renameat(dirFD, ".tmp", dirFD, name)
}
`)
	violations, err := FindInlineWriteSequences(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (internal/filewrite is the seam itself)", violations)
	}
}

func TestFindInlineWriteSequencesHonoursTheExemptionMap(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/exempt.go", `package pkg

import "golang.org/x/sys/unix"

func stillInline(dirFD int, name string) error {
	_, err := unix.Openat(dirFD, ".tmp", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	return unix.Renameat(dirFD, ".tmp", dirFD, name)
}
`)
	InlineWriteSequenceExemptions["pkg/exempt.go:stillInline"] = "test-only exemption"
	t.Cleanup(func() { delete(InlineWriteSequenceExemptions, "pkg/exempt.go:stillInline") })
	violations, err := FindInlineWriteSequences(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (exempted)", violations)
	}
}

func TestFindInlineWriteSequencesReportsAWalkFailure(t *testing.T) {
	t.Parallel()
	if _, err := FindInlineWriteSequences(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("FindInlineWriteSequences accepted a root directory that does not exist")
	}
}

func TestFindInlineWriteSequencesReportsAParseFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBoundaryFixture(t, root, "pkg/broken.go", "package pkg\n\nfunc Broken( {\n")
	if _, err := FindInlineWriteSequences(root); err == nil {
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
	want := "pkg/write.go:12: func writeThenRename implements its own create/write/publish sequence outside internal/filewrite"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
