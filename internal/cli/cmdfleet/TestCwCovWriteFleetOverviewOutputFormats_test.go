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

func TestCwCovWriteFleetOverviewOutputFormats(t *testing.T) {
	t.Parallel()
	report := fleetinspect.OverviewReport{SchemaVersion: 1, Stats: fleetinspect.StatsReport{Inventory: fleetinspect.InventoryStats{Repositories: 1}}}
	reportDir := "reports"
	deps := fakeDependencies()
	files := map[string][]byte{}
	deps.WriteFile = func(path string, data []byte, _ os.FileMode) error {
		files[path] = append([]byte(nil), data...)
		return nil
	}
	for _, format := range []string{"markdown", "yaml", "json"} {
		var err error
		var buffer bytes.Buffer
		err = writeFleetOverviewOutput(&buffer, deps, report, format, reportDir, false)
		out := buffer.String()
		if err != nil {
			t.Fatalf("format %s: %v", format, err)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("format %s produced no stdout", format)
		}
	}
	for _, name := range []string{"fleet-overview.md", "fleet-overview.yaml"} {
		if _, err := memoryRead(files, filepath.Join(reportDir, name)); err != nil {
			t.Fatalf("report file %s: %v", name, err)
		}
	}
	if err := writeFleetOverviewOutput(io.Discard, deps, report, "toml", "", false); err == nil {
		t.Fatal("unknown format was accepted")
	}
}
