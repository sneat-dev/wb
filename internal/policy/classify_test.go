package policy

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	loaded := loadSample(t)
	const self = "github.com/acme/cal/backend"
	cases := []struct {
		importPath string
		want       string
	}{
		{"github.com/acme/cal/backend/dal4cal", "own-repo"},
		{"github.com/acme/ext-cal/backend/facade4cal", "extension-contract"},
		{"github.com/acme/other/backend/dbo4other", "extension-implementation"},
		{"github.com/acme/host/pkg/modules", "host"},
		{"github.com/dal-go/dalgo/dal", "dalgo-core"},
		{"github.com/dal-go/dalgo2firestore", "dalgo-adapter"},
		{"github.com/dal-go/dalgo4spanner/x", "dalgo-adapter"},
		{"gopkg.in/yaml.v3", "third-party"},
		{"fmt", GroupStdlib},
		{"net/http", GroupStdlib},
		{"encoding/json", GroupStdlib},
	}
	for _, tc := range cases {
		t.Run(tc.importPath, func(t *testing.T) {
			got := loaded.Classify(tc.importPath, self)
			if got.Group != tc.want {
				t.Fatalf("Classify(%q).Group = %q, want %q", tc.importPath, got.Group, tc.want)
			}
		})
	}
}

func TestClassifyUnclassifiedWhenNoCatchAll(t *testing.T) {
	body := `
groups:
  - {name: only, match: ["github.com/acme/..."]}
types:
  - name: t
    detect: ["github.com/acme/*/backend"]
    scopes: {source: {allow: [only]}}
`
	loaded, err := Load(writePolicy(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Classify("example.com/other/pkg", ""); got.Group != GroupUnclassified {
		t.Fatalf("group = %q, want %q", got.Group, GroupUnclassified)
	}
}

func TestClassifyRecordsPrecedenceAndShadowedMatches(t *testing.T) {
	loaded := loadSample(t)
	got := loaded.Classify("github.com/acme/ext-cal/backend/facade4cal", "")
	if got.Group != "extension-contract" {
		t.Fatalf("group = %q", got.Group)
	}
	if got.PatternNumber != 2 {
		t.Fatalf("pattern number = %d, want 2", got.PatternNumber)
	}
	if len(got.AlsoMatched) == 0 {
		t.Fatal("expected the broad sneat-co pattern to be recorded as also-matching")
	}
	if got.AlsoMatched[0].Group != "extension-implementation" {
		t.Fatalf("also-matched group = %q, want extension-implementation", got.AlsoMatched[0].Group)
	}
}

func TestDetectType(t *testing.T) {
	loaded := loadSample(t)
	cases := map[string]string{
		"github.com/acme/cal/backend":     "extension-implementation",
		"github.com/acme/ext-cal/backend": "extension-contract",
	}
	for module, want := range cases {
		got, err := loaded.Detect(module)
		if err != nil {
			t.Fatalf("Detect(%q): %v", module, err)
		}
		if got != want {
			t.Fatalf("Detect(%q) = %q, want %q", module, got, want)
		}
	}
	if _, err := loaded.Detect("example.com/unrelated"); err == nil {
		t.Fatal("Detect on an unmatched module should fail, not guess")
	}
}

func TestDetectIsFirstMatchWins(t *testing.T) {
	body := `
groups:
  - {name: g, match: ["..."]}
types:
  - name: narrow
    detect: ["github.com/acme/ext-*/backend"]
    scopes: {source: {allow: [g]}}
  - name: broad
    detect: ["github.com/acme/*/backend"]
    scopes: {source: {allow: [g]}}
`
	loaded, err := Load(writePolicy(t, body))
	if err != nil {
		t.Fatal(err)
	}
	got, err := loaded.Detect("github.com/acme/ext-thing/backend")
	if err != nil {
		t.Fatal(err)
	}
	if got != "narrow" {
		t.Fatalf("Detect = %q, want the first declared match %q", got, "narrow")
	}
}

func TestValidateReportsShadowedDetectPattern(t *testing.T) {
	body := `
groups:
  - {name: g, match: ["..."]}
types:
  - name: broad
    detect: ["github.com/acme/*/backend"]
    scopes: {source: {allow: [g]}}
  - name: narrow
    detect: ["github.com/acme/ext-*/backend"]
    scopes: {source: {allow: [g]}}
`
	loaded, err := Load(writePolicy(t, body))
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range Validate(loaded) {
		if strings.Contains(diagnostic.Message, "type \"narrow\" detect pattern") {
			return
		}
	}
	t.Fatal("expected a shadowed detect-pattern diagnostic")
}
