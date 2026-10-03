package cmdlayout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/layout"
)

func TestLayoutMigratePublishesReportFilesAndSelectedOutput(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"markdown", "yaml", "json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies()
			report := layout.MigrateReport{SchemaVersion: 1, DryRun: true, Clones: []layout.MigrateClone{{Repository: "acme/app", Status: "planned", Source: "/legacy/app", Destination: "/canonical/app"}}}
			var options layout.MigrateOptions
			deps.Migrate = func(_ context.Context, _ string, requested layout.MigrateOptions) (layout.MigrateReport, error) {
				options = requested
				return report, nil
			}
			root := t.TempDir()
			reportDir := filepath.Join(root, "reports")
			command := newMigrateCmd(func() string { return root }, deps)
			var stdout bytes.Buffer
			command.SetOut(&stdout)
			command.SetArgs([]string{"acme/app", "--clones-only", "--include-task=task-one", "--format=" + format, "--report-dir=" + reportDir})
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if !options.ClonesOnly || len(options.Repositories) != 1 || options.Repositories[0] != "acme/app" || len(options.IncludeTasks) != 1 || options.IncludeTasks[0] != "task-one" {
				t.Fatalf("migrate options = %+v", options)
			}
			for _, name := range []string{"layout-migrate.md", "layout-migrate.yaml", "layout-migrate.json"} {
				if raw, err := os.ReadFile(filepath.Join(reportDir, name)); err != nil || len(raw) == 0 {
					t.Errorf("report %s: %q, %v", name, raw, err)
				}
			}
			if format == "json" {
				var decoded layout.MigrateReport
				if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
					t.Fatal(err)
				}
				if len(decoded.Clones) != 1 || decoded.Clones[0].Status != "planned" {
					t.Fatalf("JSON = %+v", decoded)
				}
			} else if !strings.Contains(stdout.String(), "acme/app") {
				t.Fatalf("%s output = %q", format, stdout.String())
			}
		})
	}
}

func TestLayoutMigratePreservesPartialReportOnFailure(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	want := errors.New("manifest write failed after first move")
	deps.Migrate = func(context.Context, string, layout.MigrateOptions) (layout.MigrateReport, error) {
		return layout.MigrateReport{SchemaVersion: 1, Clones: []layout.MigrateClone{{Repository: "acme/app", Status: "done"}}}, want
	}
	command := newMigrateCmd(func() string { return "fixture-root" }, deps)
	command.SilenceUsage, command.SilenceErrors = true, true
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"--format=json"})
	if err := command.Execute(); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	var decoded layout.MigrateReport
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("partial output = %q: %v", stdout.String(), err)
	}
	if len(decoded.Clones) != 1 || decoded.Clones[0].Repository != "acme/app" {
		t.Fatalf("partial report = %+v", decoded)
	}
}

func TestLayoutMigrateClassifiesInvalidInclusionAsUsage(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	for _, failure := range []error{&layout.UnknownIncludeTaskError{Task: "missing"}, &layout.UndoIncludeFlagsError{}} {
		deps.Migrate = func(context.Context, string, layout.MigrateOptions) (layout.MigrateReport, error) {
			return layout.MigrateReport{}, failure
		}
		command := newMigrateCmd(func() string { return "fixture-root" }, deps)
		command.SilenceUsage, command.SilenceErrors = true, true
		var stdout bytes.Buffer
		command.SetOut(&stdout)
		command.SetArgs(nil)
		var exit *codedError
		if err := command.Execute(); !errors.As(err, &exit) || exit.code != 2 || !strings.Contains(exit.Error(), failure.Error()) {
			t.Fatalf("%T error = %v", failure, exit)
		}
		if stdout.Len() != 0 {
			t.Fatalf("usage refusal wrote partial report: %q", stdout.String())
		}
	}
}

func TestLayoutMigrateReportsSkippedCloneAsFindings(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	deps.Migrate = func(context.Context, string, layout.MigrateOptions) (layout.MigrateReport, error) {
		return layout.MigrateReport{SchemaVersion: 1, Clones: []layout.MigrateClone{{Repository: "acme/app", Status: "skipped", Reason: "active claim"}}}, nil
	}
	command := newMigrateCmd(func() string { return "fixture-root" }, deps)
	command.SilenceUsage, command.SilenceErrors = true, true
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"--format=json"})
	var exit *codedError
	if err := command.Execute(); !errors.As(err, &exit) || exit.code != 1 {
		t.Fatalf("error = %v", exit)
	}
	if !strings.Contains(stdout.String(), `"active claim"`) {
		t.Fatalf("finding output = %q", stdout.String())
	}
}
