package streams

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, contents := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// REQ: local-link-discovers-what-the-library-publishes — the Go module path
// comes from backend/go.mod when the repository has one, and npm package
// names from libs/**/package.json.
func TestDiscoverPublishedPrefersBackendGoModAndReadsLibsPackages(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod":                     "module github.com/acme/library/tools\n",
		"backend/go.mod":             "module github.com/acme/library/backend\n",
		"libs/core/package.json":     `{"name":"@acme/core","version":"1.0.0"}`,
		"libs/internal/package.json": `{"name":"@acme/internal","private":true}`,
		"libs/unnamed/package.json":  `{"version":"1.0.0"}`,
		"package.json":               `{"name":"workspace-root","private":true}`,
	})
	identities, err := DiscoverPublished(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, identity := range identities {
		names = append(names, string(identity.Ecosystem)+" "+identity.Name)
	}
	want := []string{"go github.com/acme/library/backend", "npm @acme/core"}
	if len(names) != len(want) {
		t.Fatalf("identities = %v, want %v", names, want)
	}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("identities = %v, want %v", names, want)
		}
	}
}

func TestDiscoverPublishedFallsBackToTheModuleRoot(t *testing.T) {
	root := writeTree(t, map[string]string{"go.mod": "module github.com/acme/library\n"})
	identities, err := DiscoverPublished(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 || identities[0].Name != "github.com/acme/library" || identities[0].Directory != "." {
		t.Fatalf("identities = %#v, want the root module", identities)
	}
}

// REQ: go-consumers-link-through-an-untracked-go-work — every module in the
// consumer worktree is enumerated, including nested tooling modules, because
// a workspace holding only the library leaves the consumer's own module out.
func TestGoModulesEnumeratesEveryModuleInTheWorktree(t *testing.T) {
	root := writeTree(t, map[string]string{
		"backend/go.mod":            "module github.com/acme/app/backend\n",
		"tools/lint/go.mod":         "module github.com/acme/app/tools/lint\n",
		"node_modules/x/go.mod":     "module vendored/should/not/appear\n",
		"vendor/y/go.mod":           "module vendored/should/not/appear/either\n",
		"backend/testdata/z/go.mod": "module fixture/should/not/appear\n",
	})
	modules, err := GoModules(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 2 {
		t.Fatalf("modules = %#v, want backend and tools/lint only", modules)
	}
	if modules[0].Directory != "backend" || modules[1].Directory != "tools/lint" {
		t.Fatalf("modules = %#v, want backend then tools/lint", modules)
	}
}

// REQ: link-discovery-uses-the-canonical-dependency-sections — a declaration
// is found in every canonical section, not only `dependencies`.
func TestDiscoverDeclarationsReadsTheCanonicalSections(t *testing.T) {
	library := writeTree(t, map[string]string{
		"backend/go.mod":         "module github.com/acme/library/backend\n",
		"libs/core/package.json": `{"name":"@acme/core","version":"1.0.0"}`,
	})
	identities, err := DiscoverPublished(library)
	if err != nil {
		t.Fatal(err)
	}
	consumer := writeTree(t, map[string]string{
		"backend/go.mod": "module github.com/acme/app/backend\n\nrequire (\n\tgithub.com/acme/library/backend v0.4.0 // indirect\n)\n",
		"package.json":   `{"name":"app","peerDependencies":{"@acme/core":"^1.0.0"}}`,
	})
	declarations, err := DiscoverDeclarations(consumer, identities)
	if err != nil {
		t.Fatal(err)
	}
	if len(declarations) != 2 {
		t.Fatalf("declarations = %#v, want the Go require and the peer dependency", declarations)
	}
	sections := map[string]string{}
	for _, declaration := range declarations {
		sections[declaration.Identity.Name] = declaration.Section
	}
	if sections["github.com/acme/library/backend"] != "require" {
		t.Errorf("Go declaration section = %q", sections["github.com/acme/library/backend"])
	}
	if sections["@acme/core"] != "peerDependencies" {
		t.Errorf("npm declaration section = %q, want peerDependencies", sections["@acme/core"])
	}
}

func TestDiscoverDeclarationsReportsNothingForAnUnrelatedConsumer(t *testing.T) {
	library := writeTree(t, map[string]string{"backend/go.mod": "module github.com/acme/library/backend\n"})
	identities, err := DiscoverPublished(library)
	if err != nil {
		t.Fatal(err)
	}
	consumer := writeTree(t, map[string]string{
		"backend/go.mod": "module github.com/acme/other/backend\n\nrequire github.com/elsewhere/thing v1.0.0\n",
	})
	declarations, err := DiscoverDeclarations(consumer, identities)
	if err != nil {
		t.Fatal(err)
	}
	if len(declarations) != 0 {
		t.Fatalf("declarations = %#v, want none for a consumer that does not depend on the library", declarations)
	}
}

func TestGoRequirementsReadsBothSpellings(t *testing.T) {
	requirements := goRequirements([]byte(
		"module m\n\ngo 1.27\n\nrequire github.com/one/single v1.0.0\n\nrequire (\n\tgithub.com/two/block v2.1.0\n\tgithub.com/three/block v3.0.0 // indirect\n)\n",
	))
	for name, version := range map[string]string{
		"github.com/one/single":  "v1.0.0",
		"github.com/two/block":   "v2.1.0",
		"github.com/three/block": "v3.0.0",
	} {
		if requirements[name] != version {
			t.Errorf("requirement %s = %q, want %q", name, requirements[name], version)
		}
	}
	if _, ok := requirements["go"]; ok {
		t.Error("the go directive was read as a requirement")
	}
}
