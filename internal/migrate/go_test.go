package migrate

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestGoSelectorRewriteRemovesOnlyUnusedSourceImport(t *testing.T) {
	step := Step{
		Kind:      "selector.rewrite",
		Import:    "example.com/old/dal",
		AddImport: "example.com/record",
		Rewrites:  map[string]string{"Key": "record.Key"},
	}
	for _, test := range []struct {
		name       string
		source     string
		wantOld    bool
		wantRecord bool
	}{
		{
			name:       "default import becomes unused",
			source:     "package p\nimport \"example.com/old/dal\"\nvar key *dal.Key\n",
			wantRecord: true,
		},
		{
			name:       "aliased import becomes unused",
			source:     "package p\nimport olddal \"example.com/old/dal\"\nvar key *olddal.Key\n",
			wantRecord: true,
		},
		{
			name:       "remaining selector keeps import",
			source:     "package p\nimport \"example.com/old/dal\"\nvar key *dal.Key\nvar db dal.DB\n",
			wantOld:    true,
			wantRecord: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			updated, changed, err := transformGo([]byte(test.source), test.name+".go", step)
			if err != nil {
				t.Fatal(err)
			}
			if !changed {
				t.Fatal("transform reported no change")
			}
			got := string(updated)
			if strings.Contains(got, `"example.com/old/dal"`) != test.wantOld {
				t.Fatalf("old import presence = %v, want %v:\n%s", strings.Contains(got, `"example.com/old/dal"`), test.wantOld, got)
			}
			if strings.Contains(got, `"example.com/record"`) != test.wantRecord {
				t.Fatalf("record import presence = %v, want %v:\n%s", strings.Contains(got, `"example.com/record"`), test.wantRecord, got)
			}
		})
	}
}

func TestRemoveUnusedGoImportPreservesBlankAndDotImports(t *testing.T) {
	for _, importName := range []string{"_", "."} {
		t.Run(importName, func(t *testing.T) {
			fset := token.NewFileSet()
			source := "package p\nimport " + importName + " \"example.com/sideeffect\"\n"
			file, err := parser.ParseFile(fset, "sideeffect.go", source, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			if removeUnusedGoImport(file, fset, "example.com/sideeffect") {
				t.Fatalf("%s import was removed", importName)
			}
		})
	}
}

func TestGoCompositeFieldRenameIsLimitedToTypedStructLiterals(t *testing.T) {
	step := Step{Kind: "composite_field.rename", Language: "go", From: "RecordWithID", To: "WithID"}
	source := `package p

type T struct { RecordWithID int }
type K string

var named = T{RecordWithID: 1}
var generic = Box[int]{RecordWithID: 2}
var selected = model.Entry{RecordWithID: 3}
var mapping = map[K]int{RecordWithID: 4}
var array = [...]int{RecordWithID: 5}
var text = "RecordWithID:"
`
	updated, changed, err := transformGo([]byte(source), "fields.go", step)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("transform reported no change")
	}
	got := string(updated)
	for _, want := range []string{
		"T{WithID: 1}",
		"Box[int]{WithID: 2}",
		"model.Entry{WithID: 3}",
		"map[K]int{RecordWithID: 4}",
		"[...]int{RecordWithID: 5}",
		"\"RecordWithID:\"",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("result missing %q:\n%s", want, got)
		}
	}
}
