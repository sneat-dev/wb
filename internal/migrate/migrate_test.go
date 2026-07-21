package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPlanRewritesGoStructurally(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "example.go")
	const source = `package example

import (
	dal "github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/record"
	"github.com/dal-go/dalgo/update"
)

func create(record string) dal.Record {
	_ = record
	_ = "dal.Record must stay a string"
	return dal.NewRecord(dal.NewKeyWithID("Things", "one"))
}
`
	requireWrite(t, path, source)
	spec := Spec{
		ID:      "dalgo-record-v1",
		Version: 1,
		Scope:   Scope{Languages: []string{"go"}},
		Steps: []Step{
			{Kind: "import.replace", Language: "go", From: "github.com/dal-go/dalgo/record", To: "github.com/dal-go/record"},
			{Kind: "import.replace", Language: "go", From: "github.com/dal-go/dalgo/update", To: "github.com/dal-go/record/update"},
			{
				Kind:        "selector.rewrite",
				Language:    "go",
				Import:      "github.com/dal-go/dalgo/dal",
				AddImport:   "github.com/dal-go/record",
				AddImportAs: "record",
				Rewrites: map[string]string{
					"Record":       "record.Record",
					"NewRecord":    "record.NewRecord",
					"NewKeyWithID": "record.NewKeyWithID",
				},
			},
		},
	}

	plan, err := BuildPlan(spec, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 {
		t.Fatalf("changes = %d, want 1", len(plan.Changes))
	}
	updated := string(plan.Changes[0].Updated)
	for _, want := range []string{
		`"github.com/dal-go/record"`,
		`"github.com/dal-go/record/update"`,
		`dalrecord.Record`,
		`dalrecord.NewRecord(dalrecord.NewKeyWithID`,
		`"dal.Record must stay a string"`,
	} {
		if !strings.Contains(updated, want) {
			t.Errorf("updated source missing %q:\n%s", want, updated)
		}
	}
	if strings.Contains(updated, `github.com/dal-go/dalgo/record`) || strings.Contains(updated, "dal.Record {") {
		t.Errorf("old API remains:\n%s", updated)
	}

	if err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != updated {
		t.Error("Apply did not write planned source")
	}
	second, err := BuildPlan(spec, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Changes) != 0 {
		t.Errorf("migration is not idempotent: %+v", second.Changes)
	}
}

func TestBuildPlanTextReplaceSupportsPythonAndTypeScript(t *testing.T) {
	dir := t.TempDir()
	python := filepath.Join(dir, "client.py")
	typescript := filepath.Join(dir, "client.ts")
	requireWrite(t, python, "from old_api import Record\n")
	requireWrite(t, typescript, "import { Record } from 'old-api';\n")

	spec := Spec{
		ID:      "cross-language-imports",
		Version: 1,
		Scope:   Scope{Languages: []string{"python", "typescript"}},
		Steps: []Step{
			{Kind: "text.replace", Language: "python", From: "old_api", To: "new_api"},
			{Kind: "text.replace", Language: "typescript", From: "old-api", To: "new-api"},
		},
	}
	plan, err := BuildPlan(spec, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 2 {
		t.Fatalf("changes = %d, want 2", len(plan.Changes))
	}
	for _, change := range plan.Changes {
		if strings.Contains(string(change.Updated), "old_") || strings.Contains(string(change.Updated), "old-") {
			t.Errorf("unreplaced text in %s: %s", change.Path, change.Updated)
		}
	}
}

func TestApplyRefusesStalePlan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "example.py")
	requireWrite(t, path, "old\n")
	spec := Spec{ID: "stale-plan", Version: 1, Steps: []Step{{Kind: "text.replace", From: "old", To: "new"}}}
	plan, err := BuildPlan(spec, dir)
	if err != nil {
		t.Fatal(err)
	}
	requireWrite(t, path, "changed externally\n")
	err = Apply(plan)
	if err == nil || !strings.Contains(err.Error(), "changed after planning") {
		t.Fatalf("Apply() error = %v, want stale-plan error", err)
	}
}

func TestValidateKnownFutureAdapterLanguage(t *testing.T) {
	spec := Spec{ID: "python-import", Version: 1, Steps: []Step{{Kind: "import.replace", Language: "python", From: "old", To: "new"}}}
	if err := spec.Validate(); err != nil {
		t.Fatalf("known future adapter language should validate: %v", err)
	}
}

func TestReportIndexesFilesForHumansAndTools(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "z", "one.go")
	second := filepath.Join(dir, "a", "two.go")
	plan := Plan{
		MigrationID: "example-v1",
		Changes: []FileChange{
			{Path: first, Language: "go", OriginalSHA256: "111", Steps: []string{"selector.rewrite"}},
			{Path: second, Language: "go", OriginalSHA256: "222", Steps: []string{"import.replace"}},
		},
		Findings: []Finding{{Path: first, Language: "go", RuleID: "semantic-step", Message: "Review this call", Lines: []int{12}}},
	}
	report := NewReport(Spec{ID: "example-v1", Title: "Example", Version: 1}, plan, []string{dir}, "planned")
	if len(report.Files) != 2 || report.Files[0].Path != "a/two.go" {
		t.Fatalf("files = %+v, want sorted relative paths", report.Files)
	}
	markdown := report.Markdown()
	for _, want := range []string{
		"[a/two.go](file://", "`git -C '" + dir + "' diff -- 'a/two.go'`", "selector.rewrite", "## Required review", "Review this call",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("markdown missing %q:\n%s", want, markdown)
		}
	}
	yaml, err := report.YAML()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"schema_version: 1", "status: planned", "path: a/two.go", "operations:", "review_items:", "semantic-step"} {
		if !strings.Contains(string(yaml), want) {
			t.Errorf("YAML missing %q:\n%s", want, yaml)
		}
	}
	reportDir := filepath.Join(dir, "report")
	if err := WriteReports(reportDir, report); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"migration.md", "migration.yaml"} {
		if _, err := os.Stat(filepath.Join(reportDir, name)); err != nil {
			t.Errorf("missing written report %s: %v", name, err)
		}
	}
}

func requireWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
