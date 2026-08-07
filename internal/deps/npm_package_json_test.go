package deps

import (
	"strings"
	"testing"
)

const npmPackageJSONFixture = `{
  "name": "@sneat/example",
  "version": "1.0.0",
  "dependencies": {
    "@sneat/core": "1.2.3",
    "lodash": "^4.17.21"
  },
  "devDependencies": {
    "@sneat/core": "1.2.3",
    "typescript": "^5.4.0"
  },
  "peerDependencies": {
    "@sneat/core": "1.2.3"
  },
  "optionalDependencies": {
    "@sneat/core": "1.2.3"
  },
  "scripts": {
    "build": "tsc"
  }
}
`

func TestScanNpmPackageJSONRefsFindsAllFourDependencyFields(t *testing.T) {
	t.Parallel()
	refs := scanNpmPackageJSONRefs([]byte(npmPackageJSONFixture))
	byField := map[string]npmPackageJSONRef{}
	for _, ref := range refs {
		if ref.Key == "@sneat/core" {
			byField[ref.Field] = ref
		}
	}
	for _, field := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
		ref, ok := byField[field]
		if !ok || ref.Value != "1.2.3" {
			t.Fatalf("%s @sneat/core ref = %+v, ok=%v", field, ref, ok)
		}
	}
	var lodash npmPackageJSONRef
	found := false
	for _, ref := range refs {
		if ref.Key == "lodash" {
			lodash, found = ref, true
		}
	}
	if !found || lodash.Value != "^4.17.21" || lodash.Field != "dependencies" {
		t.Fatalf("lodash ref = %+v, found=%v", lodash, found)
	}
	// "scripts" is not a dependency field and must never be scanned.
	for _, ref := range refs {
		if ref.Field == "scripts" || ref.Key == "build" {
			t.Fatalf("scripts.build was incorrectly treated as a dependency: %+v", ref)
		}
	}
}

// TestApplyNpmPackageJSONOverridePreservesFormatting mirrors the pnpm
// workspace test: the rewrite must touch only the changed version strings,
// leaving indentation, key quoting, trailing commas, and every untouched
// field exactly as Prettier wrote it.
func TestApplyNpmPackageJSONOverridePreservesFormatting(t *testing.T) {
	t.Parallel()
	updated, matched, err := applyNpmPackageJSONOverride([]byte(npmPackageJSONFixture), "@sneat/core", "1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 4 {
		t.Fatalf("matched = %+v, want all four dependency fields updated", matched)
	}
	result := string(updated)
	if strings.Count(result, `"@sneat/core": "1.3.0"`) != 4 {
		t.Fatalf("expected 4 updated @sneat/core references:\n%s", result)
	}
	if strings.Contains(result, "1.2.3") {
		t.Fatalf("old version still present:\n%s", result)
	}
	for _, untouched := range []string{
		`"name": "@sneat/example"`, `"version": "1.0.0"`, `"lodash": "^4.17.21"`,
		`"typescript": "^5.4.0"`, `"build": "tsc"`,
	} {
		if !strings.Contains(result, untouched) {
			t.Errorf("unrelated content changed; missing %q in:\n%s", untouched, result)
		}
	}
}

func TestApplyNpmPackageJSONOverrideIsNoOpWhenDependencyAbsent(t *testing.T) {
	t.Parallel()
	updated, matched, err := applyNpmPackageJSONOverride([]byte(npmPackageJSONFixture), "@sneat/does-not-exist", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 0 {
		t.Fatalf("matched = %+v, want none", matched)
	}
	if string(updated) != npmPackageJSONFixture {
		t.Fatalf("contents changed despite no match:\n%s", updated)
	}
}

func TestApplyNpmPackageJSONOverridePreservesTrailingCommaAbsence(t *testing.T) {
	t.Parallel()
	contents := []byte("{\n  \"dependencies\": {\n    \"@sneat/core\": \"1.2.3\"\n  }\n}\n")
	updated, matched, err := applyNpmPackageJSONOverride(contents, "@sneat/core", "1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("matched = %+v", matched)
	}
	if !strings.Contains(string(updated), "\"@sneat/core\": \"1.3.0\"\n  }\n") {
		t.Fatalf("last-entry rewrite must not add a trailing comma:\n%s", updated)
	}
}
