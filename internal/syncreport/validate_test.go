package syncreport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// TestInstallWritesSchemaAndRecords pins every path Install owns: the
// settings, root-collections, and collection definition files plus one
// deterministic record file per repository, all byte-for-byte.
func TestInstallWritesSchemaAndRecords(t *testing.T) {
	t.Parallel()
	record := Record{Repository: "sneat-co/schoolus", Raw: []byte(validRecord)}
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

// TestInstallReportsDirectoryCreationFailure proves a root that cannot host
// the collection tree is a hard error rather than a partial install.
func TestInstallReportsDirectoryCreationFailure(t *testing.T) {
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

// TestInstallReportsFileWriteFailure proves a path already occupied by a
// directory is reported instead of silently skipped.
func TestInstallReportsFileWriteFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, SettingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Install(root, Report{ID: "sync-1"}); err == nil {
		t.Fatalf("Install with %s already a directory = nil, want a write failure", SettingsPath)
	}
}

// TestValidatePathAcceptsAValidDatabase covers validatePath's success path
// against a scripted runner.Runner: it must invoke `ingitdb validate --path
// <directory>` and return nil once the fake reports success.
func TestValidatePathAcceptsAValidDatabase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"ingitdb", "validate", "--path", dir}, runner.Result{}, nil)
	if err := validatePath(context.Background(), dir, fake); err != nil {
		t.Fatalf("validatePath = %v, want nil for a database the CLI accepts", err)
	}
}

// TestValidatePathReportsCLIFailure proves the CLI's own diagnostics reach
// the caller, wrapped and attributed to ingitdb validate.
func TestValidatePathReportsCLIFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"ingitdb", "validate", "--path", dir},
		runner.Result{ExitCode: 3, Stderr: "definition invalid"}, errors.New("exit status 3"))
	err := validatePath(context.Background(), dir, fake)
	if err == nil || !strings.Contains(err.Error(), "ingitdb validate") || !strings.Contains(err.Error(), "definition invalid") {
		t.Fatalf("validatePath = %v, want a wrapped failure carrying the CLI output", err)
	}
}

// TestValidatePathDefaultsToProductionRunner proves ValidatePath's exported
// wrapper resolves a nil runner to the production runner.Runner rather than
// silently doing nothing. Under `go test`, runner.Real refuses to start a
// real process (task-24's guard), so this observes ErrRealProcessBlocked
// instead of shelling out -- the point is to prove the default wiring, not
// to run ingitdb.
func TestValidatePathDefaultsToProductionRunner(t *testing.T) {
	t.Parallel()
	err := ValidatePath(context.Background(), t.TempDir())
	if !errors.Is(err, runner.ErrRealProcessBlocked) {
		t.Fatalf("ValidatePath with no injected runner = %v, want it to reach the production runner.Runner", err)
	}
}

// TestValidateInGitDBValidatesACopiedDatabase drives the full flow: the
// records are installed into a private database, the CLI is asked to
// validate that exact path, and the database is removed afterwards.
func TestValidateInGitDBValidatesACopiedDatabase(t *testing.T) {
	t.Parallel()
	var validated string
	fake := runnertest.New(t)
	fake.Expect(func(c runnertest.Call) bool {
		if c.Op != "Run" || len(c.Args) != 3 || c.Name != "ingitdb" {
			return false
		}
		if c.Args[0] != "validate" || c.Args[1] != "--path" {
			return false
		}
		validated = c.Args[2]
		return strings.HasPrefix(filepath.Base(validated), "wb-sync-report-validate-")
	}, runner.Result{}, nil)

	report := Report{ID: "sync-1", Records: []Record{{Repository: "sneat-co/schoolus", Raw: []byte(validRecord)}}}
	if err := validateInGitDB(context.Background(), report, fake); err != nil {
		t.Fatalf("validateInGitDB = %v, want nil", err)
	}
	if validated == "" {
		t.Fatal("the fake ingitdb was not invoked")
	}
	if _, err := os.Stat(validated); !os.IsNotExist(err) {
		t.Fatalf("temporary database %s still exists after validation: err=%v", validated, err)
	}
}

// TestValidateInGitDBReportsCLIFailure covers the failing-CLI branch of the
// full flow.
func TestValidateInGitDBReportsCLIFailure(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.Expect(func(c runnertest.Call) bool {
		return c.Op == "Run" && c.Name == "ingitdb"
	}, runner.Result{ExitCode: 1, Stderr: "bad column type"}, errors.New("exit status 1"))

	report := Report{ID: "sync-1", Records: []Record{{Repository: "sneat-co/schoolus", Raw: []byte(validRecord)}}}
	err := validateInGitDB(context.Background(), report, fake)
	if err == nil || !strings.Contains(err.Error(), "ingitdb validate") || !strings.Contains(err.Error(), "bad column type") {
		t.Fatalf("validateInGitDB = %v, want the CLI failure to propagate", err)
	}
}

// TestValidateInGitDBReportsInstallFailure proves a record that cannot be
// materialised is reported before the CLI is ever consulted; a report id
// containing NUL makes the record path unwritable on every platform. The
// fake carries no scripted response, so any (unexpected) invocation fails
// the test through t.Fatal.
func TestValidateInGitDBReportsInstallFailure(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	report := Report{ID: "bad\x00id", Records: []Record{{Repository: "sneat-co/schoolus", Raw: []byte(validRecord)}}}
	err := validateInGitDB(context.Background(), report, fake)
	if err == nil {
		t.Fatal("validateInGitDB with an unwritable record path = nil, want an install failure")
	}
	if strings.Contains(err.Error(), "ingitdb validate") {
		t.Fatalf("validateInGitDB = %v, want the failure to come from Install, not the CLI", err)
	}
}

// TestValidateInGitDBSurfacesATempDirectoryFailure proves an unusable TMPDIR
// is reported directly, without ever shelling out.
func TestValidateInGitDBSurfacesATempDirectoryFailure(t *testing.T) {
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
