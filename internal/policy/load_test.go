package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const samplePolicy = `
groups:
  - {name: own-repo,                 match: ["<self>/..."]}
  - {name: extension-contract,       match: ["github.com/acme/ext-*/..."]}
  - {name: host,                     match: ["github.com/acme/{host,bots}/..."]}
  - {name: extension-implementation, match: ["github.com/acme/*/..."]}
  - {name: dalgo-adapter,            match: ["github.com/dal-go/dalgo{2,4}*/..."]}
  - {name: dalgo-core,               match: ["github.com/dal-go/..."]}
  - {name: third-party,              match: ["..."]}

types:
  - name: extension-contract
    detect: ["github.com/acme/ext-*/backend"]
    scopes:
      source: {allow: [own-repo, extension-contract, dalgo-core, third-party]}
  - name: extension-implementation
    detect: ["github.com/acme/*/backend"]
    scopes:
      source: {allow: [own-repo, extension-contract, dalgo-core, third-party]}
      tests:  {allow: [own-repo, extension-contract, dalgo-core, dalgo-adapter, third-party]}

layers:
  mode: report
  unknown-role: ignore
  roles:
    const:  ["const4*"]
    dbo:    ["dbo4*"]
    dal:    ["dal4*"]
    facade: ["facade4*"]
    api:    ["api4*"]
  order:
    - [api]
    - [facade]
    - [dal]
    - [dbo]
    - [const]

expect:
  - {import: "github.com/acme/ext-cal/backend/facade", group: extension-contract}
  - {module: "github.com/acme/cal/backend", type: extension-implementation}
`

func writePolicy(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	name := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func loadSample(t *testing.T) Policy {
	t.Helper()
	loaded, err := Load(writePolicy(t, samplePolicy))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return loaded
}

func TestLoadPreservesGroupOrder(t *testing.T) {
	loaded := loadSample(t)
	want := []string{"own-repo", "extension-contract", "host", "extension-implementation", "dalgo-adapter", "dalgo-core", "third-party"}
	got := loaded.GroupNames()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("group order = %v, want %v", got, want)
	}
}

func TestLoadReadsScopesAndLayers(t *testing.T) {
	loaded := loadSample(t)
	repoType, ok := loaded.Type("extension-implementation")
	if !ok {
		t.Fatal("extension-implementation type missing")
	}
	if !repoType.Scopes[ScopeTests].Allows("dalgo-adapter") {
		t.Fatal("tests scope should allow dalgo-adapter")
	}
	if repoType.Scopes[ScopeSource].Allows("dalgo-adapter") {
		t.Fatal("source scope must not allow dalgo-adapter")
	}
	if loaded.Layers.Mode != ModeReport {
		t.Fatalf("layer mode = %q, want report", loaded.Layers.Mode)
	}
	if len(loaded.Expectations) != 2 {
		t.Fatalf("expectations = %d, want 2", len(loaded.Expectations))
	}
}

func TestScopeDefaultsToSourceWhenTestsAbsent(t *testing.T) {
	loaded := loadSample(t)
	contract, _ := loaded.Type("extension-contract")
	if _, ok := contract.Scopes[ScopeTests]; !ok {
		t.Fatal("a type without an explicit tests scope should inherit source")
	}
	if contract.Scopes[ScopeTests].Allows("dalgo-adapter") {
		t.Fatal("inherited tests scope must not gain anything")
	}
}

const groupsBlock = "groups:\n  - {name: g, match: [\"...\"]}\n"
const typesBlock = "types:\n  - name: t\n    detect: [\"x/y\"]\n    scopes: {source: {allow: [g]}}\n"

func TestLoadRejects(t *testing.T) {
	cases := map[string]string{
		"no groups":                "types:\n  - name: a\n    detect: [\"x/y\"]\n    scopes: {source: {allow: []}}\n",
		"no types":                 "groups:\n  - {name: g, match: [\"...\"]}\n",
		"duplicate group":          "groups:\n  - {name: g, match: [\"a/...\"]}\n  - {name: g, match: [\"b/...\"]}\n" + typesBlock,
		"unknown group in allow":   groupsBlock + "types:\n  - name: t\n    detect: [\"x/y\"]\n    scopes: {source: {allow: [nope]}}\n",
		"type without detect":      groupsBlock + "types:\n  - name: t\n    scopes: {source: {allow: [g]}}\n",
		"bad pattern":              "groups:\n  - {name: g, match: [\"a/.../b\"]}\n" + typesBlock,
		"bad mode":                 groupsBlock + typesBlock + "layers:\n  mode: whenever\n",
		"order names unknown role": groupsBlock + typesBlock + "layers:\n  roles: {api: [\"api4*\"]}\n  order: [[api],[ghost]]\n",
		"stdlib in allow":          groupsBlock + "types:\n  - name: t\n    detect: [\"x/y\"]\n    scopes: {source: {allow: [stdlib]}}\n",
		"duplicate type":           groupsBlock + "types:\n  - name: t\n    detect: [\"x/y\"]\n    scopes: {source: {allow: [g]}}\n  - name: t\n    detect: [\"x/z\"]\n    scopes: {source: {allow: [g]}}\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writePolicy(t, body)); err == nil {
				t.Fatal("Load succeeded, want error")
			}
		})
	}
}

func TestValidateReportsShadowedPatternAndUnusedRole(t *testing.T) {
	body := `
groups:
  - {name: broad,  match: ["github.com/acme/*/..."]}
  - {name: narrow, match: ["github.com/acme/ext-*/..."]}
types:
  - name: t
    detect: ["github.com/acme/*/backend"]
    scopes: {source: {allow: [broad]}}
layers:
  roles: {api: ["api4*"], orphan: ["orphan4*"]}
  order: [[api]]
`
	loaded, err := Load(writePolicy(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	diagnostics := Validate(loaded)
	var shadow, orphan bool
	for _, diagnostic := range diagnostics {
		switch {
		case strings.Contains(diagnostic.Message, "unreachable"):
			shadow = true
		case strings.Contains(diagnostic.Message, "never placed in the layer order"):
			orphan = true
		}
	}
	if !shadow {
		t.Errorf("expected an unreachable-pattern diagnostic, got %+v", diagnostics)
	}
	if !orphan {
		t.Errorf("expected an orphan-role diagnostic, got %+v", diagnostics)
	}
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "never allowed") {
			t.Errorf("a group nobody allows is how a policy forbids something, not a defect: %q", diagnostic.Message)
		}
	}
}
