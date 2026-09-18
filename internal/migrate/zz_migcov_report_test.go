package migrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func migCovWriteFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMigCovNewReportResolvesFilesAgainstEveryRoot(t *testing.T) {
	shortRoot := filepath.Join(t.TempDir(), "a")
	longRoot := filepath.Join(t.TempDir(), "b", "deeper")
	outsideRoot := filepath.Join(t.TempDir(), "other")
	plan := Plan{
		MigrationID: "roots",
		Changes: []FileChange{
			{Path: filepath.Join(longRoot, "pkg", "b.go"), Language: "go", OriginalSHA256: "bbb", Steps: []string{"text.replace"}},
			{Path: filepath.Join(shortRoot, "a.go"), Language: "go", OriginalSHA256: "aaa", Steps: []string{"import.replace"}},
			{Path: filepath.Join(outsideRoot, "loose.go"), Language: "go", OriginalSHA256: "ccc"},
		},
	}
	report := NewReport(Spec{Format: MigrationFormatV1, ID: "roots"}, plan, []string{shortRoot, longRoot}, "planned")
	if len(report.Files) != 3 {
		t.Fatalf("files = %+v", report.Files)
	}
	byPath := map[string]ReportFile{}
	for _, file := range report.Files {
		byPath[file.Path] = file
	}
	if file, ok := byPath["pkg/b.go"]; !ok || file.Root != longRoot {
		t.Errorf("long root file = %+v", file)
	}
	if file, ok := byPath["a.go"]; !ok || file.Root != shortRoot {
		t.Errorf("short root file = %+v", file)
	}
	// A path outside every root has no relative form: it keeps its directory
	// and base name so the report never fabricates containment.
	if file, ok := byPath["loose.go"]; !ok || file.Root != outsideRoot {
		t.Errorf("uncontained file = %+v", file)
	}
}

func TestMigCovReportMarkdownHandlesEmptyAndCompleteReports(t *testing.T) {
	empty := Report{SchemaVersion: 1, Migration: ReportMigration{ID: "empty", Format: MigrationFormatV1}, Status: "planned"}
	markdown := empty.Markdown()
	if !strings.Contains(markdown, "No files require a change.") {
		t.Errorf("empty markdown = %q", markdown)
	}

	dir := t.TempDir()
	full := Report{
		SchemaVersion: 1,
		Migration:     ReportMigration{ID: "full", Title: "Full migration.", Format: MigrationFormatV1},
		Status:        "applied",
		Files: []ReportFile{{
			Root: dir, Path: "pkg/a.go", AbsolutePath: filepath.Join(dir, "pkg", "a.go"),
			Language: "go", Operations: []string{"selector.rewrite"}, OriginalSHA256: "abc",
			GitDiffCommand: "git -C '" + dir + "' diff -- 'pkg/a.go'",
		}},
		ReviewItems: []ReportFinding{{
			Root: dir, Path: "pkg/a.go", AbsolutePath: filepath.Join(dir, "pkg", "a.go"),
			Language: "go", RuleID: "rule", Message: "review", Lines: []int{4, 9},
		}},
	}
	markdown = full.Markdown()
	for _, want := range []string{"Full migration.", "## Change index", "## Inspect detailed diffs", "## Required review", "lines 4, 9"} {
		if !strings.Contains(markdown, want) {
			t.Errorf("markdown missing %q:\n%s", want, markdown)
		}
	}
}

func TestMigCovReportJSONMatchesYAMLFieldNames(t *testing.T) {
	report := Report{
		SchemaVersion: 1,
		Migration:     ReportMigration{ID: "json", Title: "JSON", Format: MigrationFormatV1},
		Status:        "planned",
		Files:         []ReportFile{{Root: "/r", Path: "a.go", AbsolutePath: "/r/a.go", Language: "go", Operations: []string{"text.replace"}, OriginalSHA256: "x", GitDiffCommand: "git diff"}},
	}
	raw, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("JSON() produced invalid JSON: %v\n%s", err, raw)
	}
	for _, key := range []string{"schema_version", "migration", "status", "files"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("JSON missing %q: %s", key, raw)
		}
	}
	if !strings.Contains(string(raw), `"absolute_path"`) || !strings.Contains(string(raw), `"git_diff_command"`) {
		t.Errorf("JSON does not use YAML field names: %s", raw)
	}
}

func TestMigCovWriteReportsPropagatesFilesystemErrors(t *testing.T) {
	dir := t.TempDir()
	report := Report{SchemaVersion: 1, Migration: ReportMigration{ID: "write", Format: MigrationFormatV1}, Status: "applied"}
	if err := WriteReports(filepath.Join(dir, "nested"), report); err != nil {
		t.Fatalf("WriteReports() error = %v", err)
	}
	for _, name := range []string{"migration.md", "migration.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, "nested", name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}

	// Refuse to write when the report directory is really a file.
	filePath := filepath.Join(dir, "file")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteReports(filePath, report); err == nil {
		t.Fatal("WriteReports() to a file path succeeded")
	}

	// Refuse to write Markdown when a directory already owns the name.
	blocked := filepath.Join(dir, "blocked")
	if err := os.MkdirAll(filepath.Join(blocked, "migration.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteReports(blocked, report); err == nil {
		t.Fatal("WriteReports() with migration.md as a directory succeeded")
	}
}
