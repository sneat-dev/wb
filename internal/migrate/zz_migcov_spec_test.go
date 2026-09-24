package migrate

import (
	"strings"
	"testing"
)

func TestMigCovSpecValidateRejectsEveryMalformedShape(t *testing.T) {
	t.Parallel()
	validStep := Step{Kind: "text.replace", From: "old", To: "new"}
	validRule := ReviewRule{ID: "rule", Pattern: "old", Message: "review"}
	base := func() Spec {
		return Spec{Format: MigrationFormatV1, ID: "example", Steps: []Step{validStep}}
	}
	tests := []struct {
		name   string
		mutate func(*Spec)
		want   string
	}{
		{name: "missing id", mutate: func(s *Spec) { s.ID = "  " }, want: "missing id"},
		{name: "unsupported format", mutate: func(s *Spec) { s.Format = "v0" }, want: "unsupported format"},
		{name: "unknown scope language", mutate: func(s *Spec) { s.Scope.Languages = []string{"rust"} }, want: "unknown scope language"},
		{name: "no steps", mutate: func(s *Spec) { s.Steps = nil }, want: "requires at least one step"},
		{name: "invalid step", mutate: func(s *Spec) { s.Steps = []Step{{Kind: "text.replace"}} }, want: "step 1: text.replace requires from"},
		{name: "invalid review rule", mutate: func(s *Spec) { s.Review = []ReviewRule{{ID: "rule", Pattern: "x"}} }, want: "review rule 1"},
		{
			name: "requirement without version",
			mutate: func(s *Spec) {
				s.GoModuleRequires = []GoModuleRequire{{Path: "example.com/mod"}}
			},
			want: "go module requirement 1 requires path and version",
		},
		{
			name: "release without version",
			mutate: func(s *Spec) {
				s.GoModuleReleases = []GoModuleRelease{{Path: "example.com/mod"}}
			},
			want: "go module release 1 requires path and version",
		},
		{
			name: "duplicate release",
			mutate: func(s *Spec) {
				s.GoModuleReleases = []GoModuleRelease{
					{Path: "example.com/mod", Version: "v1.0.0"},
					{Path: "example.com/mod", Version: "v1.0.1"},
				}
			},
			want: "duplicate go module release",
		},
		{name: "accepts a full definition", mutate: func(s *Spec) { s.Review = []ReviewRule{validRule} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			spec := base()
			test.mutate(&spec)
			err := spec.Validate()
			if test.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestMigCovStepValidateRejectsEachKindGap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		step Step
		want string
	}{
		{name: "text missing from", step: Step{Kind: "text.replace", To: "new"}, want: "text.replace requires from"},
		{name: "text missing to", step: Step{Kind: "text.replace", From: "old"}, want: "text.replace requires to"},
		{name: "text unknown language", step: Step{Kind: "text.replace", From: "old", To: "new", Language: "rust"}, want: "unknown language"},
		{name: "import unknown language", step: Step{Kind: "import.replace", From: "old", To: "new"}, want: "import.replace requires a known language"},
		{name: "import missing from", step: Step{Kind: "import.replace", Language: "go", To: "new"}, want: "import.replace requires from and to"},
		{name: "import missing to", step: Step{Kind: "import.replace", Language: "go", From: "old"}, want: "import.replace requires from and to"},
		{name: "rewrite unknown language", step: Step{Kind: "selector.rewrite", Import: "a", AddImport: "b", Rewrites: map[string]string{"x": "y"}}, want: "selector.rewrite requires a known language"},
		{name: "rewrite missing import", step: Step{Kind: "selector.rewrite", Language: "go", AddImport: "b", Rewrites: map[string]string{"x": "y"}}, want: "selector.rewrite requires import, add_import, and rewrites"},
		{name: "rewrite missing add import", step: Step{Kind: "selector.rewrite", Language: "go", Import: "a", Rewrites: map[string]string{"x": "y"}}, want: "selector.rewrite requires import, add_import, and rewrites"},
		{name: "rewrite missing rewrites", step: Step{Kind: "selector.rewrite", Language: "go", Import: "a", AddImport: "b"}, want: "selector.rewrite requires import, add_import, and rewrites"},
		{name: "rename unknown language", step: Step{Kind: "selector.rename", Import: "a", From: "b", To: "c"}, want: "selector.rename requires a known language"},
		{name: "rename missing import", step: Step{Kind: "selector.rename", Language: "go", From: "b", To: "c"}, want: "selector.rename requires import, from, and to"},
		{name: "rename missing from", step: Step{Kind: "selector.rename", Language: "go", Import: "a", To: "c"}, want: "selector.rename requires import, from, and to"},
		{name: "rename missing to", step: Step{Kind: "selector.rename", Language: "go", Import: "a", From: "b"}, want: "selector.rename requires import, from, and to"},
		{name: "composite unknown language", step: Step{Kind: "composite_field.rename", From: "b", To: "c"}, want: "composite_field.rename requires a known language"},
		{name: "composite missing from", step: Step{Kind: "composite_field.rename", Language: "go", To: "c"}, want: "composite_field.rename requires from and to"},
		{name: "composite missing to", step: Step{Kind: "composite_field.rename", Language: "go", From: "b"}, want: "composite_field.rename requires from and to"},
		{name: "unknown kind", step: Step{Kind: "regex.replace"}, want: `unknown kind "regex.replace"`},
		{name: "valid text replace", step: Step{Kind: "text.replace", From: "old", To: "new"}},
		{name: "valid selector rewrite", step: Step{Kind: "selector.rewrite", Language: "go", Import: "a", AddImport: "b", Rewrites: map[string]string{"x": "b.Y"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.step.Validate()
			if test.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestMigCovReviewRuleValidateRejectsEachGap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rule ReviewRule
		want string
	}{
		{name: "missing id", rule: ReviewRule{Pattern: "x", Message: "m"}, want: "missing id"},
		{name: "unknown language", rule: ReviewRule{ID: "r", Language: "rust", Pattern: "x", Message: "m"}, want: "unknown language"},
		{name: "missing pattern", rule: ReviewRule{ID: "r", Message: "m"}, want: "missing pattern"},
		{name: "invalid pattern", rule: ReviewRule{ID: "r", Pattern: "(", Message: "m"}, want: "invalid pattern"},
		{name: "invalid exclude pattern", rule: ReviewRule{ID: "r", Pattern: "x", ExcludePattern: "(", Message: "m"}, want: "invalid exclude_pattern"},
		{name: "missing message", rule: ReviewRule{ID: "r", Pattern: "x"}, want: "missing message"},
		{name: "valid", rule: ReviewRule{ID: "r", Language: "go", Pattern: "x", ExcludePattern: "y", Message: "m"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.rule.Validate()
			if test.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestMigCovLoadRejectsMalformedDocuments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		contents string
		want     string
	}{
		{
			name:     "unparseable HCL",
			contents: "format = \"x\"\nmigration \"a\" {",
			want:     "parse migration",
		},
		{
			name:     "no migration block",
			contents: "format = \"" + MigrationFormatV1 + "\"\n",
			want:     "requires exactly one migration block",
		},
		{
			name: "two migration blocks",
			contents: "format = \"" + MigrationFormatV1 + "\"\n" +
				"migration \"a\" {\n  text_replace \"go\" {\n    from = \"x\"\n    to   = \"y\"\n  }\n}\n" +
				"migration \"b\" {\n  text_replace \"go\" {\n    from = \"x\"\n    to   = \"y\"\n  }\n}\n",
			want: "requires exactly one migration block, got 2",
		},
		{
			name: "two scope blocks",
			contents: "format = \"" + MigrationFormatV1 + "\"\n" +
				"migration \"a\" {\n  scope {\n    languages = [\"go\"]\n  }\n  scope {\n    languages = [\"go\"]\n  }\n" +
				"  text_replace \"go\" {\n    from = \"x\"\n    to   = \"y\"\n  }\n}\n",
			want: "at most one is allowed",
		},
		{
			name: "step fails validation",
			contents: "format = \"" + MigrationFormatV1 + "\"\n" +
				"migration \"a\" {\n  text_replace \"go\" {\n    from = \"x\"\n    to   = \"\"\n  }\n}\n",
			want: "text.replace requires to",
		},
		{
			name: "unsupported format",
			contents: "format = \"v0\"\n" +
				"migration \"a\" {\n  text_replace \"go\" {\n    from = \"x\"\n    to   = \"y\"\n  }\n}\n",
			want: "unsupported format",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := migCovWriteFile(t, "spec.hcl", test.contents)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() = %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestMigCovLoadDecodesEveryBlockKind(t *testing.T) {
	t.Parallel()
	contents := `format = "` + MigrationFormatV1 + `"

migration "everything" {
  title = "Everything migration."

  scope {
    languages = ["go"]
    include   = ["pkg/**"]
    exclude   = ["pkg/generated/**"]
  }

  text_replace "python" {
    from = "old_api"
    to   = "new_api"
  }

  import_replace "go" {
    from = "example.com/old"
    to   = "example.com/new"
  }

  selector_rewrite "go" {
    import        = "example.com/dal"
    add_import    = "example.com/record"
    add_import_as = "record"
    rewrites = {
      "Key" = "record.Key"
    }
  }

  selector_rename "go" {
    import = "example.com/record"
    from   = "OldType"
    to     = "NewType"
  }

  composite_field_rename "go" {
    from = "RecordWithID"
    to   = "WithID"
  }

  go_module_require "example.com/record" {
    version = "v0.1.0"
  }

  go_module_release "example.com/record" {
    version = "v0.2.0"
  }

  review "changes-executor" {
    language        = "go"
    pattern         = "[.]ApplyChanges[(]"
    exclude_pattern = "dal[.]ApplyChanges[(]"
    message         = "use the DAL executor"
  }
}
`
	path := migCovWriteFile(t, "everything.hcl", contents)
	spec, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if spec.Title != "Everything migration." {
		t.Errorf("Title = %q", spec.Title)
	}
	if got := strings.Join(spec.Scope.Languages, ","); got != "go" {
		t.Errorf("scope languages = %q", got)
	}
	if len(spec.Scope.Include) != 1 || spec.Scope.Include[0] != "pkg/**" {
		t.Errorf("scope include = %v", spec.Scope.Include)
	}
	if len(spec.Scope.Exclude) != 1 || spec.Scope.Exclude[0] != "pkg/generated/**" {
		t.Errorf("scope exclude = %v", spec.Scope.Exclude)
	}
	wantKinds := []string{
		"text.replace", "import.replace", "selector.rewrite",
		"selector.rename", "composite_field.rename",
	}
	if len(spec.Steps) != len(wantKinds) {
		t.Fatalf("steps = %+v", spec.Steps)
	}
	for i, want := range wantKinds {
		if spec.Steps[i].Kind != want {
			t.Errorf("step %d kind = %q, want %q", i, spec.Steps[i].Kind, want)
		}
	}
	if spec.Steps[0].Language != "python" || spec.Steps[0].From != "old_api" || spec.Steps[0].To != "new_api" {
		t.Errorf("text.replace step = %+v", spec.Steps[0])
	}
	if spec.Steps[2].Rewrites["Key"] != "record.Key" || spec.Steps[2].AddImportAs != "record" {
		t.Errorf("selector.rewrite step = %+v", spec.Steps[2])
	}
	if len(spec.GoModuleRequires) != 1 || spec.GoModuleRequires[0].Version != "v0.1.0" {
		t.Errorf("go module requires = %+v", spec.GoModuleRequires)
	}
	if len(spec.GoModuleReleases) != 1 || spec.GoModuleReleases[0].Version != "v0.2.0" {
		t.Errorf("go module releases = %+v", spec.GoModuleReleases)
	}
	if len(spec.Review) != 1 || spec.Review[0].ExcludePattern != "dal[.]ApplyChanges[(]" {
		t.Errorf("review rules = %+v", spec.Review)
	}
}
