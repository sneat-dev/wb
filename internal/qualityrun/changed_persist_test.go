package qualityrun

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteChangedCoverageOutputToFailsClosedWhenReportDirIsBlocked(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(blocker, "reports") // blocker is a file, not a directory
	err := PersistChanged(ChangedReport{}, reportDir)
	if err == nil {
		t.Fatal("want error when the report directory's parent is a regular file")
	}
}
func TestWriteChangedCoverageOutputToFailsClosedWhenReportFileIsBlocked(t *testing.T) {
	t.Parallel()
	reportDir := t.TempDir()
	// Pre-create the destination filename as a directory so the write fails.
	if err := os.MkdirAll(filepath.Join(reportDir, "coverage-ratchet.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := PersistChanged(ChangedReport{}, reportDir)
	if err == nil {
		t.Fatal("want error when the report file path is already a directory")
	}
}
