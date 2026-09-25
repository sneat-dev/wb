package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAppendEventsReportsEncodingFailure covers AppendEvents' JSON-encode
// error branch: a Timestamp whose year is out of encoding/json's [0,9999]
// range makes time.Time's own MarshalJSON fail, which AppendEvents must
// surface rather than silently dropping the event or writing partial data.
func TestAppendEventsReportsEncodingFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "metrics.jsonl")
	event := Event{
		SchemaVersion: EventSchemaVersion,
		Timestamp:     time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		Repository:    "owner/repo",
		Hook:          "pre-commit",
		Action:        "run",
		Outcome:       "pass",
	}
	err := AppendEvents(path, []Event{event})
	if err == nil || !strings.Contains(err.Error(), "year outside of range") {
		t.Fatalf("AppendEvents(out-of-range year) = %v, want a year-out-of-range encoding error", err)
	}
	// A failed encode still leaves behind the empty metrics file that
	// OpenFile(O_CREATE) created before encoding ran: AppendEvents does not
	// clean it up on this error path. Assert the concrete outcome (empty
	// file, not absent) rather than a vague existence check.
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("stat metrics file after failed encode: %v", statErr)
	}
	if info.Size() != 0 {
		t.Fatalf("metrics file size after failed encode = %d, want 0 (no partial event written)", info.Size())
	}
}
