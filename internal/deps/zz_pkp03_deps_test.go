package deps

import (
	"strings"
	"testing"
)

func TestPkp03ApplyNpmPackageJSONOverrideSkipsNonMatchingKeys(t *testing.T) {
	t.Parallel()
	contents := []byte("{\n  \"dependencies\": {\n    \"foo\": \"1.0.0\",\n    \"bar\": \"2.0.0\"\n  }\n}\n")
	out, matched, err := applyNpmPackageJSONOverride(contents, "bar", "1.2.3")
	if err != nil {
		t.Fatalf("applyNpmPackageJSONOverride: %v", err)
	}
	if len(matched) != 1 || matched[0].Key != "bar" {
		t.Fatalf("matched = %+v, want exactly one ref for bar", matched)
	}
	got := string(out)
	if !strings.Contains(got, `"bar": "1.2.3"`) {
		t.Fatalf("output missing updated bar version:\n%s", got)
	}
	if !strings.Contains(got, `"foo": "1.0.0"`) {
		t.Fatalf("output changed the non-matching foo entry:\n%s", got)
	}
}

func TestPkp03ApplyPnpmWorkspaceOverrideSkipsNonMatchingKeys(t *testing.T) {
	t.Parallel()
	contents := []byte("overrides:\n  foo: 1.0.0\n  bar: 2.0.0\n")
	out, matched, err := applyPnpmWorkspaceOverride(contents, "bar", "1.2.3")
	if err != nil {
		t.Fatalf("applyPnpmWorkspaceOverride: %v", err)
	}
	if len(matched) != 1 || matched[0].Key != "bar" {
		t.Fatalf("matched = %+v, want exactly one ref for bar", matched)
	}
	got := string(out)
	if !strings.Contains(got, "1.2.3") {
		t.Fatalf("output missing updated bar version:\n%s", got)
	}
	if !strings.Contains(got, "1.0.0") {
		t.Fatalf("output changed the non-matching foo entry:\n%s", got)
	}
	if strings.Contains(got, "2.0.0") {
		t.Fatalf("output still contains the old bar version:\n%s", got)
	}
}

func TestPkp03ScanPnpmWorkspaceRefsClosesNamedCatalogOnDedent(t *testing.T) {
	t.Parallel()
	contents := []byte("catalogs:\n  default:\n    foo: 1.0.0\n  other:\n    bar: 2.0.0\n")
	refs := scanPnpmWorkspaceRefs(contents)
	if len(refs) != 2 {
		t.Fatalf("refs = %+v, want 2", refs)
	}
	if refs[0].CatalogName != "default" || refs[0].Key != "foo" {
		t.Fatalf("refs[0] = %+v, want catalog default/foo", refs[0])
	}
	if refs[1].CatalogName != "other" || refs[1].Key != "bar" {
		t.Fatalf("refs[1] = %+v, want catalog other/bar", refs[1])
	}
}

// TestPkp03DependencySectionsPanicsOnUnknownField deliberately mutates the
// package-level canonical field list, so it must not run in parallel with
// other tests in this package.
//
//nolint:paralleltest // mutates the shared npmDependencyFieldNames package var; must stay serial
func TestPkp03DependencySectionsPanicsOnUnknownField(t *testing.T) {
	original := npmDependencyFieldNames
	npmDependencyFieldNames = []string{"unknownField"}
	defer func() { npmDependencyFieldNames = original }()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected dependencySections to panic on an unknown canonical field name")
		}
	}()
	manifest := npmPackageJSONManifest{}
	_ = manifest.dependencySections()
}
