package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/layout"
)

// TestWriteLayoutMigrateReportsWritesAllThreeFormats drives
// writeLayoutMigrateReports end to end: it must create the report directory
// and write markdown, yaml, and json siblings that round-trip the report.
func TestWriteLayoutMigrateReportsWritesAllThreeFormats(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "reports", "nested")
	report := layout.MigrateReport{SchemaVersion: 1}

	if err := writeLayoutMigrateReports(directory, report); err != nil {
		t.Fatalf("writeLayoutMigrateReports returned %v, want nil", err)
	}

	md, err := os.ReadFile(filepath.Join(directory, "layout-migrate.md"))
	if err != nil {
		t.Fatalf("read layout-migrate.md: %v", err)
	}
	if len(md) == 0 {
		t.Fatal("layout-migrate.md is empty")
	}

	raw, err := os.ReadFile(filepath.Join(directory, "layout-migrate.json"))
	if err != nil {
		t.Fatalf("read layout-migrate.json: %v", err)
	}
	var decoded layout.MigrateReport
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal layout-migrate.json: %v", err)
	}
	if decoded.SchemaVersion != report.SchemaVersion {
		t.Fatalf("decoded SchemaVersion = %d, want %d", decoded.SchemaVersion, report.SchemaVersion)
	}

	if _, err := os.Stat(filepath.Join(directory, "layout-migrate.yaml")); err != nil {
		t.Fatalf("stat layout-migrate.yaml: %v", err)
	}
}
