package streams

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A "name" field of the wrong JSON type is valid, readable JSON — so it
// passes npmPackageManifests' own lenient internal read (which only looks
// for a "workspaces" field via json.RawMessage and never inspects "name" at
// all) — but fails the stricter struct{Name string; Private bool} target
// both DiscoverPublished and collectNpmPackageNames decode into. That
// isolates their own json.Unmarshal failure branch from
// npmPackageManifests' already-covered one, without needing the manifest to
// be unreadable (which npmPackageManifests would always catch first, since
// it reads every manifest it lists before either caller gets a chance to).
const npmManifestWithWrongNameType = `{"name": 123}`

func TestDiscoverPublishedSurfacesAMalformedNpmManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	libDir := filepath.Join(root, "libs", "foo")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "package.json"), []byte(npmManifestWithWrongNameType), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := DiscoverPublished(root)
	if err == nil {
		t.Fatal("DiscoverPublished with a malformed manifest = nil error, want one")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Fatalf("DiscoverPublished error = %v, want it to name the parse failure", err)
	}
}

func TestCollectNpmPackageNamesSurfacesAMalformedNpmManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	libDir := filepath.Join(root, "libs", "foo")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "package.json"), []byte(npmManifestWithWrongNameType), 0o600); err != nil {
		t.Fatal(err)
	}
	names, finding := collectNpmPackageNames(PreflightInput{Repository: "acme/app", Path: root})
	if names != nil {
		t.Errorf("collectNpmPackageNames names = %v on a parse failure, want nil", names)
	}
	if finding.Status != PreflightUnknown {
		t.Fatalf("collectNpmPackageNames finding.Status = %v, want PreflightUnknown", finding.Status)
	}
	if !strings.Contains(finding.Detail, "parse") {
		t.Fatalf("collectNpmPackageNames finding.Detail = %q, want it to name the parse failure", finding.Detail)
	}
}
