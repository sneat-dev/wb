package migrate

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func migCovParseGo(t *testing.T, source string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "example.go", source, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse test source: %v\n%s", err, source)
	}
	return fset, file
}

func TestMigCovTransformRejectsUnsupportedAndUnadaptedSteps(t *testing.T) {
	t.Parallel()
	if _, _, err := transform([]Step{{Kind: "regex.replace"}}, "go", []byte("package p\n"), "x.go"); err == nil ||
		!strings.Contains(err.Error(), `unsupported kind "regex.replace"`) {
		t.Fatalf("transform(unsupported kind) = %v", err)
	}
	if _, _, err := transform([]Step{{Kind: "import.replace", Language: "python", From: "a", To: "b"}}, "python", []byte("a\n"), "x.py"); err == nil ||
		!strings.Contains(err.Error(), "requires a structural adapter") {
		t.Fatalf("transform(missing adapter) = %v", err)
	}
	if _, _, err := transform([]Step{{Kind: "import.replace", Language: "go", From: "a", To: "b"}}, "go", []byte("package p\nfunc (\n"), "x.go"); err == nil ||
		!strings.Contains(err.Error(), "parse Go source") {
		t.Fatalf("transform(adapter failure) = %v", err)
	}
}

func TestMigCovTransformGoRejectsUnsupportedKindAndUnparseableSource(t *testing.T) {
	t.Parallel()
	if _, _, err := transformGo([]byte("package p\n"), "p.go", Step{Kind: "text.replace", From: "a", To: "b"}); err == nil ||
		!strings.Contains(err.Error(), `unsupported Go step "text.replace"`) {
		t.Fatalf("transformGo(text.replace) = %v", err)
	}
	if _, _, err := transformGo([]byte("package p\nfunc (\n"), "p.go", Step{Kind: "import.replace", From: "a", To: "b"}); err == nil ||
		!strings.Contains(err.Error(), "parse Go source") {
		t.Fatalf("transformGo(broken source) = %v", err)
	}
}

func TestMigCovTransformGoLeavesMemberlessRewriteTargetsAlone(t *testing.T) {
	t.Parallel()
	// A rewrite target that does not name a member cannot be expressed as a
	// package-qualified selector, so the selector is left untouched while the
	// new import is still made available.
	source := "package p\n\nimport \"example.com/dal\"\n\nvar key = dal.Key\n"
	updated, changed, err := transformGo([]byte(source), "p.go", Step{
		Kind: "selector.rewrite", Import: "example.com/dal",
		AddImport: "example.com/record", Rewrites: map[string]string{"Key": "Record"},
	})
	if err != nil {
		t.Fatalf("transformGo() error = %v", err)
	}
	if !changed {
		t.Fatal("transformGo() reported no change after adding an import")
	}
	got := string(updated)
	if !strings.Contains(got, `"example.com/record"`) {
		t.Errorf("added import missing:\n%s", got)
	}
	if !strings.Contains(got, "dal.Key") {
		t.Errorf("memberless rewrite target changed the selector:\n%s", got)
	}
}

func TestMigCovTypedStructCompositeAcceptsInstantiatedNamedTypes(t *testing.T) {
	t.Parallel()
	step := Step{Kind: "composite_field.rename", Language: "go", From: "RecordWithID", To: "WithID"}
	source := "package p\n\ntype Pair[A, B any] struct { RecordWithID int }\n\nvar pair = Pair[int, string]{RecordWithID: 1}\n"
	updated, changed, err := transformGo([]byte(source), "p.go", step)
	if err != nil {
		t.Fatalf("transformGo() error = %v", err)
	}
	if !changed {
		t.Fatal("transformGo() did not rename the index-list composite field")
	}
	if !strings.Contains(string(updated), "Pair[int, string]{WithID: 1}") {
		t.Fatalf("updated source = %s", updated)
	}
}

func TestMigCovRemoveUnusedGoImportIgnoresAbsentAndOtherImports(t *testing.T) {
	t.Parallel()
	fset, file := migCovParseGo(t, "package p\n\nimport (\n\t\"example.com/other\"\n\t\"example.com/unused\"\n)\n\nvar x = other.Key\n")
	if removeUnusedGoImport(file, fset, "example.com/absent") {
		t.Fatal("removeUnusedGoImport removed an import that was never declared")
	}
	if !removeUnusedGoImport(file, fset, "example.com/unused") {
		t.Fatal("removeUnusedGoImport did not remove the unused import")
	}
	var out strings.Builder
	if err := format.Node(&out, fset, file); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "example.com/unused") {
		t.Errorf("unused import survived removal:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "example.com/other") {
		t.Errorf("used import was removed:\n%s", out.String())
	}
}

func TestMigCovEnsureGoImportAddsDeclarationAndAlias(t *testing.T) {
	t.Parallel()
	// No import declaration exists at all: the runner must create one rather
	// than assume the file already imports something.
	fset, file := migCovParseGo(t, "package p\n\nvar x = 1\n")
	name, changed := ensureGoImport(file, fset, "example.com/record", "record")
	if !changed || name != "record" {
		t.Fatalf("ensureGoImport() = %q, %v; want record, true", name, changed)
	}
	if len(file.Imports) != 1 || file.Imports[0].Path.Value != `"example.com/record"` {
		t.Fatalf("imports = %+v", file.Imports)
	}

	// A preferred alias that differs from the package's own name is applied to
	// the declaration that already exists.
	fset, file = migCovParseGo(t, "package p\n\nimport \"example.com/other\"\n\nvar y = other.Key\n")
	name, changed = ensureGoImport(file, fset, "example.com/record", "rec")
	if !changed || name != "rec" {
		t.Fatalf("ensureGoImport(alias) = %q, %v; want rec, true", name, changed)
	}
	var out strings.Builder
	if err := format.Node(&out, fset, file); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `rec "example.com/record"`) {
		t.Fatalf("aliased import missing:\n%s", out.String())
	}
}

func TestMigCovEnsureGoImportKeepsOrDropsExistingAlias(t *testing.T) {
	t.Parallel()
	// The package is already imported under the preferred name.
	fset, file := migCovParseGo(t, "package p\n\nimport \"example.com/record\"\n\nvar x = 1\n")
	name, changed := ensureGoImport(file, fset, "example.com/record", "record")
	if changed || name != "record" {
		t.Fatalf("ensureGoImport(existing) = %q, %v; want record, false", name, changed)
	}

	// The package is imported under a different alias that can be simplified
	// back to its own name.
	fset, file = migCovParseGo(t, "package p\n\nimport oldrecord \"example.com/record\"\n\nvar x = oldrecord.Key\n")
	name, changed = ensureGoImport(file, fset, "example.com/record", "record")
	if !changed || name != "record" {
		t.Fatalf("ensureGoImport(alias rewrite) = %q, %v; want record, true", name, changed)
	}
	if file.Imports[0].Name != nil {
		t.Fatalf("import alias was not reset to the package name: %+v", file.Imports[0].Name)
	}
	var out strings.Builder
	if err := format.Node(&out, fset, file); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"example.com/record"`) || !strings.Contains(out.String(), "record.Key") {
		t.Fatalf("selectors were not rebound:\n%s", out.String())
	}
}

func TestMigCovAvailableGoIdentifierFallsBackDeterministically(t *testing.T) {
	t.Parallel()
	_, file := migCovParseGo(t, "package p\n\nvar record = 1\nvar dalrecord = 2\n")
	if got := availableGoIdentifier(file, "record"); got != "dalrecord2" {
		t.Fatalf("availableGoIdentifier() = %q, want dalrecord2", got)
	}
	_, file = migCovParseGo(t, "package p\n\nvar record = 1\n")
	if got := availableGoIdentifier(file, "record"); got != "dalrecord" {
		t.Fatalf("availableGoIdentifier() = %q, want dalrecord", got)
	}
	_, file = migCovParseGo(t, "package p\n\nvar x = 1\n")
	if got := availableGoIdentifier(file, "record"); got != "record" {
		t.Fatalf("availableGoIdentifier() = %q, want record", got)
	}
}
