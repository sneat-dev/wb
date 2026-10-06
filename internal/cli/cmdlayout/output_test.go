package cmdlayout

import (
	"bytes"
	"github.com/sneat-dev/wb/internal/layout"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLayoutOutputAndReportFiles(t *testing.T) {
	t.Parallel()
	command := newAuditCmd(testRuntime(func() string { return "fixture" }), testDependencies())
	var out bytes.Buffer
	command.SetOut(&out)
	if err := writeLayoutOutput(command, "markdown", "# report\n", map[string]int{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "# report") {
		t.Errorf("markdown output = %q", out.String())
	}
	out.Reset()
	if err := writeLayoutOutput(command, "yaml", "", map[string]int{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "x: 1") {
		t.Errorf("yaml output = %q", out.String())
	}
	out.Reset()
	if err := writeLayoutOutput(command, "json", "", map[string]int{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"x": 1`) {
		t.Errorf("json output = %q", out.String())
	}
	if err := writeLayoutOutput(command, "toml", "", nil); err == nil ||
		!strings.Contains(err.Error(), `unknown --format "toml"`) {
		t.Fatalf("unknown format error = %v", err)
	}

	auditDir := filepath.Join(t.TempDir(), "audit")
	if err := writeReports(auditDir, "audit", cwCovLayoutReportFixture().Markdown(), cwCovLayoutReportFixture()); err != nil {
		t.Fatal(err)
	}
	cleanDir := filepath.Join(t.TempDir(), "clean")
	if err := writeReports(cleanDir, "clean", cwCovCleanReportFixture().Markdown(), cwCovCleanReportFixture()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"layout-audit.md", "layout-audit.yaml", "layout-audit.json"} {
		data, err := os.ReadFile(filepath.Join(auditDir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			t.Errorf("%s is empty", name)
		}
	}
	for _, name := range []string{"layout-clean.md", "layout-clean.yaml", "layout-clean.json"} {
		if _, err := os.Stat(filepath.Join(cleanDir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A report directory that cannot be created is an error, not a panic.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeReports(filepath.Join(blocked, "child"), "audit", cwCovLayoutReportFixture().Markdown(), cwCovLayoutReportFixture()); err == nil {
		t.Error("writeLayoutAuditReports accepted a path under a regular file")
	}
	if err := writeReports(filepath.Join(blocked, "child"), "clean", cwCovCleanReportFixture().Markdown(), cwCovCleanReportFixture()); err == nil {
		t.Error("writeLayoutCleanReports accepted a path under a regular file")
	}
}

func cwCovLayoutReportFixture() layout.Report {
	return layout.Report{
		SchemaVersion: 1,
		ProjectsRoot:  "/tmp/projects",
		Summary:       layout.Summary{Inspected: 1, TopLevel: 1},
		Findings: []layout.Finding{{
			Path: "/tmp/projects/stray", Kind: layout.KindTopLevel,
			OriginSlug: "acme/stray", Reason: "checkout sits directly under the projects root",
		}},
	}
}

func cwCovCleanReportFixture() layout.CleanReport {
	return layout.CleanReport{
		SchemaVersion: 1,
		ProjectsRoot:  "/tmp/projects",
		DryRun:        true,
		Actions: []layout.CleanAction{{
			Path: "/tmp/projects/stray", OriginSlug: "acme/stray",
			Status: "planned", Reason: "replaceable by the canonical clone",
		}},
	}
}
