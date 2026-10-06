package cmdquality

import (
	"bytes"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/qualityrun"
	"strings"
	"testing"
)

func TestWriteChangedCoverageOutputToPrintsWarningsInMarkdownFormat(t *testing.T) {
	t.Parallel()
	report := qualityrun.ChangedReport{
		MergeBase: "deadbeef",
		Target:    "main",
		Packages: []quality.PackageRatchet{
			{Package: ".", Uncovered: 1, HasBaseline: true, BaselineUncovered: 1, Pass: true},
		},
		Warnings: []quality.RatchetWarning{
			{Package: "legacy", File: "legacy/old.go", Line: 42},
		},
	}
	var out bytes.Buffer
	if err := writeChangedCoverageOutputTo(&out, report, "markdown"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "WARN legacy legacy/old.go:42") {
		t.Fatalf("output = %q, want a WARN line naming the package and file:line", got)
	}
}
