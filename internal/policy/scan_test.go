package policy

import (
	"os"
	"path/filepath"
	"testing"
)

func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const fixtureGoMod = `module github.com/acme/cal/backend

go 1.26

require (
	github.com/acme/other/backend v1.2.3
	github.com/dal-go/dalgo v0.9.0
)

require github.com/acme/transitive/backend v0.1.0 // indirect
`

func TestScanReadsModulePathImportsAndPositions(t *testing.T) {
	root := writeModule(t, map[string]string{
		"go.mod": fixtureGoMod,
		"facade4cal/facade.go": `package facade4cal

import (
	"fmt"

	"github.com/acme/other/backend/dbo4other"
)

var _ = fmt.Sprint
`,
		"facade4cal/facade_test.go": `package facade4cal

import "github.com/dal-go/dalgo2firestore"

var _ = dalgo2firestore.Name
`,
		"vendor/github.com/x/y.go": `package y

import "github.com/acme/forbidden/backend"
`,
		"testdata/bad.go":  `package bad` + "\n\nimport \"github.com/acme/forbidden/backend\"\n",
		"_ignored/skip.go": `package skip` + "\n\nimport \"github.com/acme/forbidden/backend\"\n",
	})

	module, err := ScanModule(root)
	if err != nil {
		t.Fatalf("ScanModule: %v", err)
	}
	if module.Path != "github.com/acme/cal/backend" {
		t.Fatalf("module path = %q", module.Path)
	}

	var sourceImport, testImport *Reference
	for index := range module.References {
		reference := &module.References[index]
		if reference.Import == "github.com/acme/other/backend/dbo4other" && reference.Scope == ScopeSource {
			sourceImport = reference
		}
		if reference.Import == "github.com/dal-go/dalgo2firestore" && reference.Scope == ScopeTests {
			testImport = reference
		}
		if reference.Import == "github.com/acme/forbidden/backend" {
			t.Fatalf("vendor, testdata and _ dirs must not be scanned: found %s", reference.File)
		}
	}
	if sourceImport == nil {
		t.Fatal("source import not found")
	}
	if sourceImport.File != "facade4cal/facade.go" {
		t.Fatalf("file = %q, want facade4cal/facade.go", sourceImport.File)
	}
	if sourceImport.Line != 6 {
		t.Fatalf("line = %d, want 6", sourceImport.Line)
	}
	if sourceImport.Package != "facade4cal" {
		t.Fatalf("package = %q", sourceImport.Package)
	}
	if testImport == nil {
		t.Fatal("a _test.go import should be scanned in the tests scope")
	}
}

func TestScanRecordsManifestRequirements(t *testing.T) {
	root := writeModule(t, map[string]string{"go.mod": fixtureGoMod})
	module, err := ScanModule(root)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, reference := range module.References {
		if reference.Import != "github.com/acme/other/backend" {
			continue
		}
		found = true
		if reference.File != "go.mod" {
			t.Fatalf("manifest requirement file = %q", reference.File)
		}
		if !reference.Manifest {
			t.Fatal("manifest requirement should be flagged as such")
		}
		if reference.Scope != ScopeSource {
			t.Fatalf("manifest requirement scope = %q, want source", reference.Scope)
		}
	}
	if !found {
		t.Fatal("go.mod requirement not recorded — a forbidden dependency must be caught before an import appears")
	}
}

func TestScanReportsMissingModule(t *testing.T) {
	if _, err := ScanModule(t.TempDir()); err == nil {
		t.Fatal("scanning a directory with no go.mod should fail")
	}
}

func TestScanSurvivesUnparseableFile(t *testing.T) {
	root := writeModule(t, map[string]string{
		"go.mod":      fixtureGoMod,
		"broken/x.go": "package broken\n\nimport (\n\t\"unterminated\n",
		"ok/ok.go":    "package ok\n\nimport \"github.com/dal-go/dalgo\"\n",
	})
	module, err := ScanModule(root)
	if err != nil {
		t.Fatalf("an unparseable file must not abort the scan: %v", err)
	}
	if len(module.Unparseable) != 1 || module.Unparseable[0] != "broken/x.go" {
		t.Fatalf("unparseable = %v, want exactly [broken/x.go]", module.Unparseable)
	}
	var found bool
	for _, reference := range module.References {
		if reference.Import == "github.com/dal-go/dalgo" && reference.File == "ok/ok.go" {
			found = true
		}
	}
	if !found {
		t.Fatal("the rest of the module should still be scanned")
	}
}

// The scan parses imports only, so a file whose body does not compile is still
// read for its imports. That is deliberate: an architecture boundary should be
// checkable while the code inside it is mid-edit or outright broken.
func TestScanReadsImportsFromAFileThatDoesNotCompile(t *testing.T) {
	root := writeModule(t, map[string]string{
		"go.mod": fixtureGoMod,
		"wip/x.go": `package wip

import "github.com/acme/other/backend/dbo4other"

func broken( { this is not go
`,
	})
	module, err := ScanModule(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(module.Unparseable) != 0 {
		t.Fatalf("unparseable = %v, want none", module.Unparseable)
	}
	for _, reference := range module.References {
		if reference.Import == "github.com/acme/other/backend/dbo4other" && reference.File == "wip/x.go" {
			return
		}
	}
	t.Fatal("import not found in a file that does not compile")
}

// An indirect requirement is in go.mod because something else needed it. A
// repository cannot remove one without changing its dependencies' own
// dependencies, so reporting it would be a finding nobody can act on.
func TestScanIgnoresIndirectRequirements(t *testing.T) {
	root := writeModule(t, map[string]string{"go.mod": fixtureGoMod})
	module, err := ScanModule(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range module.References {
		if reference.Import == "github.com/acme/transitive/backend" {
			t.Fatal("an indirect requirement must not be reported")
		}
	}
}

// A module required directly but imported only from tests is a test
// dependency. Judging it by production rules would report a violation nobody
// can act on without deleting a legitimate test.
func TestScanAttributesManifestRequirementToItsUsedScope(t *testing.T) {
	root := writeModule(t, map[string]string{
		"go.mod":               "module github.com/acme/cal/backend\n\ngo 1.26\n\nrequire (\n\tgithub.com/dal-go/dalgo2firestore v0.1.0\n\tgithub.com/acme/other/backend v1.0.0\n)\n",
		"dal4cal/repo_test.go": "package dal4cal\n\nimport \"github.com/dal-go/dalgo2firestore\"\n",
		"dal4cal/repo.go":      "package dal4cal\n\nimport \"github.com/acme/other/backend/dbo\"\n",
	})
	module, err := ScanModule(root)
	if err != nil {
		t.Fatal(err)
	}
	scopes := map[string]string{}
	for _, reference := range module.References {
		if reference.Manifest {
			scopes[reference.Import] = reference.Scope
		}
	}
	if scopes["github.com/dal-go/dalgo2firestore"] != ScopeTests {
		t.Fatalf("test-only requirement scope = %q, want tests", scopes["github.com/dal-go/dalgo2firestore"])
	}
	if scopes["github.com/acme/other/backend"] != ScopeSource {
		t.Fatalf("source requirement scope = %q, want source", scopes["github.com/acme/other/backend"])
	}
}

func TestScanRequirementUsedInBothScopesIsJudgedAsSource(t *testing.T) {
	root := writeModule(t, map[string]string{
		"go.mod":      "module github.com/acme/cal/backend\n\ngo 1.26\n\nrequire github.com/acme/other/backend v1.0.0\n",
		"a/a_test.go": "package a\n\nimport \"github.com/acme/other/backend/dbo\"\n",
		"a/a.go":      "package a\n\nimport \"github.com/acme/other/backend/dto\"\n",
	})
	module, err := ScanModule(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range module.References {
		if reference.Manifest && reference.Scope != ScopeSource {
			t.Fatalf("scope = %q, want source", reference.Scope)
		}
	}
}

func TestScanPutsPackageMainInTheMainScope(t *testing.T) {
	root := writeModule(t, map[string]string{
		"go.mod":                "module github.com/acme/cal/backend\n\ngo 1.26\n",
		"cmd/cald/main.go":      "package main\n\nimport \"github.com/dal-go/dalgo2firestore\"\n",
		"facade4cal/f.go":       "package facade4cal\n\nimport \"github.com/dal-go/dalgo\"\n",
		"cmd/cald/main_test.go": "package main\n\nimport \"github.com/dal-go/dalgo2firestore\"\n",
	})
	module, err := ScanModule(root)
	if err != nil {
		t.Fatal(err)
	}
	scopes := map[string]string{}
	for _, reference := range module.References {
		if !reference.Manifest {
			scopes[reference.File] = reference.Scope
		}
	}
	if scopes["cmd/cald/main.go"] != ScopeMain {
		t.Fatalf("main.go scope = %q, want main", scopes["cmd/cald/main.go"])
	}
	if scopes["cmd/cald/main_test.go"] != ScopeTests {
		t.Fatalf("a test in package main is still a test, got %q", scopes["cmd/cald/main_test.go"])
	}
	if scopes["facade4cal/f.go"] != ScopeSource {
		t.Fatalf("ordinary package scope = %q, want source", scopes["facade4cal/f.go"])
	}
}
