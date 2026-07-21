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

func changes() *record.WithRecordChanges {
	return &record.WithRecordChanges{}
}

type local struct{ WithRecordChanges int }

func localMember(record local) int {
	return record.WithRecordChanges
}
`
	requireWrite(t, path, source)
	spec := Spec{
		Format: MigrationFormatV1,
		ID:     "dalgo-record-v1",
		Scope:  Scope{Languages: []string{"go"}},
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
			{Kind: "selector.rename", Language: "go", Import: "github.com/dal-go/record", From: "WithRecordChanges", To: "Changes"},
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
		`*dalrecord.Changes`,
		`return record.WithRecordChanges`,
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
		Format: MigrationFormatV1,
		ID:     "cross-language-imports",
		Scope:  Scope{Languages: []string{"python", "typescript"}},
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
	spec := Spec{Format: MigrationFormatV1, ID: "stale-plan", Steps: []Step{{Kind: "text.replace", From: "old", To: "new"}}}
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

func TestReviewRuleExcludesMatchingLines(t *testing.T) {
	source := []byte("package p\nfunc f() {\n\tparams.ApplyChanges(ctx, tx)\n\tdal.ApplyChanges(ctx, tx, params.Changes)\n}\n")
	findings := reviewFindings([]ReviewRule{{
		ID:             "changes-executor",
		Language:       "go",
		Pattern:        "[.]ApplyChanges[(]",
		ExcludePattern: "dal[.]ApplyChanges[(]",
		Message:        "use the DAL executor",
	}}, "go", source, "example.go")
	if len(findings) != 1 || len(findings[0].Lines) != 1 || findings[0].Lines[0] != 3 {
		t.Fatalf("findings = %+v, want only the method call on line 3", findings)
	}
}

func TestValidateKnownFutureAdapterLanguage(t *testing.T) {
	spec := Spec{Format: MigrationFormatV1, ID: "python-import", Steps: []Step{{Kind: "import.replace", Language: "python", From: "old", To: "new"}}}
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
	report := NewReport(Spec{Format: MigrationFormatV1, ID: "example-v1", Title: "Example"}, plan, []string{dir}, "planned")
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

func TestLoadHCLAllowsRepeatedSelectorRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rename.hcl")
	requireWrite(t, path, `format = "https://sneat.dev/workbench/formats/migration/v1"

migration "rename-types" {
  selector_rename "go" {
    import = "example.com/model"
    from = "OldType"
    to = "NewType"
  }

  selector_rename "go" {
    import = "example.com/model"
    from = "OldError"
    to = "NewError"
  }
}
`)

	spec, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Format != MigrationFormatV1 || spec.ID != "rename-types" {
		t.Fatalf("loaded spec = %+v", spec)
	}
	if len(spec.Steps) != 2 {
		t.Fatalf("steps = %+v, want two selector renames", spec.Steps)
	}
	if spec.Steps[0].From != "OldType" || spec.Steps[1].From != "OldError" {
		t.Fatalf("selector rename order = %+v", spec.Steps)
	}
}

func TestLoadDALgoRecordExample(t *testing.T) {
	spec, err := Load(filepath.Join("..", "..", "examples", "migrations", "dalgo-record-v1.hcl"))
	if err != nil {
		t.Fatal(err)
	}
	if spec.ID != "dalgo-record-v1" || spec.Format != MigrationFormatV1 {
		t.Fatalf("loaded spec = %+v", spec)
	}
	if len(spec.Steps) != 5 || spec.Steps[3].Kind != "selector.rename" || spec.Steps[4].Kind != "composite_field.rename" {
		t.Fatalf("steps = %+v, want two imports, one rewrite, one selector rename, and one composite-field rename", spec.Steps)
	}
	if spec.Steps[2].Rewrites["RecordWithID"] != "record.WithID" {
		t.Fatalf("RecordWithID rewrite = %q", spec.Steps[2].Rewrites["RecordWithID"])
	}
	if _, exists := spec.Steps[2].Rewrites["Changes"]; exists {
		t.Fatal("dal.Changes must remain in the DAL package")
	}
	if len(spec.GoModuleRequires) != 1 || spec.GoModuleRequires[0].Path != "github.com/dal-go/record" {
		t.Fatalf("Go module requirements = %+v", spec.GoModuleRequires)
	}
	if len(spec.GoModuleReleases) != 24 ||
		spec.GoModuleReleases[0] != (GoModuleRelease{Path: "github.com/dal-go/record", Version: "v0.1.0"}) ||
		spec.GoModuleReleases[1] != (GoModuleRelease{Path: "github.com/dal-go/dalgo", Version: "v0.63.1"}) ||
		spec.GoModuleReleases[2] != (GoModuleRelease{Path: "github.com/strongo/strongoapp", Version: "v0.31.48"}) ||
		spec.GoModuleReleases[3] != (GoModuleRelease{Path: "github.com/bots-go-framework/bots-fw-telegram-models", Version: "v0.3.71"}) ||
		spec.GoModuleReleases[4] != (GoModuleRelease{Path: "github.com/dal-go/dalgo2firestore", Version: "v0.9.6"}) ||
		spec.GoModuleReleases[5] != (GoModuleRelease{Path: "github.com/sneat-co/commitius/backend", Version: "v0.2.3"}) ||
		spec.GoModuleReleases[6] != (GoModuleRelease{Path: "github.com/bots-go-framework/bots-fw-store-dalgo", Version: "v0.1.1"}) ||
		spec.GoModuleReleases[7] != (GoModuleRelease{Path: "github.com/sneat-co/sneat-go-core", Version: "v0.60.4"}) ||
		spec.GoModuleReleases[8] != (GoModuleRelease{Path: "github.com/bots-go-framework/bots-fw-telegram", Version: "v0.28.1"}) ||
		spec.GoModuleReleases[9] != (GoModuleRelease{Path: "github.com/sneat-co/gameboard/backend", Version: "v0.4.4"}) ||
		spec.GoModuleReleases[10] != (GoModuleRelease{Path: "github.com/sneat-co/ext-contactus/backend", Version: "v0.1.6"}) ||
		spec.GoModuleReleases[11] != (GoModuleRelease{Path: "github.com/sneat-co/sneat-core-modules", Version: "v0.53.5"}) ||
		spec.GoModuleReleases[12] != (GoModuleRelease{Path: "github.com/bots-go-framework/bots-fw-telegram-dalgo", Version: "v0.1.1"}) ||
		spec.GoModuleReleases[13] != (GoModuleRelease{Path: "github.com/sneat-co/assetus/backend", Version: "v0.3.7"}) ||
		spec.GoModuleReleases[14] != (GoModuleRelease{Path: "github.com/sneat-co/calendarius/backend", Version: "v0.4.7"}) ||
		spec.GoModuleReleases[15] != (GoModuleRelease{Path: "github.com/sneat-co/listus/backend", Version: "v0.1.12"}) ||
		spec.GoModuleReleases[16] != (GoModuleRelease{Path: "github.com/sneat-co/remindius/backend", Version: "v0.1.10"}) ||
		spec.GoModuleReleases[17] != (GoModuleRelease{Path: "github.com/sneat-co/sourcer/backend", Version: "v0.17.5"}) ||
		spec.GoModuleReleases[18] != (GoModuleRelease{Path: "github.com/sneat-co/togethered/backend", Version: "v0.6.1"}) ||
		spec.GoModuleReleases[19] != (GoModuleRelease{Path: "github.com/sneat-co/contactus/backend", Version: "v0.1.9"}) ||
		spec.GoModuleReleases[20] != (GoModuleRelease{Path: "github.com/sneat-co/debtus/backend", Version: "v0.2.30"}) ||
		spec.GoModuleReleases[21] != (GoModuleRelease{Path: "github.com/sneat-co/rosycycle/backend", Version: "v0.1.2"}) ||
		spec.GoModuleReleases[22] != (GoModuleRelease{Path: "github.com/sneat-co/trackus/backend", Version: "v0.1.1"}) ||
		spec.GoModuleReleases[23] != (GoModuleRelease{Path: "github.com/sneat-co/paymentus/backend", Version: "v0.5.5"}) {
		t.Fatalf("Go module releases = %+v", spec.GoModuleReleases)
	}
	if len(spec.Review) != 2 || spec.Review[0].ID != "changes-executor" || spec.Review[0].ExcludePattern == "" || spec.Review[1].ID != "legacy-record-api" {
		t.Fatalf("review rules = %+v, want executor exclusion and legacy API audit", spec.Review)
	}
}

func requireWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
