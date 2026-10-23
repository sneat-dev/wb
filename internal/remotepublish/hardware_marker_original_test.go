package remotepublish

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestHardwareMarkerEmptyAndUnwritablePathsPreserveBestEffortPolicy(t *testing.T) {
	t.Parallel()
	var progress bytes.Buffer
	noteHardware("", &progress)
	recordHardwareNoted(noteHardware(filepath.Join(t.TempDir(), "absent", "wb.yaml"), &progress))
	recordHardwareNoted("")
}

func TestMarkerInspectionAndWriteRefusalsRemainBestEffort(t *testing.T) {
	t.Parallel()
	parent := filepath.Join(t.TempDir(), "regular-parent")
	if err := os.WriteFile(parent, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := hardwareNoteMarker(filepath.Join(parent, "wb.yaml"))
	if marker == "" {
		t.Fatal("actual ENOTDIR inspection consumed the notice")
	}
	recordHardwareNoted(marker)
	raw, err := os.ReadFile(parent)
	if err != nil || string(raw) != "unchanged" {
		t.Fatalf("private parent=%q %v", raw, err)
	}
}
