package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestMetricsAppendFailuresRetainExistingRecords(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		step     filewrite.Step
		prefix   string
		appended bool
	}{
		{filewrite.StepOpenOrCreate, "open hook metrics", false},
		{filewrite.StepChmod, "protect hook metrics", false},
		{filewrite.StepWrite, "append hook metrics", false},
		{filewrite.StepClose, "close hook metrics", true},
	} {
		t.Run(string(tc.step), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "events.jsonl")
			original := []byte("retained prior record\n")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			refused := errors.New("native operation refused")
			err := appendEventsInjected(path, []Event{{SchemaVersion: EventSchemaVersion, Timestamp: time.Unix(10, 0), Hook: "pre-commit"}}, &filewrite.Injector{Step: tc.step, Err: refused})
			if !errors.Is(err, refused) || !strings.Contains(err.Error(), tc.prefix) {
				t.Fatalf("append error=%v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(after), string(original)) {
				t.Fatalf("prior records changed: %q", after)
			}
			if tc.appended {
				if !strings.Contains(string(after), `"hook":"pre-commit"`) {
					t.Fatalf("close failure lost appended record: %q", after)
				}
			} else if string(after) != string(original) {
				t.Fatalf("failed append changed records: %q", after)
			}
		})
	}
}

func TestMetricsEncodingFailureRetainsExistingRecords(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	original := []byte("retained prior record\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	writes := 0
	err := appendEventsInjected(path, []Event{{Timestamp: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}, &filewrite.Injector{Step: filewrite.StepWrite, Hook: func() { writes++ }})
	if err == nil || writes != 0 {
		t.Fatalf("encoding error=%v writes=%d", err, writes)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatalf("prior records=%q error=%v", after, err)
	}
}
