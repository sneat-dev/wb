package cmdfleet

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/prinventory"
)

type refusedWriter struct{}

func (refusedWriter) Write([]byte) (int, error) { return 0, errors.New("cwDeps: write refused") }
func TestCwDepsWritePRInventoryOutputRefusesAnUnwritableReportDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps := fakeDependencies()
	deps.MkdirAll = os.MkdirAll
	deps.WriteFile = os.WriteFile
	report := prinventory.Report{SchemaVersion: 1, Complete: true, Diagnostics: []prinventory.Diagnostic{{Severity: "error", Message: "owner listing was partial"}}}
	err := writePRInventoryOutput(&bytes.Buffer{}, deps, report, "markdown", filepath.Join(blocker, "reports"))
	if err == nil {
		t.Fatal("a report directory beneath a file must fail")
	}
	// A failed stdout write is surfaced too.
	if err := writePRInventoryOutput(refusedWriter{}, deps, report, "markdown", ""); err == nil ||
		!strings.Contains(err.Error(), "write refused") {
		t.Fatalf("failing writer = %v", err)
	}
	if err := writePRInventoryOutput(refusedWriter{}, deps, report, "json", ""); err == nil ||
		!strings.Contains(err.Error(), "write refused") {
		t.Fatalf("failing json writer = %v", err)
	}
}
