package syncreport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// tailCovParseValidRecord parses the shared fixture and attaches the raw bytes
// Install is expected to copy, mirroring what LoadDirectory does for a real
// record.
func tailCovParseValidRecord(t *testing.T) Record {
	t.Helper()
	record, err := Parse([]byte(validRecord))
	if err != nil {
		t.Fatal(err)
	}
	record.Raw = []byte(validRecord)
	return record
}

// tailCovFakeIngitDB puts an executable named ingitdb at the front of PATH so
// ValidatePath/ValidateInGitDB exercise their real exec path without the
// network or the actual tool.
func tailCovFakeIngitDB(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ingitdb")
	if err := testenv.WriteExecutableFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const tailCovIngitDBRecordingScript = `#!/bin/sh
printf '%s\n' "$@" > "$TAILCOV_INGITDB_ARGS"
exit 0
`

// TestTailCovInstallWritesSchemaAndRecords pins every path Install owns: the
// settings, root-collections, and collection definition files plus one
// deterministic record file per repository, all byte-for-byte.
func TestTailCovInstallWritesSchemaAndRecords(t *testing.T) {
	t.Parallel()
	record := tailCovParseValidRecord(t)
	root := t.TempDir()
	if err := Install(root, Report{ID: "sync-1", Records: []Record{record}}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		SettingsPath:        SettingsYAML,
		RootCollectionsPath: RootCollectionsYAML,
		DefinitionPath:      DefinitionYAML,
		filepath.ToSlash(filepath.Join(CollectionRecords, RecordName("sync-1", "sneat-co/schoolus"))): validRecord,
	}
	for relative, contents := range want {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read installed %s: %v", relative, err)
		}
		if string(raw) != contents {
			t.Fatalf("installed %s = %q, want %q", relative, raw, contents)
		}
	}
}

// TestTailCovInstallReportsDirectoryCreationFailure proves a root that cannot
// host the collection tree is a hard error rather than a partial install.
func TestTailCovInstallReportsDirectoryCreationFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Install(blocker, Report{ID: "sync-1"}); err == nil {
		t.Fatal("Install under a regular file = nil, want a directory-creation failure")
	}
}

// TestTailCovInstallReportsFileWriteFailure proves a path already occupied by a
// directory is reported instead of silently skipped.
func TestTailCovInstallReportsFileWriteFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, SettingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Install(root, Report{ID: "sync-1"}); err == nil {
		t.Fatalf("Install with %s already a directory = nil, want a write failure", SettingsPath)
	}
}

// TestTailCovValidatePathAcceptsAValidDatabase covers the success path of the
// CLI wrapper.
func TestTailCovValidatePathAcceptsAValidDatabase(t *testing.T) {
	tailCovFakeIngitDB(t, "#!/bin/sh\nexit 0\n")
	if err := ValidatePath(context.Background(), t.TempDir()); err != nil {
		t.Fatalf("ValidatePath = %v, want nil for a database the CLI accepts", err)
	}
}

// TestTailCovValidatePathReportsCLIFailure proves the CLI's own diagnostics
// reach the caller, wrapped and attributed to ingitdb validate.
func TestTailCovValidatePathReportsCLIFailure(t *testing.T) {
	tailCovFakeIngitDB(t, "#!/bin/sh\necho 'definition invalid' >&2\nexit 3\n")
	err := ValidatePath(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "ingitdb validate") || !strings.Contains(err.Error(), "definition invalid") {
		t.Fatalf("ValidatePath = %v, want a wrapped failure carrying the CLI output", err)
	}
}

// TestTailCovValidateInGitDBValidatesACopiedDatabase drives the full flow: the
// records are installed into a private database, the CLI is asked to validate
// that exact path, and the database is removed afterwards.
func TestTailCovValidateInGitDBValidatesACopiedDatabase(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "ingitdb-args")
	t.Setenv("TAILCOV_INGITDB_ARGS", argsFile)
	tailCovFakeIngitDB(t, tailCovIngitDBRecordingScript)

	report := Report{ID: "sync-1", Records: []Record{tailCovParseValidRecord(t)}}
	if err := ValidateInGitDB(context.Background(), report); err != nil {
		t.Fatalf("ValidateInGitDB = %v, want nil", err)
	}

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("the fake ingitdb was not invoked: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 || lines[0] != "validate" || lines[1] != "--path" {
		t.Fatalf("ingitdb args = %q, want validate --path <dir>", lines)
	}
	if base := filepath.Base(lines[2]); !strings.HasPrefix(base, "wb-sync-report-validate-") {
		t.Fatalf("validated path base = %q, want a wb-sync-report-validate-* database", base)
	}
	if _, err := os.Stat(lines[2]); !os.IsNotExist(err) {
		t.Fatalf("temporary database %s still exists after validation: err=%v", lines[2], err)
	}
}

// TestTailCovValidateInGitDBReportsCLIFailure covers the failing-CLI branch of
// the full flow.
func TestTailCovValidateInGitDBReportsCLIFailure(t *testing.T) {
	tailCovFakeIngitDB(t, "#!/bin/sh\necho 'bad column type' >&2\nexit 1\n")
	report := Report{ID: "sync-1", Records: []Record{tailCovParseValidRecord(t)}}
	err := ValidateInGitDB(context.Background(), report)
	if err == nil || !strings.Contains(err.Error(), "ingitdb validate") || !strings.Contains(err.Error(), "bad column type") {
		t.Fatalf("ValidateInGitDB = %v, want the CLI failure to propagate", err)
	}
}

// TestTailCovValidateInGitDBReportsInstallFailure proves a record that cannot
// be materialised is reported before the CLI is ever consulted; a report id
// containing NUL makes the record path unwritable on every platform.
func TestTailCovValidateInGitDBReportsInstallFailure(t *testing.T) {
	t.Parallel()
	report := Report{ID: "bad\x00id", Records: []Record{tailCovParseValidRecord(t)}}
	err := ValidateInGitDB(context.Background(), report)
	if err == nil {
		t.Fatal("ValidateInGitDB with an unwritable record path = nil, want an install failure")
	}
	if strings.Contains(err.Error(), "ingitdb validate") {
		t.Fatalf("ValidateInGitDB = %v, want the failure to come from Install, not the CLI", err)
	}
}

// TestTailCovValidateInGitDBSurfacesATempDirectoryFailure proves an unusable
// TMPDIR is reported directly, without ever shelling out.
func TestTailCovValidateInGitDBSurfacesATempDirectoryFailure(t *testing.T) {
	tailCovRequireUnixFilesystem(t)
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("TMPDIR", missing)

	err := ValidateInGitDB(context.Background(), Report{ID: "sync-1"})
	if err == nil {
		t.Fatal("ValidateInGitDB with an unusable TMPDIR = nil, want a temp-directory failure")
	}
	if strings.Contains(err.Error(), "ingitdb validate") {
		t.Fatalf("ValidateInGitDB = %v, want the failure to come from MkdirTemp, not the CLI", err)
	}
}
