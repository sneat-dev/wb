package migraterun

import (
	"github.com/sneat-dev/wb/internal/migrate"
	"github.com/sneat-dev/wb/internal/testenv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const migrationSpec = `
format = "https://sneat.dev/workbench/formats/migration/v1"

migration "cw-cov-migration" {
  title = "Coverage migration"

  scope {
    languages = ["go"]
  }

  text_replace "go" {
    from = "OLDNAME"
    to   = "NEWNAME"
  }
}
`

// hierarchicalSpec names its own module through an import step:
// a campaign refuses a migration that references no Go module at all.
const hierarchicalSpec = `
format = "https://sneat.dev/workbench/formats/migration/v1"

migration "cw-cov-migration" {
  title = "Coverage migration"

  scope {
    languages = ["go"]
  }

  import_replace "go" {
    from = "github.com/acme/sample/old"
    to   = "github.com/acme/sample/new"
  }
}
`

func migrationSource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/acme/sample\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "value.go"), []byte("package sample\n\nvar OLDNAME = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeSpec(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "migration.hcl")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestLocalRealPlanAndApplyArtifacts(t *testing.T) {
	t.Parallel()
	ops := DefaultOperations()
	spec := writeSpec(t, migrationSpec)
	root := migrationSource(t)
	// This parent-owned immutable source is shared only by read-only plan children.
	t.Run("immutable plans", func(t *testing.T) {
		for _, name := range []string{"plan", "check"} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				result, err := ops.Local(t.Context(), LocalRequest{SpecPath: spec, Roots: []string{root}})
				if err != nil || !result.HasChanges || result.Report.Status != "planned" {
					t.Fatal(result, err)
				}
				body, err := os.ReadFile(filepath.Join(root, "value.go"))
				if err != nil || !strings.Contains(string(body), "OLDNAME") {
					t.Fatalf("dry run changed source: %s %v", body, err)
				}
			})
		}
	})
	planned := filepath.Join(t.TempDir(), "planned")
	result, err := ops.Local(t.Context(), LocalRequest{SpecPath: spec, Roots: []string{root}, ReportDir: planned})
	if err != nil || !result.HasChanges {
		t.Fatal(result, err)
	}
	if _, err := os.Stat(filepath.Join(planned, "migration.md")); err != nil {
		t.Fatal("dry run did not write report", err)
	}
	applied := filepath.Join(t.TempDir(), "applied")
	result, err = ops.Local(t.Context(), LocalRequest{SpecPath: spec, Roots: []string{root}, Apply: true, ReportDir: applied})
	if err != nil || result.Report.Status != "applied" {
		t.Fatal(result, err)
	}
	body, err := os.ReadFile(filepath.Join(root, "value.go"))
	if err != nil || !strings.Contains(string(body), "NEWNAME") || strings.Contains(string(body), "OLDNAME") {
		t.Fatalf("apply did not rewrite source: %s %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(applied, "migration.md")); err != nil {
		t.Fatal("apply did not write report", err)
	}
	for _, req := range []LocalRequest{{SpecPath: filepath.Join(t.TempDir(), "absent.hcl"), Roots: []string{root}}, {SpecPath: spec, Roots: []string{filepath.Join(t.TempDir(), "absent")}}} {
		if _, err := ops.Local(t.Context(), req); err == nil {
			t.Fatal("missing input accepted", req)
		}
	}
}
func TestCampaignRealIsolatedWorktreeAndCleanup(t *testing.T) {
	t.Parallel()
	ops := DefaultOperations()
	spec := writeSpec(t, hierarchicalSpec)
	root := migrationSource(t)
	canonical := t.TempDir()
	testenv.CloneWithOrigin(t, t.TempDir(), "sample", filepath.Join(canonical, "acme", "sample"))
	reports := filepath.Join(t.TempDir(), "campaign")
	result, err := ops.Campaign(t.Context(), CampaignRequest{SpecPath: spec, Roots: []string{root}, GitHubDir: canonical, Ref: "main", Verify: migrate.VerifyNone, Parallel: 1, ReportDir: reports})
	if err != nil || result.RunError != nil {
		t.Fatal(result, err)
	}
	for _, name := range []string{"campaign.md", "campaign.yaml"} {
		if _, err := os.Stat(filepath.Join(reports, name)); err != nil {
			t.Errorf("campaign report %s missing: %v", name, err)
		}
	}
	empty := t.TempDir()
	result, err = ops.Campaign(t.Context(), CampaignRequest{SpecPath: spec, Cleanup: true, GitHubDir: empty})
	if err != nil || !result.Cleanup || len(result.Removed) != 0 {
		t.Fatal(result, err)
	}
	if _, err := ops.Campaign(t.Context(), CampaignRequest{SpecPath: filepath.Join(t.TempDir(), "absent.hcl"), Cleanup: true, GitHubDir: empty}); err == nil {
		t.Fatal("cleanup accepted absent spec")
	}
	for _, req := range []CampaignRequest{{SpecPath: spec, Cleanup: true, GitHubDir: empty, Roots: []string{root}}, {SpecPath: spec, GitHubDir: empty}} {
		if _, err := ops.Campaign(t.Context(), req); err == nil {
			t.Fatal("invalid root count accepted", req)
		}
	}
}
