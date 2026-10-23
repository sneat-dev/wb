package progress

import (
	"bytes"
	"github.com/sneat-dev/wb/internal/progress"
	"strings"
	"testing"
)

// TestCIWaitProgressOperationReporterRendersCounts drives
// operationReporter's `event.Completed > 0 || event.Total > 0` branch.
func TestCIWaitProgressOperationReporterRendersCounts(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	p := NewChecksWithHeartbeat(&out, true, 0)
	reporter := p.OperationReporter("ci audit")
	reporter(progress.Event{Completed: 2, Total: 5})
	if !strings.Contains(out.String(), "2/5") {
		t.Fatalf("output = %q; want it to render the 2/5 count", out.String())
	}
}

// TestShortRevisionTruncatesLongRevisions drives shortRevision's truncation
// branch and its pass-through branch.
func TestShortRevisionTruncatesLongRevisions(t *testing.T) {
	t.Parallel()
	if got := shortRevision("abcdef0123456789"); got != "abcdef012345" {
		t.Fatalf("shortRevision(long) = %q; want the first 12 characters", got)
	}
	if got := shortRevision("abc123"); got != "abc123" {
		t.Fatalf("shortRevision(short) = %q; want it unchanged", got)
	}
}
