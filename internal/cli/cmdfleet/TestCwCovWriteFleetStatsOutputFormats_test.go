package cmdfleet

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/fleetinspect"
)

func TestCwCovWriteFleetStatsOutputFormats(t *testing.T) {
	report := fleetinspect.StatsReport{SchemaVersion: 1, Inventory: fleetinspect.InventoryStats{Repositories: 2}}
	reportDir := "reports"
	deps := fakeDependencies()
	files := map[string][]byte{}
	deps.WriteFile = func(path string, data []byte, _ os.FileMode) error {
		files[path] = append([]byte(nil), data...)
		return nil
	}

	for _, format := range []string{"markdown", "yaml", "json"} {
		var out string
		var err error
		var buffer bytes.Buffer
		err = writeFleetStatsOutput(&buffer, deps, report, format, reportDir)
		out = buffer.String()
		if err != nil {
			t.Fatalf("format %s: %v", format, err)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("format %s produced no stdout", format)
		}
	}
	for _, name := range []string{"fleet-stats.md", "fleet-stats.yaml"} {
		data, readErr := memoryRead(files, filepath.Join(reportDir, name))
		if readErr != nil {
			t.Fatalf("report file %s: %v", name, readErr)
		}
		if !strings.Contains(string(data), "schema_version") && !strings.Contains(string(data), "# WB fleet stats") {
			t.Errorf("%s does not contain the report: %s", name, data)
		}
	}
	err := writeFleetStatsOutput(io.Discard, deps, report, "toml", "")
	if err == nil || !strings.Contains(err.Error(), `unknown --format "toml"`) {
		t.Fatalf("unknown format error = %v", err)
	}
}
