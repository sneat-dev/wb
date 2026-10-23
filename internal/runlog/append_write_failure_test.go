package runlog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestAppendWriteFailureLeavesExistingLogIntact(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	original := []byte("previous complete event\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("storage write rejected")
	err := appendInjected(path, Event{State: "requested"}, &filewrite.Injector{Step: filewrite.StepWrite, Err: failure})
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "append run event") {
		t.Fatalf("write error = %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != string(original) {
		t.Fatalf("existing log changed: %q, %v", data, err)
	}
	// The failed append releases its lock and closes the descriptor.
	if err := Append(path, Event{State: "succeeded"}); err != nil {
		t.Fatalf("append after failure: %v", err)
	}
}
