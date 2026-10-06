package qualityrun

import (
	"github.com/sneat-dev/wb/internal/quality"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoverageBaselineWritesPerPackageUncoveredCounts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture.test/base\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(dir, "profile.out")
	profile := "mode: set\n" +
		"fixture.test/base/a.go:3.10,5.2 2 1\n" +
		"fixture.test/base/pkg/b.go:9.10,11.2 4 0\n"
	if err := os.WriteFile(profilePath, []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "baseline.json")

	err := Baseline(t.Context(), BaselineRequest{Profile: profilePath, Module: dir, SHA: "abc123", Out: outPath})
	if err != nil {
		t.Fatalf("baseline operation: %v", err)
	}
	baseline, err := quality.LoadBaseline(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.SHA != "abc123" {
		t.Fatalf("baseline.SHA = %q, want abc123", baseline.SHA)
	}
	if baseline.Packages["."] != 0 || baseline.Packages["pkg"] != 4 {
		t.Fatalf("baseline.Packages = %#v, want {.: 0, pkg: 4}", baseline.Packages)
	}
}

func TestCoverageBaselineRejectsMissingProfile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture.test/base\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Baseline(t.Context(), BaselineRequest{Profile: filepath.Join(dir, "missing.out"), Module: dir})
	if err == nil {
		t.Fatal("code = 0, want nonzero for a missing coverage profile")
	}
}

func TestCoverageBaselineRejectsMissingGoMod(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.out")
	if err := os.WriteFile(profilePath, []byte("mode: set\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Baseline(t.Context(), BaselineRequest{Profile: profilePath, Module: dir})
	if err == nil {
		t.Fatal("code = 0, want nonzero when --module has no go.mod")
	}
}
func TestDeadcodeBaselinePersistsAnalyzerIdentity(t *testing.T) {
	t.Parallel()
	const identity = "example.com/repo/pkg.Helper"
	path := filepath.Join(t.TempDir(), ".wb", "deadcode-baseline.txt")
	if err := quality.WriteDeadcodeBaseline(path, []quality.DeadcodeFinding{{Identity: identity}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), identity) {
		t.Fatalf("baseline bytes=%q error=%v", raw, err)
	}
}
