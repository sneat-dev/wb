package cmdfleet

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/prinventory"
)

func TestCwCovWritePRInventoryOutput(t *testing.T) {
	report := prinventory.Report{SchemaVersion: 1, Complete: true}
	reportDir := "reports"
	deps := fakeDependencies()
	files := map[string][]byte{}
	deps.WriteFile = func(path string, data []byte, _ os.FileMode) error {
		files[path] = append([]byte(nil), data...)
		return nil
	}
	for _, format := range []string{"markdown", "json"} {
		command := NewPRs(testRuntime(&shared.Flags{}), deps)
		var out bytes.Buffer
		command.SetOut(&out)
		command.SetErr(&out)
		if err := writePRInventoryOutput(command.OutOrStdout(), deps, report, format, reportDir); err != nil {
			t.Fatalf("format %s: %v", format, err)
		}
		if strings.TrimSpace(out.String()) == "" {
			t.Errorf("format %s produced no output", format)
		}
	}
	for _, name := range []string{"pull-request-inventory.json", "pull-request-inventory.md"} {
		data, err := memoryRead(files, filepath.Join(reportDir, name))
		if err != nil {
			t.Fatalf("report file %s: %v", name, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			t.Errorf("%s is empty", name)
		}
	}
	command := NewPRs(testRuntime(&shared.Flags{}), deps)
	if err := writePRInventoryOutput(command.OutOrStdout(), deps, report, "toml", ""); err == nil ||
		!strings.Contains(err.Error(), `unsupported --format "toml"`) {
		t.Fatalf("unsupported format error = %v", err)
	}
}
