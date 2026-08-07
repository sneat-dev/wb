package deps

import (
	"strings"
	"testing"
)

const pnpmWorkspaceFixture = `packages:
  - "packages/*"
  - "apps/*"

overrides:
  "@sneat/core": "1.2.3" # pinned for the sneat-apps release train
  '@sneat/models': ^1.0.0

catalog:
  react: ^18.2.0

catalogs:
  react17:
    react: ^17.0.2
    react-dom: ^17.0.2
`

func TestScanPnpmWorkspaceRefsFindsOverridesCatalogAndCatalogs(t *testing.T) {
	t.Parallel()
	refs := scanPnpmWorkspaceRefs([]byte(pnpmWorkspaceFixture))
	byKey := map[string]pnpmWorkspaceRef{}
	for _, ref := range refs {
		byKey[ref.Section+":"+ref.CatalogName+":"+ref.Key] = ref
	}
	core, ok := byKey["overrides::@sneat/core"]
	if !ok || core.Value != "1.2.3" || core.quote != '"' || core.comment != "# pinned for the sneat-apps release train" {
		t.Fatalf("overrides @sneat/core ref = %+v, ok=%v", core, ok)
	}
	models, ok := byKey["overrides::@sneat/models"]
	if !ok || models.Value != "^1.0.0" {
		t.Fatalf("overrides @sneat/models ref = %+v, ok=%v", models, ok)
	}
	catalogReact, ok := byKey["catalog::react"]
	if !ok || catalogReact.Value != "^18.2.0" {
		t.Fatalf("catalog react ref = %+v, ok=%v", catalogReact, ok)
	}
	react17, ok := byKey["catalogs:react17:react"]
	if !ok || react17.Value != "^17.0.2" {
		t.Fatalf("catalogs.react17.react ref = %+v, ok=%v", react17, ok)
	}
	reactDom17, ok := byKey["catalogs:react17:react-dom"]
	if !ok || reactDom17.Value != "^17.0.2" {
		t.Fatalf("catalogs.react17.react-dom ref = %+v, ok=%v", reactDom17, ok)
	}
	if len(refs) != 5 {
		t.Fatalf("refs = %+v, want exactly 5", refs)
	}
}

// TestApplyPnpmWorkspaceOverridePreservesFormatting is the pnpm-workspace.yaml
// overrides case the task explicitly asked for: pnpm 11 no longer reads the
// legacy pnpm.overrides field in package.json, so a bump that updates
// package.json but misses this file is silently ineffective. The rewrite
// must touch only the version characters that changed, not reformat quoting,
// comments, or unrelated sections (packages:, catalog:, catalogs:).
func TestApplyPnpmWorkspaceOverridePreservesFormatting(t *testing.T) {
	t.Parallel()
	updated, matched, err := applyPnpmWorkspaceOverride([]byte(pnpmWorkspaceFixture), "@sneat/core", "1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 || matched[0].Value != "1.2.3" {
		t.Fatalf("matched = %+v", matched)
	}
	result := string(updated)
	if !strings.Contains(result, `"@sneat/core": "1.3.0" # pinned for the sneat-apps release train`) {
		t.Fatalf("override was not rewritten in place:\n%s", result)
	}
	// Everything else must survive byte-for-byte.
	for _, untouched := range []string{
		`packages:`, `  - "packages/*"`, `  - "apps/*"`,
		`'@sneat/models': ^1.0.0`, `  react: ^18.2.0`, `    react: ^17.0.2`, `    react-dom: ^17.0.2`,
	} {
		if !strings.Contains(result, untouched) {
			t.Errorf("unrelated content changed; missing %q in:\n%s", untouched, result)
		}
	}
}

func TestApplyPnpmWorkspaceOverrideUpdatesCatalogsEntry(t *testing.T) {
	t.Parallel()
	updated, matched, err := applyPnpmWorkspaceOverride([]byte(pnpmWorkspaceFixture), "react", "18.3.0")
	if err != nil {
		t.Fatal(err)
	}
	// "react" appears in both catalog: and catalogs.react17: — every
	// occurrence must be updated, since the caller does not disambiguate by
	// catalog name.
	if len(matched) != 2 {
		t.Fatalf("matched = %+v, want catalog and catalogs.react17 both updated", matched)
	}
	result := string(updated)
	if !strings.Contains(result, "  react: 18.3.0\n") || !strings.Contains(result, "    react: 18.3.0\n") {
		t.Fatalf("react was not updated in both catalog sections:\n%s", result)
	}
	if !strings.Contains(result, "    react-dom: ^17.0.2\n") {
		t.Fatalf("react-dom must be untouched:\n%s", result)
	}
}

func TestApplyPnpmWorkspaceOverrideIsNoOpWhenDependencyAbsent(t *testing.T) {
	t.Parallel()
	updated, matched, err := applyPnpmWorkspaceOverride([]byte(pnpmWorkspaceFixture), "@sneat/does-not-exist", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 0 {
		t.Fatalf("matched = %+v, want none", matched)
	}
	if string(updated) != pnpmWorkspaceFixture {
		t.Fatalf("contents changed despite no match:\n%s", updated)
	}
}

func TestScanPnpmWorkspaceRefsIgnoresSectionsOutsideOverridesAndCatalogs(t *testing.T) {
	t.Parallel()
	contents := []byte(`packages:
  - "packages/*"

peerDependencyRules:
  allowedVersions:
    react: 18
`)
	refs := scanPnpmWorkspaceRefs(contents)
	if len(refs) != 0 {
		t.Fatalf("refs = %+v, want none outside overrides/catalog/catalogs", refs)
	}
}
