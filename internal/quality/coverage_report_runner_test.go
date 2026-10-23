package quality

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCoverageReportBindsOnlyTheInvokedCommandManifest(t *testing.T) {
	t.Parallel()
	for _, available := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable", true: "available"}[available], func(t *testing.T) {
			t.Parallel()
			module := nativeModule(t)
			directory := t.TempDir()
			stale := newCoverageDiagnosticsSink(directory, "example/repo", module)
			if err := stale.persist(0, goCoverageJob{label: "stale"}, goCoverageJobResult{output: "old", err: errors.New("old")}); err != nil {
				t.Fatal(err)
			}
			manifest := ""
			if available {
				manifest = filepath.Join(directory, "current-native.yaml")
				if err := os.WriteFile(manifest, []byte("current invocation"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			boom := errors.New("current command failed")
			calls := 0
			report := coverWithOptionsRun(context.Background(), "example/repo", module, RunOptions{CoverageDiagnosticsDir: directory}, func(_ context.Context, options RunOptions, gotModule, profile string) (string, int, error) {
				calls++
				if gotModule != module || profile == "" || options.CoverageDiagnosticsRepository != "example/repo" {
					t.Fatal(gotModule, profile, options)
				}
				return "current failure", 1, &coverageCommandError{cause: boom, manifestPath: manifest}
			})
			if calls != 1 || report.Status != StatusFailed {
				t.Fatal(calls, report)
			}
			if available {
				if report.Diagnostic == nil || report.Diagnostic.Manifest != manifest {
					t.Fatal(report)
				}
			} else if report.Diagnostic != nil {
				t.Fatal("stale manifest substituted", report)
			}
		})
	}
}
