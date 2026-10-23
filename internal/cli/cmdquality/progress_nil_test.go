package cmdquality

import (
	"bytes"
	"github.com/sneat-dev/wb/internal/quality"
	"testing"
)

func TestQualityProgressNilReceiverIsANoOp(t *testing.T) {
	t.Parallel()
	var progress *qualityProgress
	progress.start()
	progress.report(quality.Progress{State: quality.ProgressStarted})
	progress.finish()
}
func TestQualityProgressZeroTotalIsANoOp(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newQualityProgress(&out, true, "verify", 0)
	progress.start()
	progress.report(quality.Progress{State: quality.ProgressStarted})
	progress.finish()
	if out.Len() != 0 {
		t.Fatalf("zero-total progress wrote %q, want nothing", out.String())
	}
}
func TestQualityProgressReportDefaultsEmptyModuleToDot(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newQualityProgress(&out, true, "verify", 1)
	progress.start()
	progress.report(quality.Progress{
		Repository: "acme/one", Command: "go vet", State: quality.ProgressStarted,
	})
	rendered := out.String()
	const want = "acme/one . — go vet: started"
	if !bytes.Contains([]byte(rendered), []byte(want)) {
		t.Fatalf("progress output missing %q: %q", want, rendered)
	}
}
