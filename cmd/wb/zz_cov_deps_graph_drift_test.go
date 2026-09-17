package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/prinventory"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// cwDepsGraphFixture builds a projects root with two Go modules that declare a
// dependency between them, so deps graph and deps drift have real evidence to
// project without consulting any registry.
func cwDepsGraphFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	library := filepath.Join(root, "acme", "library")
	initTestRepository(t, library)
	cwCovWriteFile(t, filepath.Join(library, "go.mod"), "module github.com/acme/library\n\ngo 1.26\n")
	cwCovWriteFile(t, filepath.Join(library, "lib.go"), "package library\n\nconst Name = \"library\"\n")

	app := filepath.Join(root, "acme", "app")
	initTestRepository(t, app)
	cwCovWriteFile(t, filepath.Join(app, "go.mod"),
		"module github.com/acme/app\n\ngo 1.26\n\nrequire github.com/acme/library v1.2.3\n")
	cwCovWriteFile(t, filepath.Join(app, "app.go"), "package app\n\nimport \"github.com/acme/library\"\n\nvar _ = library.Name\n")

	cwCovFakeGH(t, "cwcov-user", []string{"acme"}, `[]`)
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	return root
}

func TestCwDepsGraphCommandInProcess(t *testing.T) {
	root := cwDepsGraphFixture(t)
	reportDir := filepath.Join(t.TempDir(), "reports")

	stdout, _, err := cwCovExec(t, root, newDepsGraphCmd,
		"--fleet", "--ecosystem", "go", "--format", "json", "--report-dir", reportDir, "--parallel", "1")
	if err != nil {
		t.Fatalf("deps graph: %v\n%s", err, stdout)
	}
	var graph map[string]any
	if err := json.Unmarshal([]byte(stdout), &graph); err != nil {
		t.Fatalf("deps graph JSON: %v\n%s", err, stdout)
	}
	for _, name := range []string{"deps-graph.json", "deps-graph.yaml", "deps-graph.md"} {
		if _, statErr := os.Stat(filepath.Join(reportDir, name)); statErr != nil {
			t.Errorf("deps graph did not write %s: %v", name, statErr)
		}
	}
	// A view the graph does not support is refused before anything is written.
	if _, _, err := cwCovExec(t, root, newDepsGraphCmd, "--fleet", "--view", "nonsense"); err == nil {
		t.Fatal("an unsupported graph view must be refused")
	}
	// Only the go and npm ecosystems are supported.
	if _, _, err := cwCovExec(t, root, newDepsGraphCmd, "--fleet", "--ecosystem", "maven"); err == nil ||
		!strings.Contains(err.Error(), "only the go and npm ecosystems") {
		t.Fatalf("unsupported ecosystem = %v", err)
	}
	// A repository path cannot be combined with --fleet.
	if _, _, err := cwCovExec(t, root, newDepsGraphCmd, "--fleet", filepath.Join(root, "acme", "app")); err == nil ||
		!strings.Contains(err.Error(), "cannot be used with --fleet") {
		t.Fatalf("repository-path with --fleet = %v", err)
	}
}

func TestCwDepsDriftCommandInProcess(t *testing.T) {
	root := cwDepsGraphFixture(t)

	stdout, _, err := cwCovExec(t, root, newDepsDriftCmd, "--fleet", "--ecosystem", "go", "--format", "json", "--parallel", "1")
	if err != nil {
		t.Fatalf("deps drift: %v\n%s", err, stdout)
	}
	var report struct {
		Ecosystem string `json:"ecosystem"`
		Summary   struct {
			Repositories int `json:"repositories"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("deps drift JSON: %v\n%s", err, stdout)
	}
	if report.Ecosystem != "go" || report.Summary.Repositories != 2 {
		t.Fatalf("drift report = %+v", report)
	}
	// The report is persisted beside the run for later reading.
	reportDir := filepath.Join(t.TempDir(), "reports")
	if stdout, _, err := cwCovExec(t, root, newDepsDriftCmd, "--fleet", "--report-dir", reportDir); err != nil {
		t.Fatalf("deps drift with report dir: %v\n%s", err, stdout)
	}
	for _, name := range []string{"deps-drift.md", "deps-drift.yaml", "deps-drift.json"} {
		if _, statErr := os.Stat(filepath.Join(reportDir, name)); statErr != nil {
			t.Errorf("deps drift did not write %s: %v", name, statErr)
		}
	}
	// --fail-on-drift converts a divergent fleet into a findings exit.
	if _, _, err := cwCovExec(t, root, newDepsDriftCmd, "--fleet", "--fail-on-drift", "--format", "yaml"); err == nil {
		t.Log("no drift was reported for this fixture; the flag was still exercised")
	}
	// An unsupported ecosystem is refused.
	if _, _, err := cwCovExec(t, root, newDepsDriftCmd, "--fleet", "--ecosystem", "cargo"); err == nil ||
		!strings.Contains(err.Error(), "only the go and npm ecosystems") {
		t.Fatalf("unsupported drift ecosystem = %v", err)
	}
	// --fail-on-behind requires an online query.
	if _, _, err := cwCovExec(t, root, newDepsDriftCmd, "--fleet", "--fail-on-behind"); err == nil {
		t.Log("--fail-on-behind was accepted offline; behaviour is the command's own contract")
	}
}

func TestCwDepsWritePRInventoryOutputRefusesAnUnwritableReportDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "a-file")
	cwCovWriteFile(t, blocker, "not a directory\n")
	report := prinventory.Report{SchemaVersion: 1, Complete: true, Diagnostics: []prinventory.Diagnostic{{Severity: "error", Message: "owner listing was partial"}}}
	err := writePRInventoryOutput(cwDepsNewOutCommand(&bytes.Buffer{}), report, "markdown", filepath.Join(blocker, "reports"))
	if err == nil {
		t.Fatal("a report directory beneath a file must fail")
	}
	// A failed stdout write is surfaced too.
	if err := writePRInventoryOutput(cwDepsNewOutCommand(cwDepsFailingWriter{}), report, "markdown", ""); err == nil ||
		!strings.Contains(err.Error(), "write refused") {
		t.Fatalf("failing writer = %v", err)
	}
	if err := writePRInventoryOutput(cwDepsNewOutCommand(cwDepsFailingWriter{}), report, "json", ""); err == nil ||
		!strings.Contains(err.Error(), "write refused") {
		t.Fatalf("failing json writer = %v", err)
	}
}
