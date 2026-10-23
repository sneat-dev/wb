package policy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceLocateContinuesAfterAmbiguousFleetRoots(t *testing.T) {
	t.Parallel()
	source, err := ParseSource("acme/app//policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ambiguous := t.TempDir()
	for _, host := range []string{"github.com", "gitlab.com"} {
		if err := os.MkdirAll(filepath.Join(ambiguous, host, "acme", "app", ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	valid := t.TempDir()
	expected := filepath.Join(valid, "acme", "app", "policy.yaml")
	if err := os.MkdirAll(filepath.Dir(expected), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(expected, []byte("groups: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := source.Locate("", []string{ambiguous, blocked, valid}); err != nil || got != expected {
		t.Fatalf("Locate after failures = %q, %v; want %q", got, err, expected)
	}
	got, err := source.Locate("", []string{ambiguous, blocked})
	if err == nil || got != "" {
		t.Fatalf("ambiguous/unusable roots = %q, %v", got, err)
	}
	for _, want := range []string{ambiguous, blocked, "more than one host", "github.com, gitlab.com", "Clone acme/app"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Locate diagnostic %q does not contain %q", err, want)
		}
	}
}

func TestExpandBracesRejectsEmptyGroupAndInvalidSuffix(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"{}", "github.com/{acme,}/app", "{a,b}}", "{a,b}{"} {
		if got, err := expandBraces(raw); err == nil || got != nil {
			t.Errorf("expandBraces(%q) = %v, %v; want rejection", raw, got, err)
		}
	}
}

func TestScanModuleRecordsMalformedImportLiterals(t *testing.T) {
	t.Parallel()
	root := writeModule(t, map[string]string{
		"go.mod":     "module example.com/app\n\ngo 1.26\n",
		"missing.go": "package app\nimport\n",
		"escape.go":  "package app\nimport \"bad\\q\"\n",
		"valid.go":   "package app\nimport `example.com/library`\n",
	})
	module, err := ScanModule(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(module.Unparseable, ",") != "escape.go,missing.go" {
		t.Fatalf("unparseable files = %v", module.Unparseable)
	}
	if len(module.References) != 1 || module.References[0].Import != "example.com/library" || module.References[0].File != "valid.go" {
		t.Fatalf("references = %+v; want only the valid raw import literal", module.References)
	}
}

func TestScanModulePreservesAbsolutePathResolutionFailure(t *testing.T) {
	t.Parallel()
	failure := errors.New("working directory unavailable")
	calls := 0
	module, err := scanModuleWithAbs("relative/module", func(dir string) (string, error) {
		calls++
		if dir != "relative/module" {
			t.Fatalf("path resolution input = %q", dir)
		}
		return "", failure
	})
	if !errors.Is(err, failure) || calls != 1 || module.Path != "" || module.Dir != "" || len(module.References) != 0 || len(module.Unparseable) != 0 {
		t.Fatalf("failed path resolution = %+v, %v; calls=%d", module, err, calls)
	}
}
