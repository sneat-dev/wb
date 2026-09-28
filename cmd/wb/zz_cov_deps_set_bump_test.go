package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

// cwDepsSetFixture builds a projects root whose modules are real clones with
// real origins, because the deps lifecycle fetches before it plans.
func cwDepsSetFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	seeds := t.TempDir()

	library := filepath.Join(root, "acme", "library")
	cwCovCloneWithOrigin(t, seeds, "library", library)
	cwCovWriteFile(t, filepath.Join(library, "go.mod"), "module github.com/acme/library\n\ngo 1.26\n")
	cwCovWriteFile(t, filepath.Join(library, "lib.go"), "package library\n\nconst Name = \"library\"\n")
	runGit(t, library, "add", ".")
	runGit(t, library, "commit", "-m", "add module")
	runGit(t, library, "push", "origin", "main")

	app := filepath.Join(root, "acme", "app")
	cwCovCloneWithOrigin(t, seeds, "app", app)
	cwCovWriteFile(t, filepath.Join(app, "go.mod"),
		"module github.com/acme/app\n\ngo 1.26\n\nrequire github.com/acme/library v1.2.3\n")
	cwCovWriteFile(t, filepath.Join(app, "app.go"), "package app\n\nimport \"github.com/acme/library\"\n\nvar _ = library.Name\n")
	runGit(t, app, "add", ".")
	runGit(t, app, "commit", "-m", "require library")
	runGit(t, app, "push", "origin", "main")

	cwCovFakeGH(t, "acme", nil,
		`[{"name":"app","isArchived":false,"isFork":false,"sshUrl":"git@example.test:acme/app.git"},`+
			`{"name":"library","isArchived":false,"isFork":false,"sshUrl":"git@example.test:acme/library.git"}]`)
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	return root
}

func TestCwDepsSetCommandInProcessDryRun(t *testing.T) {
	root := cwDepsSetFixture(t)
	app := filepath.Join(root, "acme", "app")

	// A dry run plans against the repository's real manifest and reports the
	// exact decision without changing one tracked file.
	beforeRaw, err := os.ReadFile(filepath.Join(app, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	before := string(beforeRaw)
	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsSetCmd(&invocation{projectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", app, "--dry-run", "--format", "json", "--parallel", "1")
	if err != nil {
		t.Fatalf("deps set --dry-run: %v\n%s", err, stdout)
	}
	var report struct {
		Target       struct{ Dependency, Version string } `json:"target"`
		Repositories []struct {
			Repository string `json:"repository"`
			Status     string `json:"status"`
			Decisions  []struct {
				File      string `json:"file"`
				Action    string `json:"action"`
				AfterRef  string `json:"after_ref"`
				BeforeRef string `json:"before_ref"`
			} `json:"decisions"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("deps set JSON: %v\n%s", err, stdout)
	}
	if report.Target.Dependency != "github.com/acme/library" || report.Target.Version != "v1.2.4" {
		t.Fatalf("target = %+v", report.Target)
	}
	if len(report.Repositories) != 1 || report.Repositories[0].Repository != "acme/app" {
		t.Fatalf("repositories = %+v", report.Repositories)
	}
	afterRaw, err := os.ReadFile(filepath.Join(app, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	after := string(afterRaw)
	if before != after {
		t.Errorf("a dry run changed go.mod:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	// The report directory receives the persisted plan.
	reportDir := filepath.Join(t.TempDir(), "reports")
	if stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsSetCmd(&invocation{projectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", app, "--dry-run", "--report-dir", reportDir); err != nil {
		t.Fatalf("deps set with report dir: %v\n%s", err, stdout)
	}
	// A non-fleet target that does not match the filters is refused.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsSetCmd(&invocation{projectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", app, "--dry-run", "--match", "other/*"); err == nil ||
		!strings.Contains(err.Error(), "does not match selected filters") {
		t.Fatalf("filtered target = %v", err)
	}
	// --layer without --dependency-order is a usage error.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsSetCmd(&invocation{projectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", app, "--layer", "1"); err == nil ||
		!strings.Contains(err.Error(), "--layer requires --dependency-order") {
		t.Fatalf("--layer alone = %v", err)
	}
	// --dependency-order is go-only.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsSetCmd(&invocation{projectsRoot: root}) },
		"npm", "@acme/lib@1.2.4", app, "--dependency-order"); err == nil ||
		!strings.Contains(err.Error(), "supported only for the go ecosystem") {
		t.Fatalf("npm --dependency-order = %v", err)
	}
	// --propagate requires --fleet.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsSetCmd(&invocation{projectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", app, "--propagate"); err == nil ||
		!strings.Contains(err.Error(), "--propagate requires --fleet") {
		t.Fatalf("--propagate without --fleet = %v", err)
	}
	// A repository path cannot be combined with --fleet.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsSetCmd(&invocation{projectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", app, "--fleet"); err == nil ||
		!strings.Contains(err.Error(), "cannot be used with --fleet") {
		t.Fatalf("path with --fleet = %v", err)
	}
	// A malformed target is refused.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsSetCmd(&invocation{projectsRoot: root}) }, "go", "not-a-target", app, "--dry-run"); err == nil {
		t.Fatal("a malformed target must be refused")
	}
	// An unknown output format is refused.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsSetCmd(&invocation{projectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", app, "--dry-run", "--format", "toml"); err == nil ||
		!strings.Contains(err.Error(), "unknown --format") {
		t.Fatalf("unknown format = %v", err)
	}
}

func TestCwDepsBumpCommandInProcessDryRun(t *testing.T) {
	root := cwDepsSetFixture(t)

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsBumpCmd(&invocation{projectsRoot: root}) },
		"go", "--fleet", "--changed", "github.com/acme/library@v1.2.4", "--dry-run", "--format", "json", "--parallel", "1")
	if err != nil {
		t.Fatalf("deps bump --dry-run: %v\n%s", err, stdout)
	}
	var report struct {
		Operation string `json:"operation"`
		Status    string `json:"status"`
		Ecosystem string `json:"ecosystem"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("deps bump JSON: %v\n%s", err, stdout)
	}
	if report.Operation == "" || report.Ecosystem != "go" {
		t.Fatalf("bump report = %+v", report)
	}
	// The seed events are required.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsBumpCmd(&invocation{projectsRoot: root}) }, "go", "--fleet", "--dry-run"); err == nil ||
		!strings.Contains(err.Error(), "at least one --changed") {
		t.Fatalf("missing seed events = %v", err)
	}
	// deps bump requires --fleet.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsBumpCmd(&invocation{projectsRoot: root}) },
		"go", "--changed", "github.com/acme/library@v1.2.4", "--dry-run"); err == nil ||
		!strings.Contains(err.Error(), "deps bump requires --fleet") {
		t.Fatalf("without --fleet = %v", err)
	}
	// Only the go and npm ecosystems are supported.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsBumpCmd(&invocation{projectsRoot: root}) },
		"cargo", "--fleet", "--changed", "x@1.0.0", "--dry-run"); err == nil ||
		!strings.Contains(err.Error(), "only the go and npm ecosystems") {
		t.Fatalf("unsupported ecosystem = %v", err)
	}
	// --scope without --latest is refused rather than silently ignored.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsBumpCmd(&invocation{projectsRoot: root}) },
		"go", "--fleet", "--changed", "github.com/acme/library@v1.2.4", "--dry-run", "--scope", "github.com/acme/*"); err == nil ||
		!strings.Contains(err.Error(), "--scope selects which published modules --latest derives") {
		t.Fatalf("--scope without --latest = %v", err)
	}
	// --latest without a usable --scope is refused.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsBumpCmd(&invocation{projectsRoot: root}) },
		"go", "--fleet", "--dry-run", "--latest"); err == nil ||
		!strings.Contains(err.Error(), "--latest derives release events for the modules --scope selects") {
		t.Fatalf("--latest without --scope = %v", err)
	}
}

// TestCwDepsSetPropagateFleetDelegatesToDepsBump proves that "deps set
// --propagate --fleet" builds a single exact_set release event from the set
// target and hands it to the same wave engine as "deps bump", rather than
// applying the exact-version edit directly.
func TestCwDepsSetPropagateFleetDelegatesToDepsBump(t *testing.T) {
	root := cwDepsSetFixture(t)

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsSetCmd(&invocation{projectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", "--fleet", "--propagate", "--dry-run", "--format", "json", "--parallel", "1")
	if err != nil {
		t.Fatalf("deps set --propagate --fleet: %v\n%s", err, stdout)
	}
	var report struct {
		Operation  string `json:"operation"`
		Ecosystem  string `json:"ecosystem"`
		SeedEvents []struct {
			Dependency string `json:"dependency"`
			Version    string `json:"version"`
			Source     string `json:"source"`
		} `json:"seed_events"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("deps set --propagate JSON: %v\n%s", err, stdout)
	}
	if report.Operation == "" || report.Ecosystem != "go" {
		t.Fatalf("propagate report = %+v", report)
	}
	if len(report.SeedEvents) != 1 || report.SeedEvents[0].Dependency != "github.com/acme/library" ||
		report.SeedEvents[0].Version != "v1.2.4" || report.SeedEvents[0].Source != "exact_set" {
		t.Fatalf("propagate seed events = %+v", report.SeedEvents)
	}
}
