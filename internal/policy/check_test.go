package policy

import (
	"strings"
	"testing"
)

const layeredPolicy = `
groups:
  - {name: own-repo,                 match: ["<self>/..."]}
  - {name: extension-contract,       match: ["github.com/acme/ext-*/..."]}
  - {name: extension-implementation, match: ["github.com/acme/*/..."]}
  - {name: dalgo-adapter,            match: ["github.com/dal-go/dalgo{2,4}*/..."]}
  - {name: dalgo-core,               match: ["github.com/dal-go/..."]}
  - {name: third-party,              match: ["..."]}
types:
  - name: extension-contract
    detect: ["github.com/acme/ext-*/backend"]
    scopes: {source: {allow: [own-repo, extension-contract, third-party]}}
  - name: extension-implementation
    detect: ["github.com/acme/*/backend"]
    scopes:
      source: {allow: [own-repo, extension-contract, dalgo-core, third-party]}
      tests:  {allow: [own-repo, extension-contract, dalgo-core, dalgo-adapter, third-party]}
layers:
  mode: enforce
  roles:
    api:    ["api4*"]
    facade: ["facade4*"]
    dal:    ["dal4*"]
    dbo:    ["dbo4*"]
  order: [[api], [facade], [dal], [dbo]]
  forbid:
    - {from: api, to: dal, reason: "delivery must go through the facade"}
`

func checkFixture(t *testing.T, files map[string]string) Result {
	t.Helper()
	loaded, err := Load(writePolicy(t, layeredPolicy))
	if err != nil {
		t.Fatal(err)
	}
	module, err := ScanModule(writeModule(t, files))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Check(loaded, module, "")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCheckFlagsSiblingImplementationImport(t *testing.T) {
	result := checkFixture(t, map[string]string{
		"go.mod":               "module github.com/acme/cal/backend\n\ngo 1.26\n",
		"facade4cal/facade.go": "package facade4cal\n\nimport \"github.com/acme/other/backend/dbo4other\"\n",
	})
	if result.Type != "extension-implementation" {
		t.Fatalf("type = %q", result.Type)
	}
	if result.Blocking() != 1 {
		t.Fatalf("blocking = %d, want 1: %+v", result.Blocking(), result.Findings)
	}
	finding := result.Findings[0]
	if finding.Group != "extension-implementation" {
		t.Fatalf("group = %q", finding.Group)
	}
	if finding.Line != 3 {
		t.Fatalf("line = %d, want 3", finding.Line)
	}
}

func TestCheckAllowsContractAndOwnRepo(t *testing.T) {
	result := checkFixture(t, map[string]string{
		"go.mod":               "module github.com/acme/cal/backend\n\ngo 1.26\n",
		"facade4cal/facade.go": "package facade4cal\n\nimport (\n\t\"fmt\"\n\t\"github.com/acme/ext-other/backend/dto\"\n\t\"github.com/acme/cal/backend/dal4cal\"\n\t\"github.com/dal-go/dalgo/dal\"\n)\n",
	})
	if result.Blocking() != 0 {
		t.Fatalf("blocking = %d, want 0: %+v", result.Blocking(), result.Findings)
	}
}

func TestCheckSeparatesTestScope(t *testing.T) {
	files := map[string]string{
		"go.mod":               "module github.com/acme/cal/backend\n\ngo 1.26\n",
		"dal4cal/repo_test.go": "package dal4cal\n\nimport \"github.com/dal-go/dalgo2firestore\"\n",
	}
	if got := checkFixture(t, files).Blocking(); got != 0 {
		t.Fatalf("a dalgo adapter in _test.go should be allowed, got %d findings", got)
	}
	files["dal4cal/repo.go"] = "package dal4cal\n\nimport \"github.com/dal-go/dalgo2firestore\"\n"
	if got := checkFixture(t, files).Blocking(); got != 1 {
		t.Fatalf("the same adapter in source must be forbidden, got %d findings", got)
	}
}

func TestCheckFlagsManifestRequirementBeforeAnyImport(t *testing.T) {
	result := checkFixture(t, map[string]string{
		"go.mod": "module github.com/acme/cal/backend\n\ngo 1.26\n\nrequire github.com/acme/other/backend v1.0.0\n",
	})
	if result.Blocking() != 1 {
		t.Fatalf("blocking = %d, want 1: %+v", result.Blocking(), result.Findings)
	}
	if !result.Findings[0].Manifest {
		t.Fatal("finding should be flagged as a manifest requirement")
	}
}

func TestCheckFlagsLayerInversion(t *testing.T) {
	result := checkFixture(t, map[string]string{
		"go.mod":          "module github.com/acme/cal/backend\n\ngo 1.26\n",
		"dal4cal/repo.go": "package dal4cal\n\nimport \"github.com/acme/cal/backend/api4cal\"\n",
	})
	if result.Blocking() != 1 {
		t.Fatalf("blocking = %d, want 1: %+v", result.Blocking(), result.Findings)
	}
	finding := result.Findings[0]
	if finding.Rule != RuleLayer {
		t.Fatalf("rule = %q, want %q", finding.Rule, RuleLayer)
	}
	if finding.FromRole != "dal" || finding.ToRole != "api" {
		t.Fatalf("roles = %q → %q", finding.FromRole, finding.ToRole)
	}
}

func TestCheckAllowsDownwardAndSameLayerImports(t *testing.T) {
	result := checkFixture(t, map[string]string{
		"go.mod":               "module github.com/acme/cal/backend\n\ngo 1.26\n",
		"facade4cal/facade.go": "package facade4cal\n\nimport \"github.com/acme/cal/backend/dal4cal\"\n",
		"dal4cal/repo.go":      "package dal4cal\n\nimport \"github.com/acme/cal/backend/dbo4cal\"\n",
		"dbo4cal/entity.go":    "package dbo4cal\n",
	})
	if result.Blocking() != 0 {
		t.Fatalf("blocking = %d, want 0: %+v", result.Blocking(), result.Findings)
	}
}

func TestCheckFlagsForbiddenEdgeEvenWhenDownward(t *testing.T) {
	result := checkFixture(t, map[string]string{
		"go.mod":          "module github.com/acme/cal/backend\n\ngo 1.26\n",
		"api4cal/http.go": "package api4cal\n\nimport \"github.com/acme/cal/backend/dal4cal\"\n",
	})
	if result.Blocking() != 1 {
		t.Fatalf("blocking = %d, want 1: %+v", result.Blocking(), result.Findings)
	}
	if !strings.Contains(result.Findings[0].Message, "delivery must go through the facade") {
		t.Fatalf("message should carry the declared reason: %q", result.Findings[0].Message)
	}
}

func TestCheckReportModeDoesNotBlock(t *testing.T) {
	body := strings.Replace(layeredPolicy, "mode: enforce", "mode: report", 1)
	loaded, err := Load(writePolicy(t, body))
	if err != nil {
		t.Fatal(err)
	}
	module, err := ScanModule(writeModule(t, map[string]string{
		"go.mod":          "module github.com/acme/cal/backend\n\ngo 1.26\n",
		"dal4cal/repo.go": "package dal4cal\n\nimport \"github.com/acme/cal/backend/api4cal\"\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Check(loaded, module, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(result.Findings))
	}
	if result.Blocking() != 0 {
		t.Fatalf("report-mode findings must not block, blocking = %d", result.Blocking())
	}
}

func TestCheckExplicitTypeOverridesDetection(t *testing.T) {
	result := checkFixture(t, map[string]string{
		"go.mod": "module github.com/acme/cal/backend\n\ngo 1.26\n",
	})
	if result.Type != "extension-implementation" {
		t.Fatalf("detected type = %q", result.Type)
	}
	loaded, err := Load(writePolicy(t, layeredPolicy))
	if err != nil {
		t.Fatal(err)
	}
	module, err := ScanModule(writeModule(t, map[string]string{
		"go.mod": "module github.com/acme/cal/backend\n\ngo 1.26\n",
		"x/x.go": "package x\n\nimport \"github.com/dal-go/dalgo/dal\"\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := Check(loaded, module, "extension-contract")
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Type != "extension-contract" {
		t.Fatalf("explicit type = %q", explicit.Type)
	}
	if explicit.Blocking() != 1 {
		t.Fatalf("the contract type forbids dalgo-core, want 1 finding, got %d", explicit.Blocking())
	}
}

func TestCheckRejectsUnknownExplicitType(t *testing.T) {
	loaded, err := Load(writePolicy(t, layeredPolicy))
	if err != nil {
		t.Fatal(err)
	}
	module, err := ScanModule(writeModule(t, map[string]string{
		"go.mod": "module github.com/acme/cal/backend\n\ngo 1.26\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Check(loaded, module, "nonesuch"); err == nil {
		t.Fatal("an undeclared explicit type must be an error")
	}
}
